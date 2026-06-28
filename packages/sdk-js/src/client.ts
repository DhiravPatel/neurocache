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
  RateLimitResult,
  LeaderboardEntry,
  QueueJob,
  QueueStats,
  StreamEntry,
  StreamSubscription,
  PipelineResult,
  ConversationTurn,
  PromptVersion,
  PromptListing,
  ModerationResult,
  QuotaDims,
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

/**
 * Thrown by {@link NeuroCache.limit} when a key is over its rate limit. Carries
 * the limiter's retry hints so callers can set a Retry-After header or back off.
 */
export class RateLimitedError extends Error {
  readonly key: string;
  readonly retryAfterMs: number;
  readonly resetMs: number;
  constructor(key: string, retryAfterMs: number, resetMs: number) {
    super(`rate limit exceeded for "${key}" — retry in ${retryAfterMs}ms`);
    this.name = "RateLimitedError";
    this.key = key;
    this.retryAfterMs = retryAfterMs;
    this.resetMs = resetMs;
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

  // ─── rate limiting ───

  /**
   * Check one event against a GCRA rate limit for `key`. Does NOT throw when
   * over the limit — inspect `allowed` and the retry hints. `peek: true`
   * evaluates without consuming a slot.
   */
  async rateLimit(
    key: string,
    opts: { windowMs: number; max: number; cost?: number; peek?: boolean },
  ): Promise<RateLimitResult> {
    const res = await this.fetchImpl(`${this.baseUrl}/api/ratelimit`, {
      method: "POST",
      headers: this.headers,
      body: JSON.stringify({
        key,
        window_ms: opts.windowMs,
        max: opts.max,
        cost: opts.cost ?? 1,
        peek: opts.peek ?? false,
      }),
    });
    // 429 is a normal "denied" outcome here, not a transport error.
    if (res.status !== 200 && res.status !== 429) {
      const body = await res.text().catch(() => "");
      throw new Error(`NeuroCache ${res.status}: ${body || res.statusText}`);
    }
    return res.json() as Promise<RateLimitResult>;
  }

  /**
   * Run `fn` only if `key` is under its rate limit; otherwise throw
   * {@link RateLimitedError} (carrying retry hints) without invoking `fn`.
   */
  async limit<T>(
    key: string,
    opts: { windowMs: number; max: number; cost?: number },
    fn: () => Promise<T>,
  ): Promise<T> {
    const r = await this.rateLimit(key, opts);
    if (!r.allowed) throw new RateLimitedError(key, r.retry_after_ms, r.reset_ms);
    return fn();
  }

  /** Clear all usage recorded for a rate-limit key. */
  rateLimitReset(key: string) {
    return this.req<{ status: string }>("/api/ratelimit/reset", {
      method: "POST",
      body: JSON.stringify({ key }),
    });
  }

  // ─── leaderboards (sorted set, highest-first) ───
  leaderboard = {
    /** Set a member's score. Returns the member's new score and rank. */
    set: (name: string, member: string, score: number) =>
      this.req<LeaderboardEntry>(`/api/leaderboard/${encodeURIComponent(name)}`, {
        method: "POST",
        body: JSON.stringify({ member, score }),
      }),
    /** Increment a member's score by `by` (default 1). */
    incr: (name: string, member: string, by = 1) =>
      this.req<LeaderboardEntry>(`/api/leaderboard/${encodeURIComponent(name)}/incr`, {
        method: "POST",
        body: JSON.stringify({ member, by }),
      }),
    /** Top `n` members, highest score first. */
    top: (name: string, n = 10) =>
      this.req<{ count: number; entries: LeaderboardEntry[] }>(
        `/api/leaderboard/${encodeURIComponent(name)}/top?n=${n}`,
      ),
    /** A member's score and rank, or `{ found: false }`. */
    rank: (name: string, member: string) =>
      this.req<{ found: boolean; member?: string; score?: number; rank?: number }>(
        `/api/leaderboard/${encodeURIComponent(name)}/rank/${encodeURIComponent(member)}`,
      ),
    /** A member plus `n` neighbours on each side — the "your rank" view. */
    around: (name: string, member: string, n = 3) =>
      this.req<{ found: boolean; entries?: LeaderboardEntry[] }>(
        `/api/leaderboard/${encodeURIComponent(name)}/around/${encodeURIComponent(member)}?n=${n}`,
      ),
    /** Remove a member from the board. */
    remove: (name: string, member: string) =>
      this.req<{ removed: boolean }>(
        `/api/leaderboard/${encodeURIComponent(name)}/${encodeURIComponent(member)}`,
        { method: "DELETE" },
      ),
  };

  // ─── queues (durable jobs: priority, retries, DLQ, visibility timeout) ───
  queue = {
    /** Enqueue a job. `idempotencyKey` dedupes in-flight duplicates. */
    enqueue: (
      name: string,
      payload: string,
      opts: { priority?: number; idempotencyKey?: string } = {},
    ) =>
      this.req<{ id: number }>(`/api/worker/${encodeURIComponent(name)}`, {
        method: "POST",
        body: JSON.stringify({
          payload,
          priority: opts.priority,
          idempotency_key: opts.idempotencyKey,
        }),
      }),
    /** Reserve the highest-priority job for `visibilityMs` (default server-side 30s). */
    dequeue: (name: string, visibilityMs?: number) => {
      const qs = visibilityMs ? `?visibility_ms=${visibilityMs}` : "";
      return this.req<{ job: QueueJob | null }>(
        `/api/worker/${encodeURIComponent(name)}/next${qs}`,
      );
    },
    /** Mark a reserved job done. */
    ack: (name: string, id: number) =>
      this.req<{ acked: boolean }>(
        `/api/worker/${encodeURIComponent(name)}/ack/${id}`,
        { method: "POST" },
      ),
    /** Return a job to the queue (or dead-letter it once attempts are exhausted). */
    nack: (name: string, id: number, opts: { error?: string; delayMs?: number } = {}) =>
      this.req<{ requeued: boolean; dlq: boolean }>(
        `/api/worker/${encodeURIComponent(name)}/nack/${id}`,
        { method: "POST", body: JSON.stringify({ error: opts.error, delay_ms: opts.delayMs }) },
      ),
    stats: (name: string) =>
      this.req<QueueStats>(`/api/worker/${encodeURIComponent(name)}/stats`),
    dlq: (name: string) =>
      this.req<{ jobs: QueueJob[] }>(`/api/worker/${encodeURIComponent(name)}/dlq`),
    requeue: (name: string, id: number) =>
      this.req<{ status: string }>(
        `/api/worker/${encodeURIComponent(name)}/requeue/${id}`,
        { method: "POST" },
      ),
    configure: (name: string, opts: { maxAttempts?: number; dlqCap?: number }) =>
      this.req<{ status: string }>(`/api/worker/${encodeURIComponent(name)}/config`, {
        method: "POST",
        body: JSON.stringify({ max_attempts: opts.maxAttempts, dlq_cap: opts.dlqCap }),
      }),
    list: () => this.req<{ queues: string[] }>("/api/worker"),
  };

  /**
   * Run a polling consumer loop: reserve a job, run `handler`, then ACK on
   * success or NACK (with the error) on throw. Returns a handle; call `stop()`
   * to end the loop after the in-flight job settles.
   */
  work(
    queue: string,
    handler: (job: QueueJob) => Promise<void>,
    opts: { visibilityMs?: number; pollMs?: number; onError?: (e: unknown) => void } = {},
  ): { stop: () => void } {
    let stopped = false;
    const pollMs = opts.pollMs ?? 1000;
    const loop = async () => {
      while (!stopped) {
        let job: QueueJob | null = null;
        try {
          job = (await this.queue.dequeue(queue, opts.visibilityMs)).job;
        } catch (e) {
          opts.onError?.(e);
        }
        if (!job) {
          await sleep(pollMs);
          continue;
        }
        try {
          await handler(job);
          await this.queue.ack(queue, job.id);
        } catch (e) {
          opts.onError?.(e);
          await this.queue
            .nack(queue, job.id, { error: (e as Error)?.message ?? String(e) })
            .catch(() => {});
        }
      }
    };
    void loop();
    return { stop: () => { stopped = true; } };
  }

  // ─── streams (append-only log) ───
  streams = {
    /** Append an entry; returns its assigned id. `id` defaults to "*" (auto). */
    add: (key: string, fields: Record<string, string>, opts: { id?: string; maxlen?: number } = {}) =>
      this.req<{ id: string }>(`/api/streams/${encodeURIComponent(key)}`, {
        method: "POST",
        body: JSON.stringify({ fields, id: opts.id, maxlen: opts.maxlen }),
      }),
    /** Read a range of entries (defaults to the whole stream, oldest-first). */
    range: (key: string, opts: { start?: string; end?: string; count?: number; reverse?: boolean } = {}) => {
      const qs = new URLSearchParams();
      if (opts.start) qs.set("start", opts.start);
      if (opts.end) qs.set("end", opts.end);
      if (opts.count !== undefined) qs.set("count", String(opts.count));
      if (opts.reverse) qs.set("reverse", "1");
      return this.req<{ length: number; entries: StreamEntry[] }>(
        `/api/streams/${encodeURIComponent(key)}?${qs}`,
      );
    },
    /** Number of entries in the stream. */
    len: (key: string) =>
      this.req<{ length: number }>(`/api/streams/${encodeURIComponent(key)}/len`),
    /**
     * Follow a stream live over Server-Sent Events. `last` controls the start:
     * "$" (default) = only new entries; "0" = replay then follow. Returns a
     * handle — call `close()` to stop.
     */
    tail: (
      key: string,
      onEntry: (entry: StreamEntry) => void,
      opts: { last?: string; onOpen?: () => void; onError?: (e: unknown) => void } = {},
    ): StreamSubscription => {
      const qs = new URLSearchParams();
      if (opts.last) qs.set("last", opts.last);
      return this.sseStream(
        `${this.baseUrl}/api/streams/${encodeURIComponent(key)}/tail?${qs}`,
        (d) => onEntry(d as StreamEntry),
        { onOpen: opts.onOpen, onError: opts.onError },
      );
    },
  };

  /**
   * Shared Server-Sent Events reader (used by streams.tail). Parses default
   * "message" events as JSON and invokes `onData`; ignores comments/pings and
   * named events. Returns a handle whose `close()` aborts the stream.
   */
  private sseStream(
    url: string,
    onData: (data: unknown) => void,
    handlers: { onOpen?: () => void; onError?: (e: unknown) => void } = {},
  ): { close: () => void } {
    const controller = new AbortController();
    const run = async () => {
      const res = await this.fetchImpl(url, {
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
        while ((sep = buf.indexOf("\n\n")) !== -1) {
          const block = buf.slice(0, sep);
          buf = buf.slice(sep + 2);
          let event = "message";
          const data: string[] = [];
          for (const line of block.split("\n")) {
            if (line === "" || line.startsWith(":")) continue;
            if (line.startsWith("event:")) event = line.slice(6).trim();
            else if (line.startsWith("data:")) data.push(line.slice(5).trim());
          }
          if (event !== "message" || data.length === 0) continue;
          try { onData(JSON.parse(data.join("\n"))); } catch { /* ignore */ }
        }
      }
    };
    run().catch((err) => {
      if (!controller.signal.aborted) handlers.onError?.(err);
    });
    return { close: () => controller.abort() };
  }

  // ─── conversations / sessions (multi-turn context) ───
  conversations = {
    /** Append a turn; returns the new turn count. */
    append: (key: string, role: string, content: string) =>
      this.req<{ turns: number }>(`/api/conv/${encodeURIComponent(key)}`, {
        method: "POST",
        body: JSON.stringify({ role, content }),
      }),
    /** The recent window of turns (optionally capped to `maxTokens`). */
    window: (key: string, maxTokens?: number) => {
      const qs = maxTokens ? `?max_tokens=${maxTokens}` : "";
      return this.req<{ turns: ConversationTurn[] }>(
        `/api/conv/${encodeURIComponent(key)}${qs}`,
      );
    },
    /** Replace old turns with a summary, keeping the most recent `keepTokens`. */
    summarize: (key: string, summary: string, keepTokens?: number) =>
      this.req<{ dropped_turns: number; tokens_remaining: number }>(
        `/api/conv/${encodeURIComponent(key)}/summarize`,
        { method: "POST", body: JSON.stringify({ summary, keep_tokens: keepTokens }) },
      ),
    /** Clear a conversation. */
    reset: (key: string) =>
      this.req<{ reset: boolean }>(`/api/conv/${encodeURIComponent(key)}`, {
        method: "DELETE",
      }),
    /** Every active conversation key. */
    list: () => this.req<{ conversations: string[]; count: number }>("/api/conv"),
  };

  // ─── versioned prompt templates ───
  prompts = {
    /** Store a template body; `version` 0/omitted auto-increments. Returns the version. */
    set: (name: string, body: string, version?: number) =>
      this.req<{ version: number }>(`/api/prompts/${encodeURIComponent(name)}`, {
        method: "POST",
        body: JSON.stringify({ body, version }),
      }),
    /** Fetch a version (latest if omitted). */
    get: (name: string, version?: number) => {
      const qs = version ? `?version=${version}` : "";
      return this.req<PromptVersion>(`/api/prompts/${encodeURIComponent(name)}${qs}`);
    },
    /** Render a template with `{var}` substitutions. */
    render: (name: string, vars: Record<string, string>, version?: number) =>
      this.req<{ rendered: string }>(
        `/api/prompts/${encodeURIComponent(name)}/render`,
        { method: "POST", body: JSON.stringify({ vars, version }) },
      ),
    /** Every template with its latest version + count. */
    list: () => this.req<PromptListing[]>("/api/prompts"),
    /** All versions of a template. */
    versions: (name: string) =>
      this.req<PromptVersion[]>(`/api/prompts/${encodeURIComponent(name)}/versions`),
    /** Delete a version (or the whole template if version omitted). */
    delete: (name: string, version?: number) => {
      const qs = version ? `?version=${version}` : "";
      return this.req<{ removed: number }>(
        `/api/prompts/${encodeURIComponent(name)}${qs}`,
        { method: "DELETE" },
      );
    },
  };

  // ─── embedding cache (skip re-embedding identical text) ───
  embeddings = {
    set: (text: string, vector: number[], ttlSec?: number) =>
      this.req<{ status: string }>("/api/emb-cache", {
        method: "POST",
        body: JSON.stringify({ text, vector, ttl_sec: ttlSec }),
      }),
    get: (text: string) =>
      this.req<{ hit: boolean; vector?: number[] }>(
        `/api/emb-cache?text=${encodeURIComponent(text)}`,
      ),
    stats: () => this.req<Record<string, unknown>>("/api/emb-cache/stats"),
    purge: () => this.req<{ dropped: number }>("/api/emb-cache/purge", { method: "POST" }),
  };

  // ─── agent tool-call cache ───
  agentCache = {
    /** Cache a tool call's result keyed by (tool, args hash). */
    store: (tool: string, argsHash: string, result: string) =>
      this.req<{ status: string }>("/api/agent", {
        method: "POST",
        body: JSON.stringify({ tool, args_hash: argsHash, result }),
      }),
    /** Look up a cached tool result. */
    call: (tool: string, argsHash: string) =>
      this.req<{ hit: boolean; result?: string }>(
        `/api/agent?tool=${encodeURIComponent(tool)}&args_hash=${encodeURIComponent(argsHash)}`,
      ),
    /** Set a tool's cache determinism: "always" | "day" | "never". */
    profile: (tool: string, profile: "always" | "day" | "never") =>
      this.req<{ status: string }>("/api/agent/profile", {
        method: "POST",
        body: JSON.stringify({ tool, profile }),
      }),
    forget: (tool: string, argsHash: string) =>
      this.req<{ removed: boolean }>(
        `/api/agent?tool=${encodeURIComponent(tool)}&args_hash=${encodeURIComponent(argsHash)}`,
        { method: "DELETE" },
      ),
    stats: () => this.req<Record<string, unknown>>("/api/agent/stats"),
    purge: () => this.req<{ dropped: number }>("/api/agent/purge", { method: "POST" }),
  };

  // ─── moderation / safety verdict cache ───
  moderation = {
    /** Cache a moderation verdict for `text`. */
    set: (text: string, result: ModerationResult, ttlSec?: number) =>
      this.req<{ status: string }>("/api/safe", {
        method: "POST",
        body: JSON.stringify({ text, ...result, ttl_sec: ttlSec }),
      }),
    /** Look up a cached verdict. */
    check: (text: string) =>
      this.req<{ hit: boolean; result?: ModerationResult }>(
        `/api/safe?text=${encodeURIComponent(text)}`,
      ),
    /** Heuristic prompt-injection score (no cache needed). */
    injectionScore: (text: string) =>
      this.req<{ score: number; matched: string[] }>(
        `/api/safe/inject?text=${encodeURIComponent(text)}`,
      ),
    forget: (text: string) =>
      this.req<{ removed: boolean }>(`/api/safe?text=${encodeURIComponent(text)}`, {
        method: "DELETE",
      }),
    purge: () => this.req<{ dropped: number }>("/api/safe/purge", { method: "POST" }),
    stats: () => this.req<Record<string, unknown>>("/api/safe/stats"),
  };

  // ─── A/B experiments ───
  experiments = {
    /** Define an experiment with variants (and optional weights). */
    define: (name: string, variants: string[], weights?: number[]) =>
      this.req<{ status: string }>("/api/ab", {
        method: "POST",
        body: JSON.stringify({ name, variants, weights }),
      }),
    /** Deterministically assign a user to a variant. */
    assign: (name: string, user: string) =>
      this.req<{ variant?: string; hit?: boolean }>(
        `/api/ab/${encodeURIComponent(name)}/assign?user=${encodeURIComponent(user)}`,
      ),
    /** Record an exposure (denominator for conversion). */
    expose: (name: string, variant: string) =>
      this.req<{ status: string }>(`/api/ab/${encodeURIComponent(name)}/expose`, {
        method: "POST",
        body: JSON.stringify({ variant }),
      }),
    /** Record an outcome value for a variant. */
    record: (name: string, variant: string, value: number) =>
      this.req<{ status: string }>(`/api/ab/${encodeURIComponent(name)}/record`, {
        method: "POST",
        body: JSON.stringify({ variant, value }),
      }),
    stats: (name: string) =>
      this.req<Record<string, unknown>>(`/api/ab/${encodeURIComponent(name)}`),
    list: () => this.req<{ experiments: unknown }>("/api/ab"),
    reset: (name: string) =>
      this.req<{ reset: boolean }>(`/api/ab/${encodeURIComponent(name)}/reset`, {
        method: "POST",
      }),
    delete: (name: string) =>
      this.req<{ removed: boolean }>(`/api/ab/${encodeURIComponent(name)}`, {
        method: "DELETE",
      }),
  };

  // ─── knowledge graph (subject–predicate–object triples) ───
  graph = {
    link: (subject: string, predicate: string, object: string) =>
      this.req<{ created: boolean }>("/api/graph/link", {
        method: "POST",
        body: JSON.stringify({ subject, predicate, object }),
      }),
    unlink: (subject: string, predicate: string, object: string) =>
      this.req<{ removed: boolean }>("/api/graph/unlink", {
        method: "POST",
        body: JSON.stringify({ subject, predicate, object }),
      }),
    neighbors: (subject: string, predicate?: string) => {
      const qs = new URLSearchParams({ subject });
      if (predicate) qs.set("predicate", predicate);
      return this.req<{ neighbors: string[] }>(`/api/graph/neighbors?${qs}`);
    },
    in: (object: string, predicate?: string) => {
      const qs = new URLSearchParams({ object });
      if (predicate) qs.set("predicate", predicate);
      return this.req<{ subjects: string[] }>(`/api/graph/in?${qs}`);
    },
    /**
     * Shortest path between two nodes. `path` is the chain of edges
     * (each `{predicate, object}` = the relationship traversed and the node
     * arrived at); the `from` node is implicit.
     */
    path: (from: string, to: string, opts: { maxDepth?: number; predicate?: string } = {}) => {
      const qs = new URLSearchParams({ from, to });
      if (opts.maxDepth) qs.set("max_depth", String(opts.maxDepth));
      if (opts.predicate) qs.set("predicate", opts.predicate);
      return this.req<{ found: boolean; path?: { predicate: string; object: string }[] }>(
        `/api/graph/path?${qs}`,
      );
    },
    subjects: () => this.req<{ subjects: string[] }>("/api/graph/subjects"),
    stats: () => this.req<Record<string, unknown>>("/api/graph/stats"),
  };

  // ─── feature flags ───
  flags = {
    set: (
      name: string,
      opts: { on: boolean; percentage?: number; allow?: string[]; deny?: string[] },
    ) =>
      this.req<{ status: string }>(`/api/flag/${encodeURIComponent(name)}`, {
        method: "POST",
        body: JSON.stringify({
          on: opts.on,
          percentage: opts.percentage ?? 0,
          allow: opts.allow,
          deny: opts.deny,
        }),
      }),
    /** Whether the flag is enabled for a specific user (sticky % rollout + allow/deny). */
    is: (name: string, user: string) =>
      this.req<{ enabled: boolean }>(
        `/api/flag/${encodeURIComponent(name)}/is?user=${encodeURIComponent(user)}`,
      ),
    allow: (name: string, user: string) =>
      this.req<{ added: boolean }>(`/api/flag/${encodeURIComponent(name)}/allow`, {
        method: "POST",
        body: JSON.stringify({ user }),
      }),
    deny: (name: string, user: string) =>
      this.req<{ added: boolean }>(`/api/flag/${encodeURIComponent(name)}/deny`, {
        method: "POST",
        body: JSON.stringify({ user }),
      }),
    get: (name: string) =>
      this.req<Record<string, unknown>>(`/api/flag/${encodeURIComponent(name)}`),
    list: () => this.req<{ flags: string[] }>("/api/flag"),
    delete: (name: string) =>
      this.req<{ removed: boolean }>(`/api/flag/${encodeURIComponent(name)}`, {
        method: "DELETE",
      }),
  };

  // ─── per-user personas ───
  personas = {
    set: (user: string, persona: string) =>
      this.req<{ status: string }>(`/api/persona/${encodeURIComponent(user)}`, {
        method: "POST",
        body: JSON.stringify({ persona }),
      }),
    get: (user: string) =>
      this.req<{ persona: string }>(`/api/persona/${encodeURIComponent(user)}`),
    list: (user: string) =>
      this.req<{ personas: string[] }>(`/api/persona/${encodeURIComponent(user)}/list`),
    forget: (user: string) =>
      this.req<{ removed: boolean }>(`/api/persona/${encodeURIComponent(user)}`, {
        method: "DELETE",
      }),
  };

  // ─── scheduler (run a command at/after a time) ───
  scheduler = {
    /** Schedule `cmd` to run at an absolute time (ms since epoch). */
    at: (unixMs: number, cmd: string, args: string[] = []) =>
      this.req<{ id: number }>("/api/schedule/at", {
        method: "POST",
        body: JSON.stringify({ unix_ms: unixMs, cmd, args }),
      }),
    /** Schedule `cmd` to run after `delayMs`. */
    in: (delayMs: number, cmd: string, args: string[] = []) =>
      this.req<{ id: number }>("/api/schedule/in", {
        method: "POST",
        body: JSON.stringify({ delay_ms: delayMs, cmd, args }),
      }),
    cancel: (id: number) =>
      this.req<{ cancelled: boolean }>(`/api/schedule/${id}`, { method: "DELETE" }),
    list: () => this.req<{ tasks: unknown[] }>("/api/schedule"),
    stats: () => this.req<Record<string, unknown>>("/api/schedule/stats"),
  };

  // ─── event log + projections (event sourcing) ───
  events = {
    /** Append a JSON event to a stream; returns its sequence number. */
    append: (stream: string, payload: unknown) =>
      this.req<{ seq: number }>(`/api/event/${encodeURIComponent(stream)}`, {
        method: "POST",
        body: JSON.stringify(payload),
      }),
    /** Register a continuously-maintained projection (reducer: count/sum/last/…). */
    project: (
      stream: string,
      name: string,
      reducer: string,
      opts: { field?: string; groupBy?: string } = {},
    ) =>
      this.req<{ status: string }>(`/api/event/${encodeURIComponent(stream)}/project`, {
        method: "POST",
        body: JSON.stringify({ name, reducer, field: opts.field, group_by: opts.groupBy }),
      }),
    /** Read a projection's current value. */
    projection: (stream: string, name: string) =>
      this.req<{ projection: unknown }>(
        `/api/event/${encodeURIComponent(stream)}/projection/${encodeURIComponent(name)}`,
      ),
    /** Raw events in a sequence range. */
    range: (stream: string, start = 0, end = 0) =>
      this.req<{ events: unknown[] }>(
        `/api/event/${encodeURIComponent(stream)}/range?start=${start}&end=${end}`,
      ),
    len: (stream: string) =>
      this.req<{ len: number }>(`/api/event/${encodeURIComponent(stream)}/len`),
  };

  // ─── lineage / provenance (which sources produced an output) ───
  lineage = {
    /** Record that `outputId` was derived from `sourceId`. */
    record: (
      outputId: string,
      sourceId: string,
      opts: { snippet?: string; confidence?: number } = {},
    ) =>
      this.req<{ status: string }>("/api/lineage", {
        method: "POST",
        body: JSON.stringify({
          output_id: outputId,
          source_id: sourceId,
          snippet: opts.snippet,
          confidence: opts.confidence,
        }),
      }),
    /** All citations (sources) for an output. */
    citations: (outputId: string) =>
      this.req<{ citations: unknown[] }>(`/api/lineage/${encodeURIComponent(outputId)}`),
    sources: (outputId: string) =>
      this.req<{ sources: string[] }>(`/api/lineage/${encodeURIComponent(outputId)}/sources`),
    /** Reverse lookup: which outputs consumed a source. */
    consumers: (sourceId: string) =>
      this.req<{ consumers: string[] }>(
        `/api/lineage/sources/${encodeURIComponent(sourceId)}/consumers`,
      ),
    forget: (outputId: string) =>
      this.req<{ removed: number }>(`/api/lineage/${encodeURIComponent(outputId)}`, {
        method: "DELETE",
      }),
    stats: () => this.req<Record<string, unknown>>("/api/lineage/stats"),
  };

  // ─── inference proxy (provider-backed generate, cached) ───
  inference = {
    /**
     * Generate via the configured provider, cached by (prompt, model, temp).
     * Returns the response, whether it was a cache hit, and the estimated cost.
     */
    generate: (
      prompt: string,
      opts: {
        model?: string;
        temperature?: number;
        maxTokens?: number;
        tenant?: string;
        ttlSec?: number;
      } = {},
    ) =>
      this.req<{ response: string; hit: boolean; cost: number }>("/api/infer", {
        method: "POST",
        body: JSON.stringify({
          prompt,
          model: opts.model,
          temperature: opts.temperature,
          max_tokens: opts.maxTokens,
          tenant: opts.tenant,
          ttl_sec: opts.ttlSec,
        }),
      }),
    forget: (prompt: string, opts: { model?: string; temperature?: number } = {}) =>
      this.req<{ removed: boolean }>("/api/infer", {
        method: "DELETE",
        body: JSON.stringify({ prompt, model: opts.model, temperature: opts.temperature }),
      }),
    /** Set the default provider (e.g. "openai", "anthropic", "echo"). */
    setDefault: (provider: string) =>
      this.req<{ status: string }>("/api/infer/default", {
        method: "POST",
        body: JSON.stringify({ provider }),
      }),
    purge: () => this.req<{ dropped: number }>("/api/infer/purge", { method: "POST" }),
    stats: () => this.req<Record<string, unknown>>("/api/infer/stats"),
  };

  // ─── MCP (Model Context Protocol server) ───
  mcp = {
    tools: () => this.req<{ tools: unknown[] }>("/api/mcp/tools"),
    resources: () => this.req<{ resources: unknown[] }>("/api/mcp/resources"),
    /** Invoke an MCP tool; returns the JSON-RPC result frame. */
    call: (name: string, args: Record<string, unknown> = {}) =>
      this.req<unknown>("/api/mcp/call", {
        method: "POST",
        body: JSON.stringify({ name, arguments: args }),
      }),
    /** Read an MCP resource by URI. */
    read: (uri: string) => this.req<unknown>(`/api/mcp/read?uri=${encodeURIComponent(uri)}`),
    /** Send a raw JSON-RPC 2.0 frame. */
    rpc: (frame: unknown) =>
      this.req<unknown>("/api/mcp/rpc", { method: "POST", body: JSON.stringify(frame) }),
  };

  // ─── quota (composite admission control: cost/carbon/risk/rate/market) ───
  quota = {
    /** Define a policy that ANDs together a set of gates. */
    define: (name: string, gates: string[], mode?: string) =>
      this.req<Record<string, unknown>>(`/api/quota/${encodeURIComponent(name)}/policy`, {
        method: "POST",
        body: JSON.stringify({ gates, mode }),
      }),
    get: (name: string) =>
      this.req<Record<string, unknown>>(`/api/quota/${encodeURIComponent(name)}`),
    list: () => this.req<{ policies: unknown }>("/api/quota"),
    stats: () => this.req<Record<string, unknown>>("/api/quota/stats"),
    delete: (name: string) =>
      this.req<{ deleted: boolean }>(`/api/quota/${encodeURIComponent(name)}`, { method: "DELETE" }),
    /** Evaluate AND commit the admission decision across the policy's gates. */
    admit: (name: string, dims: QuotaDims) =>
      this.req<Record<string, unknown>>(`/api/quota/${encodeURIComponent(name)}/admit`, {
        method: "POST",
        body: JSON.stringify(dims),
      }),
    /** Evaluate WITHOUT consuming any gate (dry run). */
    simulate: (name: string, dims: QuotaDims) =>
      this.req<Record<string, unknown>>(`/api/quota/${encodeURIComponent(name)}/simulate`, {
        method: "POST",
        body: JSON.stringify(dims),
      }),
  };

  // ─── churn (tag-based cache invalidation) ───
  churn = {
    /** Tag a cache key with one or more tags. Returns how many tags were added. */
    tag: (key: string, ...tags: string[]) =>
      this.req<{ added: number }>(`/api/churn/${encodeURIComponent(key)}`, {
        method: "POST",
        body: JSON.stringify({ tags }),
      }),
    /** Remove tags from a key. */
    untag: (key: string, ...tags: string[]) => {
      const qs = new URLSearchParams();
      for (const t of tags) qs.append("tag", t);
      return this.req<{ removed: number }>(
        `/api/churn/${encodeURIComponent(key)}?${qs}`,
        { method: "DELETE" },
      );
    },
    /**
     * Invalidate (delete) every key carrying ANY of the given tags, in one
     * call. Returns the keys that were dropped — fan-out cache busting without
     * tracking key sets in your app.
     */
    invalidate: (...tags: string[]) =>
      this.req<{ dropped: string[] }>("/api/churn/invalidate", {
        method: "POST",
        body: JSON.stringify({ tags }),
      }),
    /** Keys currently tagged with `tag`. */
    keysFor: (tag: string) =>
      this.req<{ keys: string[] }>(`/api/churn/keys?tag=${encodeURIComponent(tag)}`),
    /** Tags on a specific key. */
    tagsOf: (key: string) =>
      this.req<{ tags: string[] }>(`/api/churn/${encodeURIComponent(key)}`),
    /** Every tag in use. */
    tags: () => this.req<{ tags: string[] }>("/api/churn/tags"),
    stats: () => this.req<Record<string, unknown>>("/api/churn/stats"),
  };

  // ─── raw command ───
  exec(command: string, ...args: string[]) {
    return this.req<{ ok: boolean; result?: unknown; error?: string }>(
      "/api/exec",
      { method: "POST", body: JSON.stringify({ command, args }) },
    );
  }

  /**
   * Start a pipeline — queue many commands and send them in ONE request,
   * collapsing N round-trips into one for far higher throughput:
   *
   *   const [, name] = await cache.pipeline()
   *     .set("user:1:name", "Ada")
   *     .get("user:1:name")
   *     .incr("visits")
   *     .exec();
   */
  pipeline(): Pipeline {
    return new Pipeline((commands, stopOnError) =>
      this.req<{ results: PipelineResult[] }>("/api/pipeline", {
        method: "POST",
        body: JSON.stringify({ commands, stop_on_error: stopOnError }),
      }).then((r) => r.results),
    );
  }
}

/**
 * A queued batch of commands, sent together by {@link Pipeline.exec}. Build it
 * with the generic {@link Pipeline.add} or the typed shortcuts; every method
 * returns `this` so calls chain. Results come back in submission order.
 */
export class Pipeline {
  private commands: string[][] = [];
  constructor(
    private readonly run: (
      commands: string[][],
      stopOnError: boolean,
    ) => Promise<PipelineResult[]>,
  ) {}

  /** Queue any command, e.g. `.add("EXPIRE", "k", 60)`. */
  add(command: string, ...args: (string | number)[]): this {
    this.commands.push([command, ...args.map(String)]);
    return this;
  }
  set(key: string, value: string) { return this.add("SET", key, value); }
  get(key: string) { return this.add("GET", key); }
  del(...keys: string[]) { return this.add("DEL", ...keys); }
  incr(key: string) { return this.add("INCR", key); }
  expire(key: string, seconds: number) { return this.add("EXPIRE", key, seconds); }

  /** Number of commands queued so far. */
  get size() { return this.commands.length; }

  /** Send the batch. `stopOnError` aborts at the first failing command. */
  exec(opts: { stopOnError?: boolean } = {}): Promise<PipelineResult[]> {
    return this.run(this.commands, opts.stopOnError ?? false);
  }
}
