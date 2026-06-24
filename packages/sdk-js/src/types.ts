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
