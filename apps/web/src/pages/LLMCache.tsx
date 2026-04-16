import { useEffect, useState } from "react";
import { Zap, Plus, Search } from "lucide-react";
import { api } from "../lib/api";
import { PageHeader } from "../components/Stat";

export default function LLMCachePage() {
  const [prompt, setPrompt] = useState("");
  const [response, setResponse] = useState("");
  const [query, setQuery] = useState("");
  const [result, setResult] = useState<{ hit: boolean; response: string | null; score: number } | null>(null);
  const [stats, setStats] = useState<{ size: number; hits: number; misses: number; hit_rate: number } | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const refresh = async () => {
    try {
      setStats(await api.llmStats());
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  useEffect(() => {
    refresh();
  }, []);

  const add = async () => {
    if (!prompt || !response) return;
    await api.llmSet(prompt, response);
    setPrompt("");
    setResponse("");
    refresh();
  };

  const search = async () => {
    if (!query) return;
    const r = await api.llmGet(query, 0.75);
    setResult(r);
    refresh();
  };

  return (
    <>
      <PageHeader
        title="LLM Response Cache"
        subtitle="Cache expensive LLM outputs keyed by the prompt's meaning. Semantically similar prompts reuse the cached answer."
      />

      {stats ? (
        <div className="mb-6 grid grid-cols-4 gap-3">
          <div className="card p-3 text-center">
            <div className="text-xs text-slate-500">Entries</div>
            <div className="mt-1 text-lg font-semibold">{stats.size}</div>
          </div>
          <div className="card p-3 text-center">
            <div className="text-xs text-slate-500">Hits</div>
            <div className="mt-1 text-lg font-semibold hit">{stats.hits}</div>
          </div>
          <div className="card p-3 text-center">
            <div className="text-xs text-slate-500">Misses</div>
            <div className="mt-1 text-lg font-semibold miss">{stats.misses}</div>
          </div>
          <div className="card p-3 text-center">
            <div className="text-xs text-slate-500">Hit Rate</div>
            <div className="mt-1 text-lg font-semibold text-accent">
              {(stats.hit_rate * 100).toFixed(0)}%
            </div>
          </div>
        </div>
      ) : null}

      <div className="grid gap-4 md:grid-cols-2">
        <div className="card p-4">
          <div className="mb-3 flex items-center gap-2 text-sm font-medium">
            <Plus size={15} /> Cache an LLM response
          </div>
          <div className="space-y-2">
            <textarea className="input min-h-20" placeholder="original prompt" value={prompt} onChange={(e) => setPrompt(e.target.value)} />
            <textarea className="input min-h-24" placeholder="LLM response to cache" value={response} onChange={(e) => setResponse(e.target.value)} />
            <button className="btn-primary w-full" onClick={add}>
              Cache
            </button>
          </div>
        </div>

        <div className="card p-4">
          <div className="mb-3 flex items-center gap-2 text-sm font-medium">
            <Search size={15} /> Look up a prompt
          </div>
          <div className="space-y-2">
            <textarea className="input min-h-20" placeholder="paraphrased user prompt…" value={query} onChange={(e) => setQuery(e.target.value)} />
            <button className="btn-primary w-full" onClick={search}>
              Check cache
            </button>
          </div>

          {result ? (
            <div className="mt-3 rounded-md border border-border bg-bg/40 p-3 text-sm">
              <div className="flex items-center gap-2">
                <Zap size={14} className={result.hit ? "text-emerald-400" : "text-rose-400"} />
                <span className={result.hit ? "hit" : "miss"}>
                  {result.hit ? "cache hit" : "cache miss"}
                </span>
                <span className="pill ml-auto">{(result.score * 100).toFixed(0)}% similar</span>
              </div>
              {result.hit ? <div className="mt-2 text-slate-300">{result.response}</div> : null}
            </div>
          ) : null}
        </div>
      </div>

      {err ? <div className="mt-4 text-sm text-rose-400">{err}</div> : null}
    </>
  );
}
