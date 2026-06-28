export interface NeuroCacheOptions {
  baseUrl?: string;
  fetch?: typeof fetch;
  headers?: Record<string, string>;
}

export interface CacheStats {
  size: number;
  hits: number;
  misses: number;
  hit_rate: number;
}

export interface EngineInfo {
  version: string;
  uptime_seconds: number;
  commands: number;
  kv: { keys: number; bytes: number };
  semantic: CacheStats;
  llm: CacheStats;
  memory: { entries: number; users: number };
  eviction: string;
  runtime: { goroutines: number; go_version: string; heap_mb: number };
}

export interface MemoryEntry {
  id: string;
  user_id: string;
  text: string;
  created_at: string;
  meta?: Record<string, string>;
}

export interface MemoryHit {
  entry: MemoryEntry;
  score: number;
}

export interface SemanticResult {
  query: string;
  hit: boolean;
  value: string | null;
  score: number;
}

export interface LLMResult {
  prompt: string;
  hit: boolean;
  response: string | null;
  score: number;
}

// ─── Hybrid retrieval (BM25 + vector + RRF) ────────────────────────

export type MemoryLayer = "episodic" | "semantic" | "procedural";

export interface RetrievalHit {
  id: string;
  text: string;
  score: number;
  bm25_rank?: number;
  vector_rank?: number;
  bm25_score?: number;
  vector_dist?: number;
  metadata?: Record<string, string>;
}

export interface RetrievalQueryOptions {
  k?: number;
  alpha?: number; // 0=BM25-only, 1=vector-only, 0.5=balanced
  bm25?: boolean;
  vector?: boolean;
}

export interface RetrievalStats {
  documents: number;
  terms: number;
  total_length: number;
  avg_length: number;
}

export interface RAGContextRow {
  subject: string;
  predicate: string;
  object: string;
  depth: number;
  source_doc: string;
}

export interface RAGResult {
  hits: RetrievalHit[];
  context: RAGContextRow[];
}

// ─── Layered memory ────────────────────────────────────────────────

export interface LayeredMemoryEntry {
  id: string;
  user_id: string;
  text: string;
  layer: MemoryLayer;
  importance: number;
  created_at: string;
  last_accessed_at?: string;
  access_count?: number;
  source_ids?: string[];
}

export interface LayeredMemoryHit {
  entry: LayeredMemoryEntry;
  score: number;
}

export interface MemoryLayerStats {
  episodic: number;
  semantic: number;
  procedural: number;
  other: number;
}

export interface MemoryDecayResult {
  scanned: number;
  dropped: number;
}

export interface MemoryConsolidateResult {
  clusters: number;
  written: number;
  dropped: number;
  new_ids: string[];
}

// ─── cost & budgets ───

/** Result of charging a tenant's budget. */
export interface CostChargeResult {
  /** Whether the charge fit within budget. When false, nothing was recorded. */
  allowed: boolean;
  /** USD remaining in the tenant's window after this call. */
  remaining: number;
}

/** A single tenant's budget state. */
export interface CostUsage {
  used: number;
  remaining: number;
  max: number;
  window_ms: number;
}

/** Per-tenant usage row returned by `cost.list()`. */
export interface TenantUsage extends CostUsage {
  tenant: string;
}

/** The runtime LLM-savings cost model. */
export interface CostModel {
  tokens_per_hit: number;
  usd_per_million_tokens: number;
}

// ─── pub/sub ───

/** A message delivered to a subscriber. `pattern` is empty for exact-channel
 *  subscriptions and echoes the matched glob for pattern subscriptions. */
export interface PubSubMessage {
  channel: string;
  pattern: string;
  payload: string;
}

/** Handle returned by `subscribe()` — call `close()` to stop the stream. */
export interface PubSubSubscription {
  close(): void;
}

/** Callbacks for a subscription's lifecycle. */
export interface PubSubHandlers {
  /** Fired once the stream is open and the server confirmed the channels. */
  onOpen?: () => void;
  /** Fired if the stream errors (network/HTTP), unless it was closed by you. */
  onError?: (err: unknown) => void;
}

