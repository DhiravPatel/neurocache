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
  CostChargeResult,
  CostUsage,
  TenantUsage,
  CostModel,
  PubSubMessage,
  PubSubSubscription,
  PubSubHandlers,
  PubSubChannels,
  LockAcquireResult,
  LockCheckResult,
  LockSnapshot,
} from "./types";

/**
 * Thrown by {@link NeuroCache.guardedSpend} when a tenant has no budget
 * headroom left, so the guarded call is never invoked.
 */
export class BudgetExceededError extends Error {
  readonly tenant: string;
  readonly attemptedUsd: number;
  readonly remaining: number;
  constructor(tenant: string, attemptedUsd: number, remaining: number) {
    super(
      `NeuroCache budget exceeded for tenant "${tenant}": ` +
        `attempted $${attemptedUsd}, only $${remaining} remaining`,
    );
    this.name = "BudgetExceededError";
    this.tenant = tenant;
    this.attemptedUsd = attemptedUsd;
    this.remaining = remaining;
  }
}

/**
 * Thrown by {@link NeuroCache.withLock} when the lock could not be acquired
 * within the configured wait window.
 */
export class LockAcquireTimeoutError extends Error {
  readonly lockName: string;
  readonly waitedMs: number;
  constructor(lockName: string, waitedMs: number) {
    super(`could not acquire lock "${lockName}" within ${waitedMs}ms`);
    this.name = "LockAcquireTimeoutError";
    this.lockName = lockName;
    this.waitedMs = waitedMs;
  }
}

