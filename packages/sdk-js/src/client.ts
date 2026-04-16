import type {
  NeuroCacheOptions,
  EngineInfo,
  MemoryEntry,
  MemoryHit,
  CacheStats,
  SemanticResult,
  LLMResult,
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

  // ─── raw command ───
  exec(command: string, ...args: string[]) {
    return this.req<{ ok: boolean; result?: unknown; error?: string }>(
      "/api/exec",
      { method: "POST", body: JSON.stringify({ command, args }) },
    );
  }
}
