// Package engine wires every subsystem — typed KV store, semantic
// cache, LLM cache, memory store, pub/sub broker, eviction loop, key
// versioning for WATCH, and persistence (AOF + RDB).
package engine

import (
	"bufio"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"bytes"
	"compress/gzip"
	"encoding/json"
	"net"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/acl"
	"github.com/dhiravpatel/neurocache/apps/api/internal/blocking"
	"github.com/dhiravpatel/neurocache/apps/api/internal/cluster"
	"github.com/dhiravpatel/neurocache/apps/api/internal/config"
	"github.com/dhiravpatel/neurocache/apps/api/internal/eviction"
	"github.com/dhiravpatel/neurocache/apps/api/internal/aiops"
	"github.com/dhiravpatel/neurocache/apps/api/internal/introspect"
	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
	"github.com/dhiravpatel/neurocache/apps/api/internal/memory"
	"github.com/dhiravpatel/neurocache/apps/api/internal/metrics"
	"github.com/dhiravpatel/neurocache/apps/api/internal/modules"
	"github.com/dhiravpatel/neurocache/apps/api/internal/persistence"
	"github.com/dhiravpatel/neurocache/apps/api/internal/primitives"
	"github.com/dhiravpatel/neurocache/apps/api/internal/pubsub"
	"github.com/dhiravpatel/neurocache/apps/api/internal/replication"
	"github.com/dhiravpatel/neurocache/apps/api/internal/retrieval"
	"github.com/dhiravpatel/neurocache/apps/api/internal/scripting"
	"github.com/dhiravpatel/neurocache/apps/api/internal/sentinel"
	"github.com/dhiravpatel/neurocache/apps/api/internal/semcache"
	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
)

