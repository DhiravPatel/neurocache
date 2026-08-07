// When embedded in the Go binary, the dashboard is served from the same
// origin as the API — so default to a relative base. In dev (Vite on 5173),
// set VITE_API_URL=http://localhost:8080 in apps/web/.env to override.
const BASE = import.meta.env.VITE_API_URL ?? "";

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...(init?.headers ?? {}),
    },
  });
  if (!res.ok) {
    const body = await res.text().catch(() => "");
    throw new Error(`${res.status} ${res.statusText}: ${body}`);
  }
  return res.json() as Promise<T>;
}

export type EngineInfo = {
  version: string;
  uptime_seconds: number;
  commands: number;
  kv: { keys: number; bytes: number };
  semantic: { size: number; hits: number; misses: number; hit_rate: number };
  llm: { size: number; hits: number; misses: number; hit_rate: number };
  memory: { entries: number; users: number };
  eviction: string;
  runtime: { goroutines: number; go_version: string; heap_mb: number };
};

export type MemoryEntry = {
  id: string;
  user_id: string;
  text: string;
  created_at: string;
  meta?: Record<string, string>;
};

export type MemoryHit = { entry: MemoryEntry; score: number };

export type TimelineSample = {
  t: string;
  commands: number;
  sem_hits: number;
  sem_misses: number;
  llm_hits: number;
  llm_misses: number;
  kv_hits: number;
  kv_misses: number;
  p50_ms: number;
  p95_ms: number;
};

export type HotKey = { key: string; hits: number; last_seen_unix: number };

export type CommandCount = { command: string; count: number };

export type MetricsSummary = {
  commands: number;
  sem_hits: number;
  sem_misses: number;
  sem_hit_rate: number;
  llm_hits: number;
  llm_misses: number;
  llm_hit_rate: number;
  kv_hits: number;
  kv_misses: number;
  estimated_savings_usd: number;
  tokens_per_hit: number;
  usd_per_million_tokens: number;
  command_breakdown: CommandCount[];
};

// Cost & budgets
export type TenantUsage = {
  tenant: string;
  used: number;
  remaining: number;
  max: number;
  window_ms: number;
};

export type CostModel = {
  tokens_per_hit: number;
  usd_per_million_tokens: number;
};

export type QueueJob = {
  id: number;
  queue: string;
  priority: number;
  payload: string;
  idempotency_key?: string;
  attempts: number;
  last_error?: string;
  enqueued_at: string;
};

export type ConvTurn = { role: string; content: string; tokens: number; created_at: string };
export type PromptVersion = { version: number; body: string; created_at: string };
export type PromptListing = { name: string; latest_version: number; versions: number };
export type VariantStats = {
  variant: string;
  exposures: number;
  wins: number;
  win_rate: number;
  total_value: number;
  avg_value: number;
};
export type ExperimentStats = {
  name: string;
  variants: VariantStats[];
  winner?: string;
  created_at: string;
};

export type GraphNeighbor = { predicate: string; object: string };
export type ScheduledTask = {
  id: number;
  fire_at: string;
  cmd: string;
  args: string[];
  created_at: string;
};
export type FlagState = {
  name: string;
  on: boolean;
  percentage: number;
  allow?: string[];
  deny?: string[];
  evals: number;
  enabled: number;
  created_at: string;
  updated_at: string;
};

