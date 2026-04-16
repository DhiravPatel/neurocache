const BASE = import.meta.env.VITE_API_URL ?? "http://localhost:8080";

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
};
