import { useState } from "react";
import { Search, Plus, Sparkles } from "lucide-react";
import { api } from "../lib/api";
import { PageHeader } from "../components/Stat";

type Hit = { query: string; hit: boolean; value: string | null; score: number };

export default function SemanticPage() {
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [query, setQuery] = useState("");
  const [threshold, setThreshold] = useState("0.75");
  const [history, setHistory] = useState<Hit[]>([]);
  const [err, setErr] = useState<string | null>(null);

  const add = async () => {
    if (!key || !value) return;
    try {
      await api.semSet(key, value);
      setKey("");
      setValue("");
      setErr(null);
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  const search = async () => {
    if (!query) return;
    try {
      const r = await api.semGet(query, Number(threshold));
      setHistory([r, ...history].slice(0, 20));
      setErr(null);
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  return (
    <>
      <PageHeader
        title="Semantic Cache"
        subtitle="SEMANTIC_SET stores a key by its meaning. SEMANTIC_GET finds it back from any semantically-close query."
      />

      <div className="grid gap-4 md:grid-cols-2">
        <div className="card p-4">
          <div className="mb-3 flex items-center gap-2 text-sm font-medium">
            <Plus size={15} /> SEMANTIC_SET
          </div>
          <div className="space-y-2">
            <input className="input" placeholder='key phrase, e.g. "best backend language"' value={key} onChange={(e) => setKey(e.target.value)} />
            <textarea className="input min-h-24" placeholder="cached value" value={value} onChange={(e) => setValue(e.target.value)} />
            <button className="btn-primary w-full" onClick={add}>
              Store semantically
            </button>
          </div>
        </div>

        <div className="card p-4">
          <div className="mb-3 flex items-center gap-2 text-sm font-medium">
            <Search size={15} /> SEMANTIC_GET
          </div>
          <div className="space-y-2">
            <input className="input" placeholder="try a paraphrased query…" value={query} onChange={(e) => setQuery(e.target.value)} />
            <div className="flex items-center gap-2">
              <label className="text-xs text-slate-500">threshold</label>
              <input
                className="input w-20"
                type="number"
                step="0.05"
                min="0"
                max="1"
                value={threshold}
                onChange={(e) => setThreshold(e.target.value)}
              />
              <button className="btn-primary ml-auto" onClick={search}>
                Search
              </button>
            </div>
          </div>
        </div>
      </div>

      {err ? <div className="mt-4 text-sm text-rose-400">{err}</div> : null}

      <h2 className="mt-8 mb-3 flex items-center gap-2 text-sm font-semibold text-slate-300">
        <Sparkles size={14} /> Recent queries
      </h2>
      {history.length === 0 ? (
        <div className="card p-6 text-center text-sm text-slate-500">Nothing yet — try a query above.</div>
      ) : (
        <div className="card divide-y divide-border">
          {history.map((h, i) => (
            <div key={i} className="px-4 py-3">
              <div className="flex items-center gap-2 text-sm">
                <span className="font-mono text-primary">{h.query}</span>
                <span className={"pill ml-auto " + (h.hit ? "text-emerald-400" : "text-rose-400")}>
                  {h.hit ? "hit" : "miss"} · {(h.score * 100).toFixed(0)}%
                </span>
              </div>
              {h.hit ? (
                <div className="mt-1.5 text-sm text-slate-300">{h.value}</div>
              ) : (
                <div className="mt-1.5 text-xs text-slate-500">below similarity threshold</div>
              )}
            </div>
          ))}
        </div>
      )}
    </>
  );
}