type Engine struct {
	Cfg      config.Config
	Log      *slog.Logger
	KV       *store.Store
	Semantic *semcache.Store
	LLM      *semcache.Store
	Memory   *memory.Store
	Scorer   eviction.Scorer
	Metrics  *metrics.Metrics
	PubSub   *pubsub.Broker
	ACL      *acl.Manager
	SlowLog  *introspect.SlowLog
	Latency  *introspect.LatencyMonitor
	Clients  *introspect.ClientRegistry
	Monitor  *introspect.MonitorBroker
	Tracking *introspect.TrackingTable
	Scripts   *scripting.Cache
	Functions *scripting.FunctionRegistry
	Blocker   *blocking.Hub

	Replication *replication.State
	Backlog     *replication.Backlog
	Master      *replication.Master
	ReplClient  *replication.Client

	Cluster *cluster.State
	Bus     *cluster.Bus

	Modules *modules.Registry

	RuntimeCfg *config.Runtime

	Sentinel *sentinel.Sentinel

	// NeuroCache-only primitives (no Redis equivalent).
	Idempotent  *primitives.IdempotencyStore
	Locks       *primitives.LockManager
	RateLimit   *primitives.RateLimiter
	Dedup       *primitives.Deduper
	CostTable   *primitives.CostTable
	History     *primitives.HistoryStore
	Recommender *primitives.Recommender

	// LLM-stack primitives — embedding cache, conversation/session
	// management, versioned prompt templates. Each closes a real gap
	// every LLM app rebuilds in client code; centralizing them here
	// gives uniform persistence (AOF), replication, ACL, and metrics.
	EmbCache      *llmstack.EmbCache
	Conversations *llmstack.Conversations
	Prompts       *llmstack.Prompts

	// Tool result memoization for AI agents — cache the result of a
	// tool/function call by (tool, normalized-args) so repeated calls
	// within a TTL skip the upstream round-trip. Lock-free reads via
	// sync.Map + atomic counters; stats track $ saved.
	ToolCache *llmstack.ToolCache

	// LLM cost guardrails — per-scope (user/session/global) hard $
	// caps with atomic enforcement. Apps call CostGuard.Check before
	// every chargeable LLM call so a runaway agent loop or leaked
	// API key can't burn through the bill before someone notices.
	CostGuard *llmstack.CostGuard

	// Prompt analytics — group prompts by a normalization-robust
	// fingerprint so ops can see "users are sending 50 variants of
	// the same template." Used for cost analysis, prompt-injection
	// detection, and cache-hit tuning.
	PromptAnalytics *llmstack.PromptAnalytics

	// Negative semantic cache — remembers queries that recently
	// returned no SEMANTIC_GET match. Future identical queries
	// short-circuit before the O(N) cosine scan.
	NegSemCache *semcache.NegCache

	// LLM provider failover ladder — atomic health bits per
	// provider, lock-free Next() picks the first healthy one in
	// the configured route. When OpenAI 429s, calls automatically
	// fall through to the next provider in the ladder.
	LLMRouter *llmstack.LLMRouter

	// Prompt-injection scanner — built-in pattern library covering
	// instruction overrides, role-flips, system-prompt extraction,
	// jailbreak preambles, encoded payloads, and delimiter
	// confusion. Apps call INJECT.SCAN before forwarding any
	// prompt to the model.
	InjectScanner *llmstack.InjectScanner

	// Token counting + budget tracker. Lets apps predict cost,
	// prevent context overflow, and split long docs into
	// per-model-fit chunks — without writing the tokenizer logic
	// in client code (which can't ship the BPE tables).
	Tokens *llmstack.Tokens

	// Text chunker for RAG ingestion + token-aware context window
	// assembly. Replaces the chunk-then-assemble glue every RAG app
	// rebuilds.
	Chunker *llmstack.Chunker

	// PII redaction with restore tokens. Strips emails / phones /
	// SSNs / credit cards / IP addresses / API keys before they hit
	// an external model, and swaps the originals back into the
	// model's response. Solves GDPR/HIPAA + prompt-injection at one
	// hop.
	Redactor *llmstack.Redactor

	// Citation grounding scorer. Splits LLM output into sentences
	// and computes max Jaccard overlap against the source passages
	// the answer was supposed to ground itself in. Detects
	// fabrications / fact swaps / made-up numbers before the answer
	// reaches the user.
	Ground *llmstack.GroundChecker

	// Prompt canary deployments. Routes a configurable fraction of
	// traffic to a candidate prompt, tallies per-arm scores, and
	// auto-rolls back when the candidate regresses below a delta
	// threshold. Lightweight alternative to a full A/B service for
	// the "ship a prompt tweak safely" pain point.
	Canaries *llmstack.CanaryDeploys

	// Cross-encoder rerank cache. Memoizes (query, doc_id) -> score
	// so the second time the same pair shows up the rerank-call cost
	// drops to zero. Bulk SCORE API is the production hot path:
	// returns cached scores + a parallel hits bitmap so apps know
	// which pairs still need an upstream call.
	Rerank *llmstack.RerankCache

	// LLM-as-judge eval suite. Stores test cases (input + expected +
	// grader) per prompt-id, accepts actual outputs from the app's
	// own LLM call, scores them with one of five graders (exact /
	// contains / regex / numeric_within / llm), and tracks pass-rate
	// over a sliding window for regression alerts.
	Judge *llmstack.JudgeSuite

	// Few-shot example library with semantic retrieval. Stores
	// labeled (input, output) examples per bank; QUERY returns the
	// top-K most-similar examples for in-context learning. Optional
	// tag filter for multi-tenant banks.
	FewShot *llmstack.FewShotBank

	// Composable safety pipeline. GUARDRAIL.RUN executes a named
	// chain of stages (inject + redact + ground + length + regex
	// blocks + custom verdicts) and returns a per-stage breakdown
	// + the final mutated text. Replaces the bespoke safety glue
	// every team rebuilds.
	Guardrail *llmstack.GuardrailManager

	// JSON schema validation + auto-repair-prompt generation. Apps
	// validate LLM-generated JSON against a registered schema; on
	// failure, REPAIR_PROMPT synthesises a clear "your output didn't
	// match, fix it" instruction the app passes back to the model.
	Struct *llmstack.StructValidator

	// Single-flight thundering-herd protection. When 100 users all
	// ask the same popular question simultaneously, only the first
	// COALESCE.LOCK winner fires the upstream call; the rest WAIT
	// and share the result. Per-key channels close on PUBLISH so
	// thousands of waiters wake up at once with no polling.
	Coalesce *llmstack.Coalescer

	// Hedged-call tracker. Apps fire the same prompt to N providers
	// in parallel; HEDGE deduplicates results, records who-won, and
	// per-provider latency stats. Atomic CAS on winner_idx ensures
	// only one publisher claims the winner slot under concurrent
	// publishes. Lock-free reads via sync.Map; per-call channel
	// broadcast wakeup for waiters.
	Hedge *llmstack.HedgeTracker

	// Self-consistency consensus over N model samples. For high-
	// stakes outputs (medical / legal / code), three voting
	// strategies: exact (bucket by string match), medoid (highest
	// token-Jaccard to all others), cluster (largest semantic
	// cluster by cosine over hashed-BoW). Returns chosen sample +
	// confidence + bucket breakdown.
	Verify *llmstack.VerifyManager

	// Query-rewrite cache for hyDE / step-back / decompose /
	// multi-query / paraphrase. Lock-free reads, soft 50k cap,
	// per-technique hit-rate tracking, saved_usd reporting.
	Rewrite *llmstack.RewriteCache

	// Citation extractor + validator. Parses "[N]" / "[Source-A]"
	// markers in LLM output and resolves them against caller-
	// supplied source passages. Catches hallucinated citation IDs +
	// missing/unreferenced sources. Lock-free, atomic counters.
	Citations *llmstack.CitationExtractor

	// Prompt compression — composable strategies (whitespace +
	// stopwords + truncate). Reports tokens-saved + ratio so
	// dashboards track the cost reduction. Pure compute, no state.
	Shrinker *llmstack.PromptShrinker

	// Agent step-budget enforcer. STEP increments atomic counters
	// and checks every cap (max_steps / max_tool_calls / max_tokens
	// / max_time_ms); returns should_stop=true on the first
	// breach. Prevents runaway agents from blowing through token
	// budgets. Lock-free, sub-100ns per step.
	AgentLoop *llmstack.AgentLoopTracker

	// Semantic deduplication for streams. SEEN does dedup-check-
	// and-insert in a single round trip: cosine over a per-bucket
	// FIFO window. Catches paraphrases that hash dedup misses.
	// 128-dim hashed-BoW fallback or app-supplied embeddings.
	SemDedup *llmstack.SemDeduper

	// KV-cache-aware prefix routing. Tracks which workers have
	// which prompt prefixes warm so apps route to a warm worker
	// instead of round-robin. Lock-free reads via sync.Map nested
	// in sync.Map. Sub-microsecond LOOKUP at typical N.
	PrefixRouter *llmstack.PrefixRouter

	// Tool schema registry with semantic capability search. Apps
	// register N tools once; SEARCH returns the top-K relevant to
	// a query so the LLM's function-call manifest stays slim.
	// Replaces the "give me only tools relevant to THIS query"
	// glue every team writes.
	Toolbox *llmstack.ToolBox

	// Translation cache by (source_lang, target_lang, text).
	// Translation calls are massively cacheable — same input always
	// produces the same output and queries repeat across users.
	// Sub-microsecond hash-keyed lookup; per-language-pair stats.
	Translate *llmstack.TranslateCache

	// Inline embedding matrix with server-side cosine top-K.
	// Replaces "ship vectors to a separate vector DB just for math"
	// for small-scale (sub-100k row) retrieval workloads. Vectors
	// stored L2-normalised so cosine = dot product on the hot path.
	EmbedMat *llmstack.EmbedMatrix

	// Deterministic LLM operation memoisation. Exact-match cache
	// keyed by (op_id, input, model, params) — for temp=0 workloads
	// where the same prompt MUST yield the same output (code
	// generation, SQL synthesis, structured extraction). Distinct
	// from the semantic cache which matches paraphrases.
	OpCache *llmstack.OpCache

	// Per-list sorted-string prefix completion. Sub-microsecond
	// SUGGEST for chat suggestion UIs, command palettes, gazetteer
	// matching, NER lookups. Score-weighted top-K.
	Autocomplete *llmstack.Autocomplete

	// Multi-step workflow state machine with named artifact storage.
	// Apps DEFINE a chain, START runs, DONE each step (storing the
	// artifact); RESUME after a crash returns the next pending step
	// + every artifact produced so far. Crash-safe agentic
	// orchestration without losing intermediate work.
	ChainState *llmstack.ChainStateMgr

	// Mixture-of-Experts router. Routes queries to the right expert
	// model by combining cosine capability match × live success-
	// rate health. Atomic counter updates from RECORD; lock-free
	// ROUTE hot path. Smart model selection without hand-coded
	// rules.
	MoE *llmstack.MoERouter

	// Phase 11 — extended AI-ops primitives. Each replaces a layer
	// every team rebuilds: agent tool caches, streaming-replay,
	// per-tenant cost budgets, stale-while-revalidate, multi-persona
	// memory, moderation cache + injection detector, provenance
	// tracking, SLO breach signal, A/B experiments, knowledge graph,
	// scheduler, event log + projections, RBAC verdict cache, LLM
	// proxy, and an MCP server.
	AgentCache  *aiops.AgentToolCache
	StreamCache *aiops.StreamCache
	CostBudgets *aiops.CostBudgets
	Shadow      *aiops.Shadow
	Personas    *aiops.Personas
	Moderation  *aiops.Moderation
	Lineage     *aiops.Lineage
	SLOTracker  *aiops.SLOTracker
	Experiments *aiops.Experiments
	Graph       *aiops.Graph
	Scheduler   *aiops.Scheduler
	EventLog    *aiops.EventLog
	Policies    *aiops.Policies
	Inference   *aiops.Inference
	MCP         *aiops.MCP

	// Retrieval is the per-engine registry of hybrid (BM25 + vector +
	// RRF) indexes. Backs RETRIEVE.* and RAG.QUERY (GraphRAG).
	Retrieval *retrieval.Manager
	// Phase 12 — uniqueness primitives. Things Redis genuinely doesn't
	// have at the cache layer: tagged invalidation, production job
	// queues, feature flags, structured audit trails, in-memory
	// distributed tracing, JSON-Patch document sync, and a native
	// Prometheus exporter.
	Churn   *aiops.ChurnTags
	Workers *aiops.Workers
	Flags   *aiops.Flags
	Audit   *aiops.Audit
	Tracer  *aiops.Tracer
	Docs    *aiops.Docs
	Observe *aiops.Observe

	// Phase 13 — resilience & coordination primitives. Three families
	// genuinely beyond Redis: distributed circuit breakers (sliding-
	// window failure-rate trip + half-open probing), long-running
	// workflow orchestration with compensation (saga pattern), and
	// conflict-free replicated data types (G/PN-counters, OR-Set,
	// LWW-Register). All persist via AOF + replication and are gated
	// under the @ai ACL category.
	Circuits *aiops.Circuits
	Sagas    *aiops.Sagas
	CRDTs    *aiops.CRDTRegistry

	// HotKeys is the runtime top-K access tracker driven by the
	// keyspace notifier. Replaces the awkward `redis-cli --hotkeys`
	// scan + LFU-only OBJECT FREQ approach with a HeavyKeeper-backed
	// streaming top-K answerable in O(K log K).
	HotKeys *introspect.HotKeys

	// replayRunner is the command applier the replica client uses. We
	// stash it so (a) FollowMaster can restart the client after a role
	// flip without re-wiring, and (b) tests can swap in a no-op.
	replayRunner func(cmd string, args []string) error

	AOF *persistence.AOF
	RDB *persistence.RDB

	StartedAt time.Time
	CmdCount  atomic.Uint64
	stopCh    chan struct{}
	// Clock is a process-wide cached monotonic clock used by the RESP
	// dispatch hot path. See engine/clock.go for the rationale.
	Clock *FastClock

	// lastSave is the unix timestamp of the most recent successful RDB
	// write (manual or scheduled). Seeded from the on-disk file's mtime
	// at boot so LASTSAVE survives restarts.
	lastSave atomic.Int64

	// bgSaveBusy / bgRewriteBusy throttle BGSAVE and BGREWRITEAOF to a
	// single concurrent operation. Redis also rejects re-entrant calls.
	bgSaveBusy    atomic.Bool
	bgRewriteBusy atomic.Bool

	vmu      sync.RWMutex
	versions map[string]uint64
}