function randomOwner(): string {
  return (
    "owner-" +
    Math.random().toString(36).slice(2, 10) +
    Date.now().toString(36)
  );
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

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

  // ─── cost & budgets ───
  cost = {
    /** Configure a tenant's spend allowance over a sliding window. */
    setBudget: (tenant: string, maxUsd: number, windowMs: number) =>
      this.req<{ status: string }>(
        `/api/cost/${encodeURIComponent(tenant)}/budget`,
        { method: "POST", body: JSON.stringify({ max_usd: maxUsd, window_ms: windowMs }) },
      ),
    /**
     * Record a spend against a tenant. If it would exceed the budget the call
     * is rejected (`allowed: false`) and nothing is recorded — let callers
     * short-circuit before paying for an LLM request they can't afford.
     */
    charge: (tenant: string, usd: number) =>
      this.req<CostChargeResult>(
        `/api/cost/${encodeURIComponent(tenant)}/charge`,
        { method: "POST", body: JSON.stringify({ usd }) },
      ),
    /** Current usage for a tenant. */
    usage: (tenant: string) =>
      this.req<CostUsage>(`/api/cost/${encodeURIComponent(tenant)}`),
    /** Zero a tenant's spend log without changing its budget. */
    reset: (tenant: string) =>
      this.req<{ reset: boolean }>(
        `/api/cost/${encodeURIComponent(tenant)}/reset`,
        { method: "POST" },
      ),
    /** Usage snapshot for every configured tenant. */
    list: () => this.req<{ tenants: TenantUsage[] }>("/api/cost"),
    /** Read the runtime LLM-savings cost model. */
    model: () => this.req<CostModel>("/api/cost-model"),
    /**
     * Update the runtime LLM-savings cost model. Omit a field (or pass a
     * non-positive value) to leave it unchanged. Returns the effective model.
     */
    setModel: (opts: { tokensPerHit?: number; usdPerMillion?: number }) =>
      this.req<CostModel>("/api/cost-model", {
        method: "POST",
        body: JSON.stringify({
          tokens_per_hit: opts.tokensPerHit ?? 0,
          usd_per_million_tokens: opts.usdPerMillion ?? 0,
        }),
      }),
  };

  /**
   * Reserve budget for a tenant before an expensive operation. Charges
   * `estUsd` up front; if the tenant is over budget, throws
   * {@link BudgetExceededError} WITHOUT invoking `spend`. Otherwise runs
   * `spend` and returns its result.
   *
   * The estimate is recorded as the charge — pass your best per-call cost
   * estimate (e.g. expected tokens × price). For exact accounting, charge the
   * measured cost yourself after the call instead.
   */
  async guardedSpend<T>(
    tenant: string,
    estUsd: number,
    spend: () => Promise<T>,
  ): Promise<T> {
    const { allowed, remaining } = await this.cost.charge(tenant, estUsd);
    if (!allowed) throw new BudgetExceededError(tenant, estUsd, remaining);
    return spend();
  }

  // ─── distributed locks ───
  locks = {
    /**
     * Try to acquire `name` for `owner` for `ttlMs`. Returns the fencing
     * token on success (a monotonically increasing integer you can pass to
     * downstream services so they reject stale holders). Reentrant: the
     * current owner re-acquiring refreshes the TTL and bumps the token.
     */
    acquire: (name: string, owner: string, ttlMs: number) =>
      this.req<LockAcquireResult>(
        `/api/locks/${encodeURIComponent(name)}/acquire`,
        { method: "POST", body: JSON.stringify({ owner, ttl_ms: ttlMs }) },
      ),
    /** Release `name` if `owner` holds it. */
    release: (name: string, owner: string) =>
      this.req<{ released: boolean }>(
        `/api/locks/${encodeURIComponent(name)}/release`,
        { method: "POST", body: JSON.stringify({ owner }) },
      ),
    /** Extend the lease on `name` if `owner` holds it (token unchanged). */
    extend: (name: string, owner: string, ttlMs: number) =>
      this.req<{ extended: boolean }>(
        `/api/locks/${encodeURIComponent(name)}/extend`,
        { method: "POST", body: JSON.stringify({ owner, ttl_ms: ttlMs }) },
      ),
    /** Inspect the current holder of `name`. */
    check: (name: string) =>
      this.req<LockCheckResult>(`/api/locks/${encodeURIComponent(name)}`),
    /** Every lock currently held. */
    list: () => this.req<{ locks: LockSnapshot[] }>("/api/locks"),
  };

  /**
   * Run `fn` while holding the lock `name`, handling the full lease lifecycle:
   * acquire (optionally retrying until `waitMs` elapses), keep the lease alive
   * by extending in the background while `fn` runs, and always release on the
   * way out. `fn` receives the fencing token — forward it to downstream
   * systems so they can fence stale writers.
   *
   * Throws {@link LockAcquireTimeoutError} if the lock can't be acquired in
   * time. Auto-extension is best-effort; correctness should ultimately rest on
   * the fencing token, not on the lease never lapsing.
   */
  async withLock<T>(
    name: string,
    fn: (token: number) => Promise<T>,
    opts: {
      ttlMs?: number;
      owner?: string;
      waitMs?: number;
      retryMs?: number;
      autoExtend?: boolean;
    } = {},
  ): Promise<T> {
    const ttlMs = opts.ttlMs ?? 30_000;
    const owner = opts.owner ?? randomOwner();
    const waitMs = opts.waitMs ?? 0;
    const retryMs = opts.retryMs ?? 100;
    const autoExtend = opts.autoExtend ?? true;

    const deadline = Date.now() + waitMs;
    let token = 0;
    for (;;) {
      const res = await this.locks.acquire(name, owner, ttlMs);
      if (res.acquired) {
        token = res.token;
        break;
      }
      if (Date.now() >= deadline) throw new LockAcquireTimeoutError(name, waitMs);
      await sleep(Math.min(retryMs, Math.max(0, deadline - Date.now())));
    }

    let timer: ReturnType<typeof setInterval> | undefined;
    if (autoExtend) {
      timer = setInterval(() => {
        // best-effort; swallow transient errors so the interval keeps trying
        this.locks.extend(name, owner, ttlMs).catch(() => {});
      }, Math.max(1_000, Math.floor(ttlMs / 3)));
    }

    try {
      return await fn(token);
    } finally {
      if (timer) clearInterval(timer);
      await this.locks.release(name, owner).catch(() => {});
    }
  }

  // ─── pub/sub ───

  /** Publish a message to a channel. Returns how many subscribers received it. */
  publish(channel: string, message: string) {
    return this.req<{ receivers: number }>("/api/publish", {
      method: "POST",
      body: JSON.stringify({ channel, message }),
    });
  }

  /** Introspect active channels (PUBSUB CHANNELS / NUMSUB / NUMPAT). */
  pubsubChannels(pattern = "*") {
    return this.req<PubSubChannels>(
      `/api/pubsub/channels?pattern=${encodeURIComponent(pattern)}`,
    );
  }

  /**
   * Subscribe to one or more channels and/or glob patterns and receive
   * messages over a streaming connection (Server-Sent Events). Works in the
   * browser and in Node 18+. Returns a handle — call `close()` to stop.
   *
   *   const sub = cache.subscribe(
   *     { channels: ["news"], patterns: ["room.*"] },
   *     (m) => console.log(m.channel, m.payload),
   *   );
   *   // …later
   *   sub.close();
   */
  subscribe(
    opts: { channels?: string[]; patterns?: string[] },
    onMessage: (msg: PubSubMessage) => void,
    handlers: PubSubHandlers = {},
  ): PubSubSubscription {
    const qs = new URLSearchParams();
    for (const c of opts.channels ?? []) qs.append("channel", c);
    for (const p of opts.patterns ?? []) qs.append("pattern", p);

    const controller = new AbortController();

    const run = async () => {
      const res = await this.fetchImpl(`${this.baseUrl}/api/subscribe?${qs}`, {
        headers: { ...this.headers, Accept: "text/event-stream" },
        signal: controller.signal,
      });
      if (!res.ok || !res.body) {
        const body = await res.text().catch(() => "");
        throw new Error(`NeuroCache ${res.status}: ${body || res.statusText}`);
      }
      handlers.onOpen?.();

      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buf = "";
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        let sep: number;
        // SSE events are separated by a blank line.
        while ((sep = buf.indexOf("\n\n")) !== -1) {
          const block = buf.slice(0, sep);
          buf = buf.slice(sep + 2);
          let event = "message";
          const data: string[] = [];
          for (const line of block.split("\n")) {
            if (line === "" || line.startsWith(":")) continue; // ping/comment
            if (line.startsWith("event:")) event = line.slice(6).trim();
            else if (line.startsWith("data:")) data.push(line.slice(5).trim());
          }
          if (event !== "message" || data.length === 0) continue; // skip "subscribed"
          try {
            onMessage(JSON.parse(data.join("\n")) as PubSubMessage);
          } catch {
            /* ignore malformed frame */
          }
        }
      }
    };

    run().catch((err) => {
      if (!controller.signal.aborted) handlers.onError?.(err);
    });

    return { close: () => controller.abort() };
  }

  // ─── raw command ───
  exec(command: string, ...args: string[]) {
    return this.req<{ ok: boolean; result?: unknown; error?: string }>(
      "/api/exec",
      { method: "POST", body: JSON.stringify({ command, args }) },
    );
  }
}
