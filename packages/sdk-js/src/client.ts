import type {
  NeuroCacheOptions,
  EngineInfo,
  MemoryEntry,
  MemoryHit,
  CacheStats,
  SemanticResult,
  LLMResult,
  MemoryLayer,
  RetrievalHit,
  RetrievalQueryOptions,
  RetrievalStats,
  RAGResult,
  LayeredMemoryHit,
  LayeredMemoryEntry,
  MemoryLayerStats,
  MemoryDecayResult,
  MemoryConsolidateResult,
} from "./types";

export class NeuroCache {
  private baseUrl: string;
  private fetchImpl: typeof fetch;
  private headers: Record<string, string>;

  constructor(opts: NeuroCacheOptions = {}) {
    this.baseUrl = (opts.baseUrl ?? "http://localhost:8080").replace(/\/$/, "");
    this.fetchImpl = opts.fetch ?? fetch;
    this.headers = { "Content-Type": "application/json", ...(opts.headers ?? {}) };
  }

  private async req<T>(path: string, init?: RequestInit): Promise<T> {
    const res = await this.fetchImpl(`${this.baseUrl}${path}`, {
      ...init,
      headers: { ...this.headers, ...(init?.headers ?? {}) },
    });
    if (!res.ok) {
      const body = await res.text().catch(() => "");
      throw new Error(`NeuroCache ${res.status}: ${body || res.statusText}`);
    }
    return res.json() as Promise<T>;
  }

  // ─── health / info ───
  health() { return this.req<{ status: string; uptime: number }>("/api/health"); }
  info()   { return this.req<EngineInfo>("/api/info"); }

  // ─── KV ───
  set(key: string, value: string, ttlSeconds = 0) {
    return this.req<{ ok: boolean; key: string }>("/api/kv", {
      method: "POST",
      body: JSON.stringify({ key, value, ttl: ttlSeconds }),
    });
  }
  get(key: string) {
    return this.req<{ key: string; value: string | null; hit: boolean }>(
      `/api/kv/${encodeURIComponent(key)}`,
    );
  }
  del(key: string) {
    return this.req<{ deleted: number }>(
      `/api/kv/${encodeURIComponent(key)}`,
      { method: "DELETE" },
    );
  }
  incr(key: string, by = 1) {
    return this.req<{ key: string; value: number }>(
      `/api/kv/${encodeURIComponent(key)}/incr`,
      { method: "POST", body: JSON.stringify({ by }) },
    );
  }
  expire(key: string, ttlSeconds: number) {
    return this.req<{ ok: boolean }>(
      `/api/kv/${encodeURIComponent(key)}/expire`,
      { method: "POST", body: JSON.stringify({ ttl: ttlSeconds }) },
    );
  }

  // ─── semantic ───
  semanticSet(key: string, value: string) {
    return this.req<{ ok: boolean; id: string }>("/api/semantic", {
      method: "POST",
      body: JSON.stringify({ key, value }),
    });
  }
  semanticGet(query: string, threshold?: number) {
    const qs = new URLSearchParams({ q: query });
    if (threshold !== undefined) qs.set("threshold", String(threshold));
    return this.req<SemanticResult>(`/api/semantic?${qs}`);
  }

  // ─── LLM cache ───
  cacheLLM(prompt: string, response: string) {
    return this.req<{ ok: boolean }>("/api/llm", {
      method: "POST",
      body: JSON.stringify({ prompt, response }),
    });
  }
  cacheLLMGet(prompt: string, threshold?: number) {
    const qs = new URLSearchParams({ prompt });
    if (threshold !== undefined) qs.set("threshold", String(threshold));
    return this.req<LLMResult>(`/api/llm?${qs}`);
  }
  cacheLLMStats() { return this.req<CacheStats>("/api/llm/stats"); }

  /**
   * Wrap an LLM call with a semantic cache.
   * If a cached response for a sufficiently similar prompt exists, returns it;
   * otherwise invokes `onMiss`, stores the result, and returns it.
   */
  async cacheLLMAround(
    prompt: string,
    onMiss: () => Promise<string>,
    opts: { threshold?: number } = {},
  ): Promise<{ value: string; hit: boolean; score: number }> {
    const existing = await this.cacheLLMGet(prompt, opts.threshold);
    if (existing.hit && existing.response !== null) {
      return { value: existing.response, hit: true, score: existing.score };
    }
    const value = await onMiss();
    await this.cacheLLM(prompt, value);
    return { value, hit: false, score: 0 };
  }

  // ─── memory ───
  memory = {
    add: (user: string, text: string, meta?: Record<string, string>) =>
      this.req<MemoryEntry>(`/api/memory/${encodeURIComponent(user)}`, {
        method: "POST",
        body: JSON.stringify({ text, meta }),
      }),
    list: (user: string) =>
      this.req<{ user: string; entries: MemoryEntry[] }>(
        `/api/memory/${encodeURIComponent(user)}`,
      ),
    query: (user: string, q: string, k = 5) =>
      this.req<{ user: string; query: string; hits: MemoryHit[]; context: string }>(
        `/api/memory/${encodeURIComponent(user)}?q=${encodeURIComponent(q)}&k=${k}`,
      ),
    del: (user: string, id: string) =>
      this.req<{ deleted: boolean }>(
        `/api/memory/${encodeURIComponent(user)}/${encodeURIComponent(id)}`,
        { method: "DELETE" },
      ),
  };