func New(cfg config.Config, log *slog.Logger) *Engine {
	aclMgr := acl.NewManager(log)
	if path := acl.ResolvePath(cfg.ACLFile, cfg.DataDir); path != "" {
		if err := aclMgr.LoadFile(path); err != nil {
			log.Warn("acl load failed", "err", err, "path", path)
		}
	}
	if cfg.RequirePass != "" {
		// Legacy "requirepass" — set the default user's password so
		// callers can `AUTH <pass>` without a username.
		aclMgr.SetRequirePass(cfg.RequirePass)
	}

	e := &Engine{
		Cfg:       cfg,
		Log:       log,
		KV:        store.New(),
		Semantic:  semcache.New(cfg.EmbeddingDim, "semantic"),
		LLM:       semcache.New(cfg.EmbeddingDim, "llm"),
		Memory:    memory.New(cfg.EmbeddingDim),
		Scorer:    eviction.NewScorer(cfg.Eviction),
		Metrics:   metrics.New(),
		PubSub:    pubsub.New(64),
		ACL:       aclMgr,
		SlowLog:   introspect.NewSlowLog(cfg.SlowLogMaxLen, time.Duration(cfg.SlowLogThreshold)*time.Microsecond),
		Latency:   newLatencyMonitorWithDefault(cfg.LatencyMaxLen),
		Clients:   introspect.NewClientRegistry(),
		Monitor:   introspect.NewMonitorBroker(),
		Tracking:  introspect.NewTrackingTable(),
		Scripts:   scripting.NewCache(),
		Functions: scripting.NewFunctionRegistry(),
		Blocker:   blocking.NewHub(),
		StartedAt: time.Now(),
		Clock:     NewFastClock(),

		Replication: replication.NewState(),
		Backlog:     replication.NewBacklog(cfg.ReplBacklogSize),
		Cluster:     cluster.NewState(),
		stopCh:    make(chan struct{}),
		versions:  map[string]uint64{},
	}
	e.Modules = modules.NewRegistry(&moduleHandle{e: e})
	e.RuntimeCfg = config.NewRuntime(&e.Cfg)
	e.Idempotent = primitives.NewIdempotencyStore()
	e.Locks = primitives.NewLockManager()
	e.RateLimit = primitives.NewRateLimiter()
	e.Dedup = primitives.NewDeduper()
	e.CostTable = primitives.NewCostTable()
	e.History = primitives.NewHistoryStore(64, 24*time.Hour)
	e.Recommender = primitives.NewRecommender()
	e.EmbCache = llmstack.NewEmbCache()
	e.Conversations = llmstack.NewConversations()
	e.Prompts = llmstack.NewPrompts()
	e.ToolCache = llmstack.NewToolCache()
	e.CostGuard = llmstack.NewCostGuard()
	e.PromptAnalytics = llmstack.NewPromptAnalytics()
	e.NegSemCache = semcache.NewNegCache()
	e.LLMRouter = llmstack.NewLLMRouter()
	e.InjectScanner = llmstack.NewInjectScanner()
	e.Tokens = llmstack.NewTokens()
	e.Chunker = llmstack.NewChunker(e.Tokens)
	e.Redactor = llmstack.NewRedactor()
	e.Ground = llmstack.NewGroundChecker()
	e.Canaries = llmstack.NewCanaryDeploys()
	e.Rerank = llmstack.NewRerankCache()
	e.Judge = llmstack.NewJudgeSuite()
	e.FewShot = llmstack.NewFewShotBank()
	e.Guardrail = llmstack.NewGuardrailManager()
	e.Guardrail.SetEngine(e.InjectScanner, e.Redactor, e.Ground)
	e.Struct = llmstack.NewStructValidator()
	e.Coalesce = llmstack.NewCoalescer()
	e.Hedge = llmstack.NewHedgeTracker()
	e.Verify = llmstack.NewVerifyManager()
	e.Rewrite = llmstack.NewRewriteCache()
	e.Citations = llmstack.NewCitationExtractor()
	e.Shrinker = llmstack.NewPromptShrinker(e.Tokens)
	e.AgentLoop = llmstack.NewAgentLoopTracker()
	e.SemDedup = llmstack.NewSemDeduper()
	e.PrefixRouter = llmstack.NewPrefixRouter()
	e.Toolbox = llmstack.NewToolBox()
	e.Translate = llmstack.NewTranslateCache()
	e.EmbedMat = llmstack.NewEmbedMatrix()
	e.OpCache = llmstack.NewOpCache()
	e.Autocomplete = llmstack.NewAutocomplete()
	e.ChainState = llmstack.NewChainStateMgr()
	e.MoE = llmstack.NewMoERouter()

	// Phase 11 — instantiate every AI-ops manager. Schedulers and the
	// inference proxy take engine-level wiring after construction so
	// they can call back into the dispatcher / register providers.
	e.AgentCache = aiops.NewAgentToolCache()
	e.StreamCache = aiops.NewStreamCache()
	e.CostBudgets = aiops.NewCostBudgets()
	e.Shadow = aiops.NewShadow(nil)
	e.Personas = aiops.NewPersonas()
	e.Moderation = aiops.NewModeration()
	e.Lineage = aiops.NewLineage()
	e.SLOTracker = aiops.NewSLOTracker()
	e.Experiments = aiops.NewExperiments()
	e.Graph = aiops.NewGraph()
	e.Scheduler = aiops.NewScheduler()
	e.EventLog = aiops.NewEventLog()
	e.Policies = aiops.NewPolicies(nil)
	e.Inference = aiops.NewInference()
	e.MCP = aiops.NewMCP()
	e.Retrieval = retrieval.NewManager(cfg.EmbeddingDim)
	e.registerMCPCatalog()

	// Phase 13 — instantiate the resilience & coordination primitives.
	// These are pure in-memory state managers; no goroutines to start.
	e.Circuits = aiops.NewCircuits()
	e.Sagas = aiops.NewSagas()
	e.CRDTs = aiops.NewCRDTRegistry()
	// Phase 12 — instantiate the uniqueness primitives. Wire CHURN to
	// the engine's keyspace deleter so CHURN.INVALIDATE actually
	// drops keys; default the Audit retention to 1M events.
	e.Churn = aiops.NewChurnTags()
	e.Churn.SetInvalidator(func(keys []string) int {
		return e.KV.Del(keys...)
	})
	e.Workers = aiops.NewWorkers()
	e.Flags = aiops.NewFlags()
	e.Audit = aiops.NewAudit()
	e.Tracer = aiops.NewTracer()
	e.Docs = aiops.NewDocs()
	e.Observe = aiops.NewObserve()
	// Register baseline OBSERVE counters/gauges that map to engine
	// state. Values are pulled at scrape time so we don't pay for
	// instrumentation when nobody's scraping.
	e.Observe.RegisterCounter("neurocache_commands_total", "Total RESP commands dispatched.", nil)
	e.Observe.RegisterGauge("neurocache_keyspace_size", "Number of live keys.", nil)
	e.Observe.RegisterGauge("neurocache_bytes_used", "Approximate bytes used by the keyspace.", nil)
	e.HotKeys = introspect.NewHotKeys(introspect.HotKeysOptions{
		K:           cfg.HotKeysK,
		SampleEvery: cfg.HotKeysSample,
	})
	e.KV.SetNotifier(func(event, key string) {
		e.BumpKey(key)
		if key == "" {
			return
		}
		// Feed the hot-key tracker. Sampling + atomic counter live
		// inside HotKeys so this branch is essentially a single load
		// + one atomic add when sampling skips the event.
		if e.HotKeys != nil {
			e.HotKeys.Record(key)
		}
		// Keyspace-notification fast-path. Without this, every write
		// command paid two string concats ("__keyspace__:"+key,
		// "__keyevent__:"+event) plus two PubSub.Publish calls (each
		// taking an RLock + iterating the psubscriber map) on the
		// chance that a CONFIG SET notify-keyspace-events client is
		// listening. The atomic load below short-circuits when nobody
		// has subscribed — the steady state for any cache without
		// keyspace events configured.
		if e.PubSub.HasSubscribers() {
			e.PubSub.Publish("__keyspace__:"+key, event)
			e.PubSub.Publish("__keyevent__:"+event, key)
		}
		// Server-assisted client caching: fan out invalidations to
		// every client that read this key (default mode) or whose
		// PREFIX subscriptions match (BCAST mode). The pump goroutine
		// on each receiving conn turns this into a RESP3 Push frame.
		// HasActive is an atomic load — when no client has CLIENT
		// TRACKING ON (the steady state), we skip the RWLock-protected
		// scan inside Invalidations entirely.
		if e.Tracking != nil && e.Tracking.HasActive() {
			for _, t := range e.Tracking.Invalidations(key, 0) {
				e.invalidateClient(t.ClientID, []string{key})
			}
		}
		// KEY.TRACK time-travel — snapshot the new value when this key
		// is opted into versioning. HasAny is a lock-free atomic load;
		// when nobody's opted any key into KEY.TRACK (the steady state)
		// we skip the IsTracked RLock + GetTyped on every write.
		if e.History != nil && e.History.HasAny() && e.History.IsTracked(key) {
			if v, ok, _ := e.KV.GetTyped(key); ok {
				e.History.Snapshot(key, v)
			}
		}
		// Wake any blocked clients (BLPOP/BRPOP/BLMOVE/BZPOPMIN/BZPOPMAX
		// /XREAD BLOCK). The blocker filters by event below — only writes
		// that produce something a consumer can pop need to fire.
		switch event {
		case "lpush", "rpush", "rpoplpush", "lpushx", "rpushx",
			"zadd", "zincrby",
			"xadd",
			"set", "setnx", "setex", "psetex", "incr", "decr",
			"incrby", "decrby", "incrbyfloat", "append", "setrange":
			e.Blocker.Notify(key)
		case "del", "expired", "flushdb":
			e.Blocker.NotifyAll(key)
		}
	})
	return e
}

