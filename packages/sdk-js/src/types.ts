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
