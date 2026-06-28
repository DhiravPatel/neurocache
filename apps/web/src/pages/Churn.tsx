import { useEffect, useState } from "react";
import { Tags, Plus, Trash2, Zap, KeyRound } from "lucide-react";
import { api } from "../lib/api";
import { usePolling } from "../lib/usePolling";
import { PageHeader, Stat } from "../components/Stat";

export default function ChurnPage() {
  const { data: tagData } = usePolling(api.churnTags, 2000);
  const { data: stats } = usePolling(api.churnStats, 2500);
  const tags = tagData?.tags ?? [];

  const [key, setKey] = useState("user:1:profile");
  const [tagInput, setTagInput] = useState("user:1, profiles");
  const [selectedTag, setSelectedTag] = useState("");
  const [keys, setKeys] = useState<string[]>([]);
  const [msg, setMsg] = useState<string | null>(null);

  const active = selectedTag || tags[0] || "";
  const loadKeys = async (t: string) => {
    if (!t) { setKeys([]); return; }
    try { setKeys((await api.churnKeysFor(t)).keys ?? []); } catch { setKeys([]); }
  };
  useEffect(() => { loadKeys(active); /* eslint-disable-next-line */ }, [active, tags.length]);

  const tagKey = async () => {
    const ts = tagInput.split(",").map((s) => s.trim()).filter(Boolean);
    if (!key.trim() || ts.length === 0) { setMsg("key + at least one tag required"); return; }
    // Seed the key in the KV store so an invalidate has something to drop.
    await api.kvSet(key.trim(), "cached-value", 0);
    const r = await api.churnTag(key.trim(), ts);
    setMsg(`Tagged ${key.trim()} with ${r.added} tag(s)`);
    setSelectedTag(ts[0]);
    loadKeys(ts[0]);
  };
  const invalidate = async (t: string) => {
    const r = await api.churnInvalidate([t]);
    setMsg(`Invalidated tag "${t}" — dropped ${r.dropped.length} key(s): ${r.dropped.join(", ") || "none"}`);
    loadKeys(t);
  };

  return (
    <>
      <PageHeader
        title="Tag Invalidation"
        subtitle="Tag cache keys, then bust an entire group in one call — drop every key carrying a tag without tracking key sets in your app. The classic 'invalidate everything for user X' pattern, server-side."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Tags" icon={Tags} value={stats?.unique_tags ?? tags.length} accent="primary" />
        <Stat label="Tagged keys" icon={KeyRound} value={stats?.tagged_keys ?? 0} accent="accent" />
        <Stat label={`Keys in "${active || "—"}"`} icon={Tags} value={keys.length} accent="emerald" />
        <Stat label="Avg keys / tag" icon={Zap}
          value={stats?.unique_tags ? ((stats.tagged_keys ?? 0) / stats.unique_tags).toFixed(1) : "0"} accent="primary" />
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-[360px_1fr]">
        <div className="space-y-4">
          <div className="card p-4">
            <div className="mb-3 flex items-center gap-2 text-sm font-medium"><Plus size={15} /> Tag a key</div>
            <div className="space-y-2">
              <label className="block">
                <span className="text-xs text-slate-400">Cache key</span>
                <input className="input mt-1" value={key} onChange={(e) => setKey(e.target.value)} />
              </label>
              <label className="block">
                <span className="text-xs text-slate-400">Tags (comma-separated)</span>
                <input className="input mt-1" value={tagInput} onChange={(e) => setTagInput(e.target.value)} />
              </label>
              <button className="btn-primary w-full" onClick={tagKey}><Plus size={14} /> Tag key</button>
            </div>
            {msg && <div className="mt-2 text-xs text-slate-400">{msg}</div>}
          </div>

          <div className="card p-4">
            <div className="mb-2 text-[11px] font-semibold uppercase tracking-wider text-slate-500">Tags</div>
            {tags.length === 0 ? (
              <div className="text-sm text-slate-500">No tags yet.</div>
            ) : (
              <div className="flex flex-wrap gap-1.5">
                {tags.map((t) => (
                  <button key={t} onClick={() => setSelectedTag(t)}
                    className={"rounded-full border px-2.5 py-1 font-mono text-xs " +
                      (t === active ? "border-primary/50 bg-primary/15 text-primary" : "border-border text-slate-400 hover:text-slate-200")}>
                    {t}
                  </button>
                ))}
              </div>
            )}
          </div>
        </div>

        <div className="card flex flex-col p-0">
          <div className="flex items-center gap-2 border-b border-border px-4 py-2.5 text-sm font-medium">
            <Tags size={15} /> Keys tagged
            <span className="rounded bg-primary/15 px-1.5 py-0.5 font-mono text-[12px] text-primary">{active || "—"}</span>
            <button onClick={() => active && invalidate(active)} disabled={!active || keys.length === 0}
              className="ml-auto inline-flex items-center gap-1 rounded-md border border-rose-400/30 bg-rose-500/10 px-2.5 py-1 text-xs text-rose-300 hover:bg-rose-500/20 disabled:opacity-40">
              <Zap size={13} /> Invalidate tag
            </button>
          </div>
          {keys.length === 0 ? (
            <div className="flex h-40 items-center justify-center text-sm text-slate-500">
              {active ? "No live keys for this tag." : "Tag a key to get started."}
            </div>
          ) : (
            <ul className="divide-y divide-border">
              {keys.map((k) => (
                <li key={k} className="flex items-center gap-2 px-4 py-2.5">
                  <KeyRound size={14} className="text-slate-500" />
                  <span className="font-mono text-[13px] text-slate-200">{k}</span>
                  <Trash2 size={13} className="ml-auto text-slate-600" />
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </>
  );
}