// EnablePersistence opens AOF/RDB handles and restores any prior state.
//
// Load-order rule (matches Redis's default): if AOF is enabled, it is
// the sole source of truth — RDB files are ignored on startup, since
// replaying AOF on top of an RDB would double-apply non-idempotent
// commands like XADD. If only RDB is enabled, we load it. The reverse
// (AOF-only) replays every recorded write.
//
// The caller passes a run function that can execute a command against
// the engine without re-appending it to the AOF; only dispatch knows
// how to turn "SET k v EX 10" into the right store calls.
func (e *Engine) EnablePersistence(run func(cmd string, args []string) error) error {
	if !e.Cfg.AOFEnabled && !e.Cfg.RDBEnabled {
		return nil
	}
	dir := e.Cfg.DataDir

	switch {
	case e.Cfg.AOFEnabled:
		aofPath := filepath.Join(dir, "append.aof")
		if err := persistence.Replay(aofPath, run); err != nil {
			e.Log.Warn("aof replay failed", "err", err)
		} else {
			e.Log.Info("aof replayed", "path", aofPath)
		}
		aof, err := persistence.OpenAOF(aofPath, parseFsyncPolicy(e.Cfg.AOFFsync))
		if err != nil {
			return err
		}
		e.AOF = aof
	case e.Cfg.RDBEnabled:
		rdbPath := filepath.Join(dir, "dump.rdb")
		snap, err := persistence.LoadRDB(rdbPath)
		if err != nil {
			e.Log.Warn("rdb load failed", "err", err)
		} else if snap != nil {
			e.KV.Restore(convertFromRDB(snap.Keys))
			e.Log.Info("rdb loaded", "keys", len(snap.Keys), "at", snap.CreatedAt)
			e.lastSave.Store(snap.CreatedAt.Unix())
		}
	}

	// RDB snapshotting is always wired when enabled — it works fine
	// alongside AOF as a periodic full-state backup. The *load* path
	// is the only place where the two modes are mutually exclusive.
	if e.Cfg.RDBEnabled {
		rdbPath := filepath.Join(dir, "dump.rdb")
		interval := time.Duration(e.Cfg.RDBIntervalSec) * time.Second
		rdb, err := persistence.OpenRDB(rdbPath, interval, e.snapshotFn)
		if err != nil {
			return err
		}
		e.RDB = rdb
	}
	return nil
}

