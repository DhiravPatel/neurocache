import { useEffect, useState } from "react";
import { Brain, Plus, Search, Trash2 } from "lucide-react";
import { api, type MemoryEntry, type MemoryHit } from "../lib/api";
import { PageHeader } from "../components/Stat";

export default function MemoryPage() {
  const [user, setUser] = useState("dhirav");
  const [text, setText] = useState("");
  const [query, setQuery] = useState("");
  const [entries, setEntries] = useState<MemoryEntry[]>([]);
  const [hits, setHits] = useState<MemoryHit[]>([]);
  const [context, setContext] = useState("");
  const [err, setErr] = useState<string | null>(null);

  const loadList = async (u: string) => {
    try {
      const r = await api.memList(u);
      setEntries(r.entries ?? []);
      setErr(null);
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  useEffect(() => {
    loadList(user);
  }, [user]);

  const add = async () => {
    if (!text.trim()) return;
    await api.memAdd(user, text.trim());
    setText("");
    loadList(user);
  };

  const search = async () => {
    if (!query.trim()) return;
    const r = await api.memQuery(user, query.trim(), 5);
    setHits(r.hits ?? []);
    setContext(r.context ?? "");
  };

  const del = async (id: string) => {
    await api.memDel(user, id);
    loadList(user);
  };

  return (
    <>
      <PageHeader
        title="User Memory"
        subtitle="Per-user persistent context for LLM apps. MEMORY_QUERY runs a semantic top-k search."
      />

      <div className="card p-4">
        <div className="mb-3 flex items-center gap-2 text-sm font-medium">
          <Brain size={15} /> User
        </div>
        <input className="input w-64" value={user} onChange={(e) => setUser(e.target.value)} placeholder="user id" />
      </div>

      <div className="mt-4 grid gap-4 md:grid-cols-2">
        <div className="card p-4">
          <div className="mb-3 flex items-center gap-2 text-sm font-medium">
            <Plus size={15} /> MEMORY_ADD
          </div>
          <div className="space-y-2">
            <textarea className="input min-h-24" placeholder="a fact about this user…" value={text} onChange={(e) => setText(e.target.value)} />
            <button className="btn-primary w-full" onClick={add}>
              Remember
            </button>
          </div>
        </div>

        <div className="card p-4">
          <div className="mb-3 flex items-center gap-2 text-sm font-medium">
            <Search size={15} /> MEMORY_QUERY
          </div>
          <div className="space-y-2">
            <input className="input" placeholder="what does the user prefer?" value={query} onChange={(e) => setQuery(e.target.value)} />
            <button className="btn-primary w-full" onClick={search}>
              Recall
            </button>
          </div>
        </div>
      </div>

      {hits.length > 0 ? (
        <div className="mt-4 card p-4">
          <div className="mb-2 text-xs uppercase tracking-wider text-slate-500">Top matches</div>
          <div className="space-y-2">
            {hits.map((h) => (
              <div key={h.entry.id} className="flex items-start gap-3 rounded-md border border-border bg-bg/40 p-2">
                <div className="flex-1 text-sm text-slate-300">{h.entry.text}</div>
                <span className="pill">{(h.score * 100).toFixed(0)}%</span>
              </div>
            ))}
          </div>
          {context ? (
            <div className="mt-3 rounded-md border border-primary/40 bg-primary/5 p-3 text-sm">
              <div className="mb-1 text-xs uppercase tracking-wider text-primary">Synthesized context</div>
              <pre className="whitespace-pre-wrap font-mono text-xs text-slate-300">{context}</pre>
            </div>
          ) : null}
        </div>
      ) : null}

      <h2 className="mt-8 mb-3 text-sm font-semibold text-slate-300">
        All memories for <span className="font-mono text-primary">{user}</span>
      </h2>
      {err ? <div className="text-sm text-rose-400">{err}</div> : null}
      {entries.length === 0 ? (
        <div className="card p-6 text-center text-sm text-slate-500">No memories for this user yet.</div>
      ) : (
        <div className="card divide-y divide-border">
          {entries.map((e) => (
            <div key={e.id} className="flex items-start gap-3 px-4 py-3">
              <div className="flex-1">
                <div className="text-sm text-slate-200">{e.text}</div>
                <div className="mt-1 text-[11px] text-slate-500">{e.id} · {new Date(e.created_at).toLocaleString()}</div>
              </div>
              <button className="btn-ghost text-rose-400 hover:text-rose-300" onClick={() => del(e.id)}>
                <Trash2 size={14} />
              </button>
            </div>
          ))}
        </div>
      )}
    </>
  );
}