export const api = {
  info: () => req<EngineInfo>("/api/info"),
  health: () => req<{ status: string; uptime: number }>("/api/health"),

  // KV
  kvSet: (key: string, value: string, ttl = 0) =>
    req("/api/kv", { method: "POST", body: JSON.stringify({ key, value, ttl }) }),
  kvGet: (key: string) =>
    req<{ key: string; value: string | null; hit: boolean }>(
      `/api/kv/${encodeURIComponent(key)}`,
    ),
  kvDel: (key: string) =>
    req<{ deleted: number }>(`/api/kv/${encodeURIComponent(key)}`, { method: "DELETE" }),
  kvList: (prefix = "", limit = 50) =>
    req<{ keys: { key: string; value: string }[]; total: number }>(
      `/api/kv?prefix=${encodeURIComponent(prefix)}&limit=${limit}`,
    ),

  // Semantic
  semSet: (key: string, value: string) =>
    req("/api/semantic", { method: "POST", body: JSON.stringify({ key, value }) }),
  semGet: (q: string, threshold?: number) => {
    const qs = new URLSearchParams({ q });
    if (threshold !== undefined) qs.set("threshold", String(threshold));
    return req<{ query: string; hit: boolean; value: string | null; score: number }>(
      `/api/semantic?${qs}`,
    );
  },

  // LLM
  llmSet: (prompt: string, response: string) =>
    req("/api/llm", { method: "POST", body: JSON.stringify({ prompt, response }) }),
  llmGet: (prompt: string, threshold?: number) => {
    const qs = new URLSearchParams({ prompt });
    if (threshold !== undefined) qs.set("threshold", String(threshold));
    return req<{ prompt: string; hit: boolean; response: string | null; score: number }>(
      `/api/llm?${qs}`,
    );
  },
  llmStats: () =>
    req<{ size: number; hits: number; misses: number; hit_rate: number }>(
      "/api/llm/stats",
    ),

  // Memory
  memAdd: (user: string, text: string, meta?: Record<string, string>) =>
    req<MemoryEntry>(`/api/memory/${encodeURIComponent(user)}`, {
      method: "POST",
      body: JSON.stringify({ text, meta }),
    }),
  memList: (user: string) =>
    req<{ user: string; entries: MemoryEntry[] }>(
      `/api/memory/${encodeURIComponent(user)}`,
    ),
  memQuery: (user: string, q: string, k = 5) =>
    req<{ user: string; query: string; hits: MemoryHit[]; context: string }>(
      `/api/memory/${encodeURIComponent(user)}?q=${encodeURIComponent(q)}&k=${k}`,
    ),
  memDel: (user: string, id: string) =>
    req<{ deleted: boolean }>(
      `/api/memory/${encodeURIComponent(user)}/${encodeURIComponent(id)}`,
      { method: "DELETE" },
    ),

  // Raw command
  exec: (command: string, args: string[]) =>
    req<{ ok: boolean; result?: unknown; error?: string }>(`/api/exec`, {
      method: "POST",
      body: JSON.stringify({ command, args }),
    }),

  flushAll: () => req("/api/flushall", { method: "POST" }),

  // Cost & budgets
  costList: () => req<{ tenants: TenantUsage[] }>("/api/cost"),
  costUsage: (tenant: string) =>
    req<{ used: number; remaining: number; max: number; window_ms: number }>(
      `/api/cost/${encodeURIComponent(tenant)}`,
    ),
  costSetBudget: (tenant: string, maxUsd: number, windowMs: number) =>
    req<{ status: string }>(`/api/cost/${encodeURIComponent(tenant)}/budget`, {
      method: "POST",
      body: JSON.stringify({ max_usd: maxUsd, window_ms: windowMs }),
    }),
  costCharge: (tenant: string, usd: number) =>
    req<{ allowed: boolean; remaining: number }>(
      `/api/cost/${encodeURIComponent(tenant)}/charge`,
      { method: "POST", body: JSON.stringify({ usd }) },
    ),
  costReset: (tenant: string) =>
    req<{ reset: boolean }>(`/api/cost/${encodeURIComponent(tenant)}/reset`, {
      method: "POST",
    }),
  costModel: () => req<CostModel>("/api/cost-model"),
  costSetModel: (tokensPerHit: number, usdPerMillion: number) =>
    req<CostModel>("/api/cost-model", {
      method: "POST",
      body: JSON.stringify({
        tokens_per_hit: tokensPerHit,
        usd_per_million_tokens: usdPerMillion,
      }),
    }),

  // Pub/Sub
  publish: (channel: string, message: string) =>
    req<{ receivers: number }>("/api/publish", {
      method: "POST",
      body: JSON.stringify({ channel, message }),
    }),
  pubsubChannels: (pattern = "*") =>
    req<{ channels: string[]; num_subs: Record<string, number>; num_patterns: number }>(
      `/api/pubsub/channels?pattern=${encodeURIComponent(pattern)}`,
    ),
  /** Build the EventSource URL for a SUBSCRIBE stream (consumed via EventSource). */
  subscribeUrl: (opts: { channels?: string[]; patterns?: string[] }) => {
    const qs = new URLSearchParams();
    (opts.channels ?? []).forEach((c) => qs.append("channel", c));
    (opts.patterns ?? []).forEach((p) => qs.append("pattern", p));
    return `${BASE}/api/subscribe?${qs}`;
  },

  // Distributed locks
  lockList: () =>
    req<{ locks: { name: string; owner: string; token: number; remaining_ms: number }[] }>(
      "/api/locks",
    ),
  lockCheck: (name: string) =>
    req<{ held: boolean; owner?: string; token?: number; remaining_ms?: number }>(
      `/api/locks/${encodeURIComponent(name)}`,
    ),
  lockAcquire: (name: string, owner: string, ttlMs: number) =>
    req<{ acquired: boolean; token: number }>(
      `/api/locks/${encodeURIComponent(name)}/acquire`,
      { method: "POST", body: JSON.stringify({ owner, ttl_ms: ttlMs }) },
    ),
  lockRelease: (name: string, owner: string) =>
    req<{ released: boolean }>(`/api/locks/${encodeURIComponent(name)}/release`, {
      method: "POST",
      body: JSON.stringify({ owner }),
    }),
  lockExtend: (name: string, owner: string, ttlMs: number) =>
    req<{ extended: boolean }>(`/api/locks/${encodeURIComponent(name)}/extend`, {
      method: "POST",
      body: JSON.stringify({ owner, ttl_ms: ttlMs }),
    }),

  // Rate limiting (429 is a normal "denied" outcome, not a transport error)
  rateLimit: async (key: string, windowMs: number, max: number, cost = 1, peek = false) => {
    const res = await fetch(`${BASE}/api/ratelimit`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ key, window_ms: windowMs, max, cost, peek }),
    });
    if (res.status !== 200 && res.status !== 429) throw new Error(`${res.status}`);
    return res.json() as Promise<{
      allowed: boolean; remaining: number; retry_after_ms: number; reset_ms: number;
    }>;
  },
  rateLimitReset: (key: string) =>
    req("/api/ratelimit/reset", { method: "POST", body: JSON.stringify({ key }) }),

  // Leaderboards
  lbSet: (name: string, member: string, score: number) =>
    req<{ member: string; score: number; rank: number }>(
      `/api/leaderboard/${encodeURIComponent(name)}`,
      { method: "POST", body: JSON.stringify({ member, score }) },
    ),
  lbIncr: (name: string, member: string, by: number) =>
    req<{ member: string; score: number; rank: number }>(
      `/api/leaderboard/${encodeURIComponent(name)}/incr`,
      { method: "POST", body: JSON.stringify({ member, by }) },
    ),
  lbTop: (name: string, n = 10) =>
    req<{ count: number; entries: { member: string; score: number; rank: number }[] }>(
      `/api/leaderboard/${encodeURIComponent(name)}/top?n=${n}`,
    ),
  lbRemove: (name: string, member: string) =>
    req<{ removed: boolean }>(
      `/api/leaderboard/${encodeURIComponent(name)}/${encodeURIComponent(member)}`,
      { method: "DELETE" },
    ),

  // Queues (durable Workers job queue)
  queueList: () => req<{ queues: string[] }>("/api/worker"),
  queueStats: (name: string) =>
    req<{ name: string; pending: number; reserved: number; dlq: number; max_attempts: number; dlq_cap: number }>(
      `/api/worker/${encodeURIComponent(name)}/stats`,
    ),
  queueEnqueue: (name: string, payload: string, priority = 0) =>
    req<{ id: number }>(`/api/worker/${encodeURIComponent(name)}`, {
      method: "POST",
      body: JSON.stringify({ payload, priority }),
    }),
  queueDequeue: (name: string) =>
    req<{ job: QueueJob | null }>(`/api/worker/${encodeURIComponent(name)}/next`),
  queueAck: (name: string, id: number) =>
    req<{ acked: boolean }>(`/api/worker/${encodeURIComponent(name)}/ack/${id}`, { method: "POST" }),
  queueDLQ: (name: string) =>
    req<{ jobs: QueueJob[] }>(`/api/worker/${encodeURIComponent(name)}/dlq`),
  queueRequeue: (name: string, id: number) =>
    req<{ status: string }>(`/api/worker/${encodeURIComponent(name)}/requeue/${id}`, { method: "POST" }),

  // Streams
  streamAdd: (key: string, fields: Record<string, string>) =>
    req<{ id: string }>(`/api/streams/${encodeURIComponent(key)}`, {
      method: "POST",
      body: JSON.stringify({ fields }),
    }),
  streamRange: (key: string, count = 50, reverse = true) =>
    req<{ length: number; entries: { id: string; fields: Record<string, string> }[] }>(
      `/api/streams/${encodeURIComponent(key)}?count=${count}&reverse=${reverse ? 1 : 0}`,
    ),
  streamTailUrl: (key: string, last = "$") =>
    `${BASE}/api/streams/${encodeURIComponent(key)}/tail?last=${encodeURIComponent(last)}`,

  // Conversations / sessions
  convList: () => req<{ conversations: string[]; count: number }>("/api/conv"),
  convWindow: (key: string, maxTokens?: number) =>
    req<{ turns: ConvTurn[] }>(
      `/api/conv/${encodeURIComponent(key)}${maxTokens ? `?max_tokens=${maxTokens}` : ""}`,
    ),
  convAppend: (key: string, role: string, content: string) =>
    req<{ turns: number }>(`/api/conv/${encodeURIComponent(key)}`, {
      method: "POST",
      body: JSON.stringify({ role, content }),
    }),
  convSummarize: (key: string, summary: string, keepTokens?: number) =>
    req<{ dropped_turns: number; tokens_remaining: number }>(
      `/api/conv/${encodeURIComponent(key)}/summarize`,
      { method: "POST", body: JSON.stringify({ summary, keep_tokens: keepTokens }) },
    ),
  convReset: (key: string) =>
    req<{ reset: boolean }>(`/api/conv/${encodeURIComponent(key)}`, { method: "DELETE" }),

  // Prompt templates
  promptList: () => req<PromptListing[]>("/api/prompts"),
  promptGet: (name: string, version?: number) =>
    req<PromptVersion>(`/api/prompts/${encodeURIComponent(name)}${version ? `?version=${version}` : ""}`),
  promptVersions: (name: string) =>
    req<PromptVersion[]>(`/api/prompts/${encodeURIComponent(name)}/versions`),
  promptSet: (name: string, body: string, version?: number) =>
    req<{ version: number }>(`/api/prompts/${encodeURIComponent(name)}`, {
      method: "POST",
      body: JSON.stringify({ body, version }),
    }),
  promptRender: (name: string, vars: Record<string, string>, version?: number) =>
    req<{ rendered: string }>(`/api/prompts/${encodeURIComponent(name)}/render`, {
      method: "POST",
      body: JSON.stringify({ vars, version }),
    }),
  promptDelete: (name: string, version?: number) =>
    req<{ removed: number }>(
      `/api/prompts/${encodeURIComponent(name)}${version ? `?version=${version}` : ""}`,
      { method: "DELETE" },
    ),

  // A/B experiments
  abList: () => req<{ experiments: string[] }>("/api/ab"),
  abStats: (name: string) => req<ExperimentStats>(`/api/ab/${encodeURIComponent(name)}`),
  abDefine: (name: string, variants: string[], weights?: number[]) =>
    req<{ status: string }>("/api/ab", {
      method: "POST",
      body: JSON.stringify({ name, variants, weights }),
    }),
  abAssign: (name: string, user: string) =>
    req<{ variant?: string; hit?: boolean }>(
      `/api/ab/${encodeURIComponent(name)}/assign?user=${encodeURIComponent(user)}`,
    ),
  abExpose: (name: string, variant: string) =>
    req<{ status: string }>(`/api/ab/${encodeURIComponent(name)}/expose`, {
      method: "POST",
      body: JSON.stringify({ variant }),
    }),
  abRecord: (name: string, variant: string, value: number) =>
    req<{ status: string }>(`/api/ab/${encodeURIComponent(name)}/record`, {
      method: "POST",
      body: JSON.stringify({ variant, value }),
    }),
  abReset: (name: string) =>
    req<{ reset: boolean }>(`/api/ab/${encodeURIComponent(name)}/reset`, { method: "POST" }),
  abDelete: (name: string) =>
    req<{ removed: boolean }>(`/api/ab/${encodeURIComponent(name)}`, { method: "DELETE" }),

  // Knowledge graph
  graphLink: (subject: string, predicate: string, object: string) =>
    req<{ created: boolean }>("/api/graph/link", {
      method: "POST",
      body: JSON.stringify({ subject, predicate, object }),
    }),
  graphUnlink: (subject: string, predicate: string, object: string) =>
    req<{ removed: boolean }>("/api/graph/unlink", {
      method: "POST",
      body: JSON.stringify({ subject, predicate, object }),
    }),
  graphNeighbors: (subject: string, predicate?: string) => {
    const qs = new URLSearchParams({ subject });
    if (predicate) qs.set("predicate", predicate);
    return req<{ neighbors: GraphNeighbor[] }>(`/api/graph/neighbors?${qs}`);
  },
  graphIn: (object: string, predicate?: string) => {
    const qs = new URLSearchParams({ object });
    if (predicate) qs.set("predicate", predicate);
    return req<{ subjects: string[] }>(`/api/graph/in?${qs}`);
  },
  graphPath: (from: string, to: string, maxDepth?: number) => {
    const qs = new URLSearchParams({ from, to });
    if (maxDepth) qs.set("max_depth", String(maxDepth));
    // path is the chain of edges (predicate + arrived-at node); `from` is implicit.
    return req<{ found: boolean; path?: GraphNeighbor[] }>(`/api/graph/path?${qs}`);
  },
  graphSubjects: () => req<{ subjects: string[] }>("/api/graph/subjects"),
  graphStats: () => req<Record<string, number>>("/api/graph/stats"),

  // Moderation / safety
  safeCheck: (text: string) =>
    req<{ hit: boolean; result?: { safe: boolean; score: number; categories?: string[] } }>(
      `/api/safe?text=${encodeURIComponent(text)}`,
    ),
  safeInject: (text: string) =>
    req<{ score: number; matched: string[] }>(`/api/safe/inject?text=${encodeURIComponent(text)}`),
  safeSet: (text: string, safe: boolean, score: number, categories?: string[]) =>
    req<{ status: string }>("/api/safe", {
      method: "POST",
      body: JSON.stringify({ text, safe, score, categories }),
    }),
  safeStats: () => req<Record<string, number>>("/api/safe/stats"),

  // Feature flags
  flagList: () => req<{ flags: string[] }>("/api/flag"),
  flagGet: (name: string) => req<FlagState>(`/api/flag/${encodeURIComponent(name)}`),
  flagSet: (name: string, on: boolean, percentage: number, allow?: string[], deny?: string[]) =>
    req<{ status: string }>(`/api/flag/${encodeURIComponent(name)}`, {
      method: "POST",
      body: JSON.stringify({ on, percentage, allow, deny }),
    }),
  flagIs: (name: string, user: string) =>
    req<{ enabled: boolean }>(
      `/api/flag/${encodeURIComponent(name)}/is?user=${encodeURIComponent(user)}`,
    ),
  flagDelete: (name: string) =>
    req<{ removed: boolean }>(`/api/flag/${encodeURIComponent(name)}`, { method: "DELETE" }),

  // Churn (tag-based invalidation)
  churnTags: () => req<{ tags: string[] }>("/api/churn/tags"),
  churnTag: (key: string, tags: string[]) =>
    req<{ added: number }>(`/api/churn/${encodeURIComponent(key)}`, {
      method: "POST",
      body: JSON.stringify({ tags }),
    }),
  churnTagsOf: (key: string) => req<{ tags: string[] }>(`/api/churn/${encodeURIComponent(key)}`),
  churnKeysFor: (tag: string) =>
    req<{ keys: string[] }>(`/api/churn/keys?tag=${encodeURIComponent(tag)}`),
  churnInvalidate: (tags: string[]) =>
    req<{ dropped: string[] }>("/api/churn/invalidate", {
      method: "POST",
      body: JSON.stringify({ tags }),
    }),
  churnStats: () => req<Record<string, number>>("/api/churn/stats"),

  // Scheduler
  scheduleList: () => req<{ tasks: ScheduledTask[] }>("/api/schedule"),
  scheduleStats: () => req<Record<string, number>>("/api/schedule/stats"),
  scheduleIn: (delayMs: number, cmd: string, args: string[] = []) =>
    req<{ id: number }>("/api/schedule/in", {
      method: "POST",
      body: JSON.stringify({ delay_ms: delayMs, cmd, args }),
    }),
  scheduleAt: (unixMs: number, cmd: string, args: string[] = []) =>
    req<{ id: number }>("/api/schedule/at", {
      method: "POST",
      body: JSON.stringify({ unix_ms: unixMs, cmd, args }),
    }),
  scheduleCancel: (id: number) =>
    req<{ cancelled: boolean }>(`/api/schedule/${id}`, { method: "DELETE" }),

  // Event sourcing
  eventLen: (stream: string) => req<{ len: number }>(`/api/event/${encodeURIComponent(stream)}/len`),
  eventRange: (stream: string, start = 0, end = 0) =>
    req<{ events: unknown[] }>(
      `/api/event/${encodeURIComponent(stream)}/range?start=${start}&end=${end}`,
    ),
  eventAppend: (stream: string, payload: unknown) =>
    req<{ seq: number }>(`/api/event/${encodeURIComponent(stream)}`, {
      method: "POST",
      body: JSON.stringify(payload),
    }),
  eventProject: (stream: string, name: string, reducer: string, field?: string, groupBy?: string) =>
    req<{ status: string }>(`/api/event/${encodeURIComponent(stream)}/project`, {
      method: "POST",
      body: JSON.stringify({ name, reducer, field, group_by: groupBy }),
    }),
  eventProjection: (stream: string, name: string) =>
    req<{ projection: unknown }>(
      `/api/event/${encodeURIComponent(stream)}/projection/${encodeURIComponent(name)}`,
    ),

  // Metrics / analytics
  metricsSummary: () => req<MetricsSummary>("/api/metrics/summary"),
  metricsTimeline: () =>
    req<{ samples: TimelineSample[] }>("/api/metrics/timeline"),
  metricsHotKeys: (k = 10) =>
    req<{ keys: HotKey[] }>(`/api/metrics/hot-keys?k=${k}`),
  metricsBreakdown: () =>
    req<{ commands: CommandCount[] }>("/api/metrics/breakdown"),

  // HOTKEYS — runtime HeavyKeeper-backed top-K (every keyspace
  // mutation, downsampled). Distinct from metricsHotKeys which only
  // tracks GET hits.
  // Vector sets — first-class V* type inventory.
  vectorSets: () =>
    req<{
      sets: {
        key: string;
        algo: string;
        dim: number;
        metric: string;
        m: number;
        ef_construct: number;
        ef_runtime: number;
        card: number;
        bytes_approx: number;
      }[];
    }>("/api/vector/sets"),

  hotKeysTracker: (k = 25) =>
    req<{
      keys: { key: string; count: number }[];
      stats: {
        Enabled: boolean;
        K: number;
        Width: number;
        Depth: number;
        Decay: number;
        SampleEvery: number;
        Threshold: number;
        Tracked: number;
        Observations: number;
        Events: number;
        BytesApprox: number;
      };
    }>(`/api/hotkeys?k=${k}`),
};