func (e *Engine) Start() {
	go e.evictLoop()
	if e.RDB != nil {
		e.RDB.Start()
	}
	// Scheduler runs delayed commands via the same dispatch path as
	// regular RESP clients — wire the runner here once the replay
	// path is available, then start the dispatcher goroutine.
	if e.Scheduler != nil && e.replayRunner != nil {
		e.Scheduler.SetRunner(func(cmd string, args []string) error {
			return e.replayRunner(cmd, args)
		})
		e.Scheduler.Start()
	}
	// Phase 12 — start the workers sweeper (visibility-timeout +
	// delayed-retry tick).
	if e.Workers != nil {
		e.Workers.Start()
	}
	// SLO breach notifier — fan out to a well-known pub/sub channel
	// so dashboards / alerting can pick it up. Cheap when no
	// subscribers are attached because Publish short-circuits on the
	// empty subscriber set.
	if e.SLOTracker != nil {
		e.SLOTracker.SetNotifier(func(cmd, percentile string, observedMs, targetMs float64) {
			payload := fmt.Sprintf(`{"cmd":%q,"pct":%q,"observed_ms":%.3f,"target_ms":%.3f}`,
				cmd, percentile, observedMs, targetMs)
			e.PubSub.Publish("__slo__:breach", payload)
		})
	}
	e.StartMaster()
	if host, port, ok := ParseReplicaOf(e.Cfg.ReplicaOf); ok {
		e.FollowMaster(host, port)
	}
	if e.Cfg.ClusterEnabled {
		if err := e.startCluster(); err != nil {
			e.Log.Error("cluster bootstrap failed", "err", err)
		}
	}
	e.loadModulesFromConfig()
	if e.Cfg.SentinelEnabled {
		e.startSentinel()
	}
	if e.Cfg.ClusterAutoFailover && e.Cluster != nil && e.Cluster.Enabled() {
		e.startAutoFailover()
	}
}

// startSentinel boots the sentinel monitoring loop. Each entry in
// NEUROCACHE_SENTINEL_MONITOR (`name=host:port:quorum`, comma-separated)
// becomes a watched master.
func (e *Engine) startSentinel() {
	host, port := e.Cfg.Host, e.Cfg.RESPPort
	id := e.Replication.ReplID() // reuse the replid as sentinel-id
	s := sentinel.New(id, host, port, sentinel.Config{})
	for _, entry := range strings.Split(e.Cfg.SentinelMonitor, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			e.Log.Warn("sentinel monitor: bad entry", "entry", entry)
			continue
		}
		name := parts[0]
		fields := strings.Split(parts[1], ":")
		if len(fields) != 3 {
			e.Log.Warn("sentinel monitor: expected host:port:quorum", "entry", entry)
			continue
		}
		quorum, err := strconv.Atoi(fields[2])
		if err != nil {
			e.Log.Warn("sentinel monitor: bad quorum", "entry", entry, "err", err)
			continue
		}
		if err := s.Monitor(name, fields[0], fields[1], quorum); err != nil {
			e.Log.Warn("sentinel monitor failed", "name", name, "err", err)
			continue
		}
		e.Log.Info("sentinel watching", "name", name, "addr", fields[0]+":"+fields[1], "quorum", quorum)
	}
	s.Start()
	e.Sentinel = s
}

// startAutoFailover wires a callback into the cluster bus's FAIL
// detection. When a master is declared FAIL, the surviving node with
// the lowest ID among the master's replicas promotes itself. This is
// the simple deterministic election scheme described in the sentinel
// package — converges within one gossip round and avoids split-brain
// by tying election to the gossip-confirmed FAIL flag.
func (e *Engine) startAutoFailover() {
	// We poll the cluster state once per gossip tick rather than
	// hooking the bus directly — the bus already announces FAIL via
	// AnnounceFail, and the local cluster state's flag is up to date
	// before this runs.
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-e.stopCh:
				return
			case <-t.C:
				e.evaluateAutoFailover()
			}
		}
	}()
}

func (e *Engine) evaluateAutoFailover() {
	myself := e.Cluster.Myself()
	if myself == nil {
		return
	}
	for _, n := range e.Cluster.Nodes() {
		if !n.HasFlag(cluster.FlagFail) {
			continue
		}
		if n.HasFlag(cluster.FlagReplica) {
			continue
		}
		// We are a candidate iff we replicate this master + we have
		// the lowest ID among active replicas of it.
		if myself.MasterID != n.ID {
			continue
		}
		if !lowestIDReplica(e.Cluster.Nodes(), n.ID, myself.ID) {
			continue
		}
		e.Log.Warn("auto-failover: promoting self", "former_master", n.ID)
		// Promote: take ownership of the failed master's slots.
		for _, r := range n.SlotRanges() {
			for s := r[0]; s <= r[1]; s++ {
				_, _ = e.Cluster.AssignSlot(s, myself.ID)
			}
		}
		myself.Role = cluster.RoleMaster
		myself.SetFlag(cluster.FlagMaster)
		myself.ClearFlag(cluster.FlagReplica)
		myself.MasterID = ""
		e.Cluster.BumpEpoch()
		e.PromoteToMaster()
		return // don't promote twice in one tick
	}
}