  // ─── hybrid retrieval (BM25 + vector + RRF) ───
  retrieve = {
    create: (
      name: string,
      opts: { dim?: number; k1?: number; b?: number; hnsw?: boolean } = {},
    ) =>
      this.req<{ status: string; name: string }>("/api/retrieve", {
        method: "POST",
        body: JSON.stringify({ name, ...opts }),
      }),
    drop: (name: string) =>
      this.req<void>(`/api/retrieve/${encodeURIComponent(name)}`, { method: "DELETE" }),
    list: () => this.req<{ indexes: string[] }>("/api/retrieve"),
    stats: (name: string) =>
      this.req<RetrievalStats>(`/api/retrieve/${encodeURIComponent(name)}/stats`),
    add: (
      name: string,
      doc: { id: string; text: string; metadata?: Record<string, string> },
    ) =>
      this.req<{ id: string }>(
        `/api/retrieve/${encodeURIComponent(name)}/docs`,
        { method: "POST", body: JSON.stringify(doc) },
      ),
    del: (name: string, id: string) =>
      this.req<void>(
        `/api/retrieve/${encodeURIComponent(name)}/docs/${encodeURIComponent(id)}`,
        { method: "DELETE" },
      ),
    query: (name: string, q: string, opts: RetrievalQueryOptions = {}) => {
      const qs = new URLSearchParams({ q });
      if (opts.k !== undefined) qs.set("k", String(opts.k));
      if (opts.alpha !== undefined) qs.set("alpha", String(opts.alpha));
      if (opts.bm25) qs.set("bm25", "1");
      if (opts.vector) qs.set("vector", "1");
      return this.req<{ hits: RetrievalHit[] }>(
        `/api/retrieve/${encodeURIComponent(name)}/query?${qs}`,
      );
    },
    rag: (
      name: string,
      q: string,
      opts: { k?: number; hops?: number; alpha?: number; predicate?: string } = {},
    ) => {
      const qs = new URLSearchParams({ q });
      if (opts.k !== undefined) qs.set("k", String(opts.k));
      if (opts.hops !== undefined) qs.set("hops", String(opts.hops));
      if (opts.alpha !== undefined) qs.set("alpha", String(opts.alpha));
      if (opts.predicate) qs.set("predicate", opts.predicate);
      return this.req<RAGResult>(
        `/api/retrieve/${encodeURIComponent(name)}/rag?${qs}`,
      );
    },
  };

  // ─── layered memory (episodic / semantic / procedural) ───
  memoryLayer = {
    add: (
      user: string,
      text: string,
      opts: {
        layer?: MemoryLayer;
        importance?: number;
        dedupThreshold?: number;
        metadata?: Record<string, string>;
      } = {},
    ) =>
      this.req<{ id: string; new: boolean; layer: MemoryLayer }>(
        `/api/memory/${encodeURIComponent(user)}/layer`,
        {
          method: "POST",
          body: JSON.stringify({
            text,
            layer: opts.layer,
            importance: opts.importance,
            dedup_threshold: opts.dedupThreshold,
            metadata: opts.metadata,
          }),
        },
      ),
    query: (
      user: string,
      q: string,
      opts: {
        layer?: MemoryLayer;
        k?: number;
        threshold?: number;
        recency?: number;
        touch?: boolean;
      } = {},
    ) => {
      const qs = new URLSearchParams({ q });
      if (opts.layer) qs.set("layer", opts.layer);
      if (opts.k !== undefined) qs.set("k", String(opts.k));
      if (opts.threshold !== undefined) qs.set("threshold", String(opts.threshold));
      if (opts.recency !== undefined) qs.set("recency", String(opts.recency));
      if (opts.touch) qs.set("touch", "1");
      return this.req<{ hits: LayeredMemoryHit[] }>(
        `/api/memory/${encodeURIComponent(user)}/query?${qs}`,
      );
    },
    stats: (user: string) =>
      this.req<MemoryLayerStats>(
        `/api/memory/${encodeURIComponent(user)}/stats`,
      ),
    decay: (
      user: string,
      opts: {
        layer?: MemoryLayer;
        halfLifeSeconds?: number;
        maxAgeSeconds?: number;
        untouchedForSeconds?: number;
        minScore?: number;
        dryRun?: boolean;
      } = {},
    ) =>
      this.req<MemoryDecayResult>(
        `/api/memory/${encodeURIComponent(user)}/decay`,
        {
          method: "POST",
          body: JSON.stringify({
            layer: opts.layer,
            half_life_seconds: opts.halfLifeSeconds,
            max_age_seconds: opts.maxAgeSeconds,
            untouched_for_seconds: opts.untouchedForSeconds,
            min_score: opts.minScore,
            dry_run: opts.dryRun,
          }),
        },
      ),
    consolidate: (
      user: string,
      opts: {
        threshold?: number;
        minSize?: number;
        drop?: boolean;
        importance?: number;
      } = {},
    ) =>
      this.req<MemoryConsolidateResult>(
        `/api/memory/${encodeURIComponent(user)}/consolidate`,
        {
          method: "POST",
          body: JSON.stringify({
            threshold: opts.threshold,
            min_size: opts.minSize,
            drop: opts.drop,
            importance: opts.importance,
          }),
        },
      ),
  };

  // ─── raw command ───
  exec(command: string, ...args: string[]) {
    return this.req<{ ok: boolean; result?: unknown; error?: string }>(
      "/api/exec",
      { method: "POST", body: JSON.stringify({ command, args }) },
    );
  }
}
