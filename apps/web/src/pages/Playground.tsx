import { useState } from "react";
import { Terminal, Play } from "lucide-react";
import { api } from "../lib/api";
import { PageHeader } from "../components/Stat";

type Log = { cmd: string; ok: boolean; result?: unknown; error?: string };

const EXAMPLES = [
  'PING',
  'SET greeting "hello world"',
  'GET greeting',
  'INCR counter',
  'SEMANTIC_SET "best backend language" "Go is fast and simple"',
  'SEMANTIC_GET "what language should I use for APIs"',
  'MEMORY_ADD dhirav "prefers Go + React + Tailwind"',
  'MEMORY_QUERY dhirav "what tools does this user like?"',
  'CACHE_LLM "write a cold email" "Subject: Quick question…"',
  'CACHE_LLM_GET "draft a cold outreach"',
  'INFO',
];

function tokenize(s: string) {
  const out: string[] = [];
  let cur = "";
  let inQ = false;
  for (const c of s) {
    if (c === '"') { inQ = !inQ; continue; }
    if (c === " " && !inQ) { if (cur) { out.push(cur); cur = ""; } continue; }
    cur += c;
  }
  if (cur) out.push(cur);
  return out;
}

export default function Playground() {
  const [input, setInput] = useState(EXAMPLES[0]);
  const [logs, setLogs] = useState<Log[]>([]);

  const run = async () => {
    const parts = tokenize(input.trim());
    if (parts.length === 0) return;
    const [cmd, ...args] = parts;
    try {
      const r = await api.exec(cmd.toUpperCase(), args);
      setLogs([{ cmd: input, ok: r.ok, result: r.result, error: r.error }, ...logs].slice(0, 50));
    } catch (e) {
      setLogs([{ cmd: input, ok: false, error: (e as Error).message }, ...logs].slice(0, 50));
    }
  };

  return (
    <>
      <PageHeader
        title="Command Playground"
        subtitle="Fire Redis-compatible commands and AI-native commands at the engine."
      />

      <div className="card p-4">
        <div className="flex items-center gap-2">
          <Terminal size={16} className="text-primary" />
          <input
            className="input flex-1 font-mono"
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && run()}
            placeholder="COMMAND arg1 arg2 …"
          />
          <button className="btn-primary" onClick={run}>
            <Play size={14} /> Run
          </button>
        </div>

        <div className="mt-3 flex flex-wrap gap-1.5">
          {EXAMPLES.map((ex) => (
            <button key={ex} className="pill hover:bg-white/10" onClick={() => setInput(ex)}>
              {ex.length > 40 ? ex.slice(0, 38) + "…" : ex}
            </button>
          ))}
        </div>
      </div>

      <h2 className="mt-6 mb-2 text-sm font-semibold text-slate-300">Log</h2>
      {logs.length === 0 ? (
        <div className="card p-6 text-center text-sm text-slate-500">Run a command above to see the response.</div>
      ) : (
        <div className="card divide-y divide-border">
          {logs.map((l, i) => (
            <div key={i} className="p-3">
              <div className="flex items-center gap-2">
                <span className="pill">{l.ok ? "ok" : "err"}</span>
                <code className="font-mono text-sm text-slate-200">&gt; {l.cmd}</code>
              </div>
              <pre className="mt-2 overflow-x-auto rounded-md border border-border bg-bg/60 p-3 font-mono text-xs text-slate-300">
                {l.error ? l.error : JSON.stringify(l.result, null, 2)}
              </pre>
            </div>
          ))}
        </div>
      )}
    </>
  );
}