// lowestIDReplica returns true if myID is the lowest-sorted among the
// alive replicas of masterID. Used by the deterministic election.
func lowestIDReplica(nodes []*cluster.Node, masterID, myID string) bool {
	best := myID
	for _, n := range nodes {
		if n.MasterID != masterID {
			continue
		}
		if n.HasFlag(cluster.FlagFail) {
			continue
		}
		if n.ID < best {
			best = n.ID
		}
	}
	return best == myID
}

// startCluster builds the local node, plugs the slot counter into the
// cluster state, opens the bus listener, and wires PUBLISH fan-out so
// pub/sub messages reach every node.
func (e *Engine) startCluster() error {
	host := e.Cfg.ClusterAnnounceHost
	if host == "" {
		host = e.Cfg.Host
	}
	port := e.Cfg.ClusterAnnouncePort
	if port == "" {
		port = e.Cfg.RESPPort
	}
	busPort := e.Cfg.ClusterBusPort
	if busPort == "" {
		// Default: dataplane port + 10000, matching Redis's convention.
		if n, err := strconv.Atoi(port); err == nil {
			busPort = strconv.Itoa(n + 10000)
		} else {
			busPort = "16379"
		}
	}
	myself := cluster.NewNode(e.Cfg.ClusterNodeID, host, port, busPort, cluster.RoleMaster)
	e.Cluster.Enable(myself)
	e.Cluster.SetKeyCounter(func(slot int) int {
		return e.KV.CountKeysInSlot(slot, cluster.KeySlot)
	})

	e.Bus = cluster.NewBus(e.Cluster, e.Log, ":"+busPort)
	e.Bus.SetPublishHandler(func(channel, payload string) {
		e.PubSub.Publish(channel, payload)
	})
	if err := e.Bus.Start(); err != nil {
		return err
	}
	e.Log.Info("cluster mode enabled",
		"node_id", myself.ID, "addr", myself.Addr(), "bus", myself.BusAddr())
	return nil
}

// KeysInSlot is a thin wrapper used by CLUSTER GETKEYSINSLOT.
func (e *Engine) KeysInSlot(slot, count int) []string {
	return e.KV.KeysInSlot(slot, count, cluster.KeySlot)
}

func (e *Engine) Stop() {
	close(e.stopCh)
	e.Metrics.Stop()
	e.Clock.Stop()
	if e.Scheduler != nil {
		e.Scheduler.Stop()
	}
	if e.Workers != nil {
		e.Workers.Stop()
	}
	if e.AOF != nil {
		_ = e.AOF.Close()
	}
	if e.RDB != nil {
		e.RDB.Stop()
	}
	if e.Master != nil {
		e.Master.Stop()
	}
	if e.ReplClient != nil {
		e.ReplClient.Stop()
	}
	if e.Bus != nil {
		e.Bus.Stop()
	}
	if e.Modules != nil {
		_ = e.Modules.ShutdownAll()
	}
}

// BumpKey increments the per-key version counter. Called by the store
// notifier on every mutation.
func (e *Engine) BumpKey(key string) {
	if key == "" {
		return
	}
	e.vmu.Lock()
	e.versions[key]++
	e.vmu.Unlock()
}

// KeyVersion reads the current version for a key (0 if never seen).
func (e *Engine) KeyVersion(key string) uint64 {
	e.vmu.RLock()
	defer e.vmu.RUnlock()
	return e.versions[key]
}

// RecordWrite hands a write-path command to the AOF + replication
// backlog. Called from dispatch after the command executes
// successfully.
//
// Replica-mode nodes skip the AOF (the master is durable on its own)
// but, when ReplChains is enabled, still feed the backlog so
// downstream replicas-of-replicas can PSYNC. We never fan out to the
// engine's own master link — only the local fan-out to attached
// replicas matters for the chain.
func (e *Engine) RecordWrite(cmd string, args []string) {
	isReplica := e.Replication != nil && e.Replication.IsReplica()
	if isReplica && !e.Cfg.ReplChains {
		return
	}
	if !isReplica && e.AOF != nil {
		_ = e.AOF.Append(cmd, args)
	}
	if e.Master != nil {
		e.Master.Propagate(cmd, args)
	}
}

// RewriteAOF dumps the current keyspace back to the AOF synchronously.
// Used by the CLI/replay paths. The RESP-level BGREWRITEAOF command
// calls BGRewriteAOF to avoid blocking the caller.
func (e *Engine) RewriteAOF() error {
	if e.AOF == nil {
		return nil
	}
	return e.AOF.Rewrite(func(w *bufio.Writer) error {
		return writeAOFSnapshot(w, e.KV.Export())
	})
}

// BGRewriteAOF kicks off an AOF rewrite on a background goroutine. It
// returns immediately. Only one rewrite runs at a time — a concurrent
// request returns ErrBgBusy so clients can retry.
func (e *Engine) BGRewriteAOF() error {
	if e.AOF == nil {
		return nil
	}
	if !e.bgRewriteBusy.CompareAndSwap(false, true) {
		return ErrBgBusy
	}
	go func() {
		defer e.bgRewriteBusy.Store(false)
		if err := e.RewriteAOF(); err != nil {
			e.Log.Warn("bgrewriteaof failed", "err", err)
		}
	}()
	return nil
}

// SaveRDB writes a snapshot synchronously and updates the LASTSAVE
// timestamp on success.
func (e *Engine) SaveRDB() error {
	if e.RDB == nil {
		return nil
	}
	if err := e.RDB.SaveNow(); err != nil {
		return err
	}
	e.lastSave.Store(time.Now().Unix())
	return nil
}

// BGSaveRDB runs an RDB snapshot on a background goroutine. Returns
// immediately. Concurrent requests return ErrBgBusy.
func (e *Engine) BGSaveRDB() error {
	if e.RDB == nil {
		return nil
	}
	if !e.bgSaveBusy.CompareAndSwap(false, true) {
		return ErrBgBusy
	}
	go func() {
		defer e.bgSaveBusy.Store(false)
		if err := e.SaveRDB(); err != nil {
			e.Log.Warn("bgsave failed", "err", err)
		}
	}()
	return nil
}

// LastSave returns the unix timestamp of the last successful RDB write,
// 0 if none has happened this process.
func (e *Engine) LastSave() int64 { return e.lastSave.Load() }

// IsBGSaveInProgress / IsBGRewriteInProgress expose the async flags to
// INFO and DEBUG handlers.
func (e *Engine) IsBGSaveInProgress() bool    { return e.bgSaveBusy.Load() }
func (e *Engine) IsBGRewriteInProgress() bool { return e.bgRewriteBusy.Load() }

// ErrBgBusy is returned when BGSAVE/BGREWRITEAOF is already running.
var ErrBgBusy = fmt.Errorf("background save already in progress")