/** Channels/pattern introspection (PUBSUB CHANNELS / NUMSUB / NUMPAT). */
export interface PubSubChannels {
  channels: string[];
  num_subs: Record<string, number>;
  num_patterns: number;
}

// ─── distributed locks ───

/** Result of LOCK ACQUIRE. `token` is the monotonic fencing token (0 if not acquired). */
export interface LockAcquireResult {
  acquired: boolean;
  token: number;
}

/** Current state of a lock (LOCK CHECK). */
export interface LockCheckResult {
  held: boolean;
  owner?: string;
  token?: number;
  remaining_ms?: number;
}

/** One live lock, as returned by `locks.list()`. */
export interface LockSnapshot {
  name: string;
  owner: string;
  token: number;
  remaining_ms: number;
}

// ─── rate limiting ───

export interface RateLimitResult {
  allowed: boolean;
  remaining: number;
  retry_after_ms: number;
  reset_ms: number;
}

// ─── leaderboards ───

export interface LeaderboardEntry {
  member: string;
  score: number;
  rank: number;
}

// ─── queues ───

export interface QueueJob {
  id: number;
  queue: string;
  priority: number;
  payload: string;
  idempotency_key?: string;
  attempts: number;
  last_error?: string;
  enqueued_at: string;
}

export interface QueueStats {
  name: string;
  pending: number;
  reserved: number;
  dlq: number;
  max_attempts: number;
  dlq_cap: number;
}

// ─── streams ───

export interface StreamEntry {
  id: string;
  fields: Record<string, string>;
}

export interface StreamSubscription {
  close(): void;
}

// ─── pipelining ───

/** One command's outcome in a pipeline, in submission order. */
export interface PipelineResult {
  ok: boolean;
  result?: unknown;
  error?: string;
}

// ─── conversations / sessions ───

export interface ConversationTurn {
  role: string; // "system" | "user" | "assistant" | "tool"
  content: string;
  tokens: number;
  created_at: string;
}

// ─── prompt templates ───

export interface PromptVersion {
  version: number;
  body: string;
  created_at: string;
}

export interface PromptListing {
  name: string;
  latest_version: number;
  versions: number;
}

// ─── moderation / safety ───

export interface ModerationResult {
  safe: boolean;
  score: number;
  categories?: string[];
}

// ─── quota (composite admission control) ───

/** Per-gate inputs for a quota admit/simulate. Supply only the gates the
 *  policy requires. */
export interface QuotaDims {
  cost?: { scope: string; usd: number };
  carbon?: { tenant: string; tokens: number; model?: string; feature?: string; region?: string };
  risk?: { session: string; score: number };
  rate?: { key: string; window_ms: number; max: number; cost?: number };
  market?: { market: string; max_price: number };
}

// ─── grounding / verification (hallucination + citation check) ───

/** Per-sentence support: how strongly one claim in the answer is backed by
 *  the supplied context. `support` is the max cosine similarity to any
 *  context chunk in [0,1]; `best_chunk` is its 0-indexed source (-1 if no
 *  context). `supported` is `support >= min_support`. */
export interface SentenceSupport {
  sentence: string;
  support: number;
  best_chunk: number;
  supported: boolean;
}

/** Result of grounding an LLM answer against its retrieved context. */
export interface VerifyResult {
  /** Worst per-claim support — the weakest sentence drives the doc score. */
  doc_score: number;
  /** Mean support across every claim. */
  mean_score: number;
  /** The threshold a claim must clear to count as supported. */
  min_support: number;
  /** True only when every sentence clears `min_support`. */
  grounded: boolean;
  sentences: SentenceSupport[];
  /** The sentences that fell below threshold — likely hallucinations. */
  unsupported: string[];
}

/** Risk-budget debit returned when a session is supplied to `require`. */
export interface RiskDebit {
  balance: number;
  budget: number;
  enforce: boolean;
  debited: number;
}

export interface GroundRequireResult {
  result: VerifyResult;
  risk?: RiskDebit;
  risk_session?: string;
}

export interface GroundStats {
  dim: number;
  scorer: string;
  total_verify: number;
  total_require: number;
  total_pass: number;
  total_fail: number;
  extern_scores: number;
}