// StartMaster lazily wires the master fan-out loop. Called at boot on
// a master node and by PromoteToMaster after a role flip.
func (e *Engine) StartMaster() {
	if e.Master != nil {
		return
	}
	e.Master = replication.NewMaster(e.Replication, e.Backlog)
	e.Master.Start()
}

// FollowMaster puts this node into replica mode following host:port.
// If a client was previously running we stop it first so the restart
// is clean.
func (e *Engine) FollowMaster(host, port string) {
	e.Replication.SetRoleReplica(host, port)
	if e.ReplClient != nil {
		e.ReplClient.Stop()
	}
	c := replication.NewClient(e.Replication, e.Log, e.replicaApplier())
	c.ListenPort = e.Cfg.RESPPort
	c.RDBRestore = e.restoreFromRDBBlob
	e.ReplClient = c
	c.Start()
}

// PromoteToMaster flips this node back into master mode (REPLICAOF NO
// ONE or FAILOVER on the replica side). Connected replicas stay — they
// can keep streaming if their replid matches the previous one.
func (e *Engine) PromoteToMaster() {
	if e.ReplClient != nil {
		e.ReplClient.Stop()
		e.ReplClient = nil
	}
	e.Replication.SetRoleMaster()
	e.StartMaster()
}

// replicaApplier returns a closure that runs an incoming replication
// command through the engine while replicaMode is true — so the
// command mutates local state but doesn't re-append to AOF or backlog.
func (e *Engine) replicaApplier() replication.Applier {
	return func(cmd string, args []string) error {
		if e.replayRunner == nil {
			return fmt.Errorf("engine: no replay runner installed")
		}
		return e.replayRunner(cmd, args)
	}
}

// SetReplayRunner is how the bootstrap plugs the HTTP-style dispatcher
// into the replica apply path. Called once at startup.
func (e *Engine) SetReplayRunner(run func(cmd string, args []string) error) {
	e.replayRunner = run
}

// RDBBlob returns a gzipped-JSON snapshot of the current keyspace,
// shaped the way our RDB format stores it. Used by the master to send
// a full-resync payload and by the replica's restore path.
func (e *Engine) RDBBlob() []byte {
	snap := e.snapshotFn()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if err := json.NewEncoder(gz).Encode(snap); err != nil {
		return nil
	}
	_ = gz.Close()
	return buf.Bytes()
}

// restoreFromRDBBlob decodes a gzipped-JSON snapshot and replaces the
// live keyspace with its contents. Called by the replica client after
// a full-resync.
func (e *Engine) restoreFromRDBBlob(blob []byte) error {
	gz, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return err
	}
	defer gz.Close()
	var snap persistence.Snapshot
	if err := json.NewDecoder(gz).Decode(&snap); err != nil {
		return err
	}
	e.KV.Restore(convertFromRDB(snap.Keys))
	e.Log.Info("replica applied full-resync snapshot", "keys", len(snap.Keys))
	return nil
}

// ConsumeReplicaHeartbeats runs a goroutine that reads REPLCONF ACK
// frames from a connected replica so WAIT sees up-to-date offsets.
// Exits when the link closes.
func (e *Engine) ConsumeReplicaHeartbeats(r *replication.ReplicaLink) {
	br := r.Reader()
	for {
		parts, err := replication.ReadArray(br)
		if err != nil {
			e.Replication.RemoveReplica(r)
			r.Close()
			return
		}
		if len(parts) < 1 {
			continue
		}
		if strings.EqualFold(parts[0], "REPLCONF") {
			for i := 1; i+1 < len(parts); i += 2 {
				if strings.EqualFold(parts[i], "ACK") {
					var off int64
					_, _ = fmt.Sscanf(parts[i+1], "%d", &off)
					r.AckOffset.Store(off)
				}
			}
		}
	}
}

// ── client-tracking dispatcher ─────────────────────────────────────
//
// The RESP layer registers per-client invalidation channels with the
// engine when CLIENT TRACKING ON fires. The keyspace notifier looks
// up the client by ID and forwards the keys.

var (
	invalMu       sync.RWMutex
	invalidateChs = map[uint64]chan<- []string{}
)

// RegisterInvalidationChannel exposes a client's push channel so the
// engine notifier can deliver invalidations.
func RegisterInvalidationChannel(clientID uint64, ch chan<- []string) {
	invalMu.Lock()
	defer invalMu.Unlock()
	invalidateChs[clientID] = ch
}

// UnregisterInvalidationChannel cleans up on disconnect.
func UnregisterInvalidationChannel(clientID uint64) {
	invalMu.Lock()
	defer invalMu.Unlock()
	delete(invalidateChs, clientID)
}

// invalidateClient is the notifier-side dispatch. Drops on overflow
// since invalidation is best-effort — the client falls back to a TTL.
func (e *Engine) invalidateClient(clientID uint64, keys []string) {
	invalMu.RLock()
	ch := invalidateChs[clientID]
	invalMu.RUnlock()
	if ch == nil {
		return
	}
	select {
	case ch <- keys:
	default:
	}
}

// ParseReplicaOf converts a "host:port" config string into (host, port)
// — returns ("", "", false) when the string is empty or malformed.
func ParseReplicaOf(s string) (string, string, bool) {
	if s == "" {
		return "", "", false
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", "", false
	}
	return host, port, true
}

// parseFsyncPolicy maps the config string onto the persistence policy.
func parseFsyncPolicy(s string) persistence.FsyncPolicy {
	switch s {
	case "always":
		return persistence.FsyncAlways
	case "no":
		return persistence.FsyncNo
	default:
		return persistence.FsyncEverySec
	}
}

// snapshotFn is the callback the RDB loop invokes. Convert our typed
// export into the persistence wire format.
func (e *Engine) snapshotFn() persistence.Snapshot {
	keys := e.KV.Export()
	out := make([]persistence.KeySnapshot, 0, len(keys))
	for _, k := range keys {
		ks := persistence.KeySnapshot{Key: k.Key, Type: k.Type, ExpireAt: k.ExpireAt, Str: k.Str, List: k.List, Hash: k.Hash, Set: k.Set}
		for _, zm := range k.ZSet {
			ks.ZSet = append(ks.ZSet, persistence.ZMember{Member: zm.Member, Score: zm.Score})
		}
		for _, se := range k.Stream {
			ks.Stream = append(ks.Stream, persistence.StreamSnapshotEntry{ID: se.ID, Fields: se.Fields})
		}
		out = append(out, ks)
	}
	return persistence.Snapshot{Version: 1, CreatedAt: time.Now(), Keys: out}
}

// convertFromRDB maps the wire format back into the store's type.
func convertFromRDB(in []persistence.KeySnapshot) []store.ExportEntry {
	out := make([]store.ExportEntry, 0, len(in))
	for _, k := range in {
		ent := store.ExportEntry{Key: k.Key, Type: k.Type, ExpireAt: k.ExpireAt, Str: k.Str, List: k.List, Hash: k.Hash, Set: k.Set}
		for _, zm := range k.ZSet {
			ent.ZSet = append(ent.ZSet, store.ExportZMember{Member: zm.Member, Score: zm.Score})
		}
		for _, se := range k.Stream {
			ent.Stream = append(ent.Stream, store.ExportStreamEntry{ID: se.ID, Fields: se.Fields})
		}
		out = append(out, ent)
	}
	return out
}

// writeAOFSnapshot serializes the live keyspace as RESP-format commands
// the engine can replay on startup. This powers BGREWRITEAOF.
func writeAOFSnapshot(w *bufio.Writer, entries []store.ExportEntry) error {
	for _, e := range entries {
		switch e.Type {
		case "string":
			if err := writeAOFCmd(w, "SET", e.Key, e.Str); err != nil {
				return err
			}
		case "list":
			args := append([]string{e.Key}, e.List...)
			if err := writeAOFCmd(w, "RPUSH", args...); err != nil {
				return err
			}
		case "hash":
			args := []string{e.Key}
			for f, v := range e.Hash {
				args = append(args, f, v)
			}
			if err := writeAOFCmd(w, "HSET", args...); err != nil {
				return err
			}
		case "set":
			args := append([]string{e.Key}, e.Set...)
			if err := writeAOFCmd(w, "SADD", args...); err != nil {
				return err
			}
		case "zset":
			args := []string{e.Key}
			for _, zm := range e.ZSet {
				args = append(args, strconv.FormatFloat(zm.Score, 'f', -1, 64), zm.Member)
			}
			if err := writeAOFCmd(w, "ZADD", args...); err != nil {
				return err
			}
		case "stream":
			for _, se := range e.Stream {
				args := append([]string{e.Key, se.ID}, se.Fields...)
				if err := writeAOFCmd(w, "XADD", args...); err != nil {
					return err
				}
			}
		}
		if e.ExpireAt > 0 {
			ms := strconv.FormatInt(e.ExpireAt, 10)
			if err := writeAOFCmd(w, "PEXPIREAT", e.Key, ms); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeAOFCmd(w *bufio.Writer, cmd string, args ...string) error {
	if _, err := w.WriteString("*"); err != nil {
		return err
	}
	if _, err := w.WriteString(strconv.Itoa(1 + len(args))); err != nil {
		return err
	}
	if _, err := w.WriteString("\r\n$"); err != nil {
		return err
	}
	if _, err := w.WriteString(strconv.Itoa(len(cmd))); err != nil {
		return err
	}
	if _, err := w.WriteString("\r\n" + cmd + "\r\n"); err != nil {
		return err
	}
	for _, a := range args {
		if _, err := w.WriteString("$" + strconv.Itoa(len(a)) + "\r\n" + a + "\r\n"); err != nil {
			return err
		}
	}
	return nil
}

// evictLoop sweeps when we cross the configured memory cap.
func (e *Engine) evictLoop() {
	if e.Scorer == nil {
		return
	}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	capBytes := int64(e.Cfg.MaxMemoryMB) * 1024 * 1024
	for {
		select {
		case <-e.stopCh:
			return
		case <-t.C:
			used := e.KV.BytesUsed()
			if used <= capBytes {
				continue
			}
			snap := e.KV.Snapshot()
			n := len(snap) / 10
			if n < 1 {
				n = 1
			}
			victims := eviction.PickVictims(snap, e.Scorer, n)
			if removed := e.KV.Evict(victims); removed > 0 {
				e.Log.Info("evicted", "count", removed, "used_bytes", used, "cap_bytes", capBytes)
			}
		}
	}
}

// Info is a snapshot of engine metrics for /api/info.
type Info struct {
	Version       string  `json:"version"`
	UptimeSeconds float64 `json:"uptime_seconds"`
	Commands      uint64  `json:"commands"`
	KV            struct {
		Keys  int   `json:"keys"`
		Bytes int64 `json:"bytes"`
	} `json:"kv"`
	Semantic semcache.Stats `json:"semantic"`
	LLM      semcache.Stats `json:"llm"`
	Memory   struct {
		Entries int `json:"entries"`
		Users   int `json:"users"`
	} `json:"memory"`
	Eviction    string `json:"eviction"`
	Persistence struct {
		AOF                 bool  `json:"aof"`
		RDB                 bool  `json:"rdb"`
		LastSave            int64 `json:"last_save"`
		BGSaveInProgress    bool  `json:"bgsave_in_progress"`
		BGRewriteInProgress bool  `json:"bgrewrite_in_progress"`
	} `json:"persistence"`
	PubSub struct {
		Patterns int `json:"patterns"`
	} `json:"pubsub"`
	Runtime struct {
		Goroutines int    `json:"goroutines"`
		GoVersion  string `json:"go_version"`
		HeapMB     uint64 `json:"heap_mb"`
	} `json:"runtime"`
}

func (e *Engine) Info() Info {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	i := Info{
		Version:       "0.3.0",
		UptimeSeconds: time.Since(e.StartedAt).Seconds(),
		Commands:      e.CmdCount.Load(),
		Eviction:      e.Cfg.Eviction,
	}
	i.KV.Keys = e.KV.Size()
	i.KV.Bytes = e.KV.BytesUsed()
	i.Semantic = e.Semantic.Stats()
	i.LLM = e.LLM.Stats()
	i.Memory.Entries = e.Memory.Size()
	i.Memory.Users = e.Memory.Users()
	i.Persistence.AOF = e.AOF != nil
	i.Persistence.RDB = e.RDB != nil
	i.Persistence.LastSave = e.LastSave()
	i.Persistence.BGSaveInProgress = e.IsBGSaveInProgress()
	i.Persistence.BGRewriteInProgress = e.IsBGRewriteInProgress()
	i.PubSub.Patterns = e.PubSub.NumPat()
	i.Runtime.Goroutines = runtime.NumGoroutine()
	i.Runtime.GoVersion = runtime.Version()
	i.Runtime.HeapMB = m.HeapAlloc / (1024 * 1024)
	return i
}

// newLatencyMonitorWithDefault constructs a LatencyMonitor with a 1ms
// default cutoff. Sub-millisecond commands (the vast majority — even
// SET/GET take ~5µs on M-series Apple silicon) skip the per-command
// mutex lock, while genuine outliers still feed LATENCY HISTORY.
// Operators can clear via `LATENCY RESET` + their own threshold; the
// CONFIG SET surface for `slowlog-log-slower-than` is wired separately
// and uses SetThreshold directly.
func newLatencyMonitorWithDefault(maxLen int) *introspect.LatencyMonitor {
	lm := introspect.NewLatencyMonitor(maxLen)
	lm.SetThreshold(time.Millisecond)
	return lm
}
