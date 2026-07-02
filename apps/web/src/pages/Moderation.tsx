import { useState } from "react";
import { ShieldCheck, ShieldAlert, ScanSearch, Syringe, Save } from "lucide-react";
import { api } from "../lib/api";
import { usePolling } from "../lib/usePolling";
import { PageHeader, Stat } from "../components/Stat";

export default function ModerationPage() {
  const { data: stats } = usePolling(api.safeStats, 2500);

  const [text, setText] = useState("How do I reset my password?");
  const [verdict, setVerdict] = useState<{ hit: boolean; result?: { safe: boolean; score: number; categories?: string[] } } | null>(null);
  const [inject, setInject] = useState<{ score: number; matched: string[] } | null>(null);
  const [msg, setMsg] = useState<string | null>(null);

  const check = async () => {
    try {
      const [v, ij] = await Promise.all([api.safeCheck(text), api.safeInject(text)]);
      setVerdict(v); setInject(ij); setMsg(null);
    } catch (e) { setMsg((e as Error).message); }
  };
  const cache = async (safe: boolean) => {
    // Cache a verdict; score mirrors the injection heuristic when unsafe.
    const score = safe ? 0.02 : Math.max(0.8, inject?.score ?? 0.9);
    await api.safeSet(text, safe, score, safe ? [] : ["unsafe"]);
    setMsg(`Cached verdict: ${safe ? "safe" : "unsafe"} (score ${score.toFixed(2)})`);
    check();
  };

  const injHigh = (inject?.score ?? 0) >= 0.5;

  return (
    <>
      <PageHeader
        title="Moderation & Safety"
        subtitle="Cache moderation verdicts so you never re-score the same text, and screen prompts for injection attempts with a built-in heuristic — keep the safety layer fast and cheap."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Cached verdicts" icon={ShieldCheck} value={stats?.size ?? stats?.entries ?? 0} accent="primary" />
        <Stat label="Hits" icon={ScanSearch} value={stats?.hits ?? 0} accent="emerald" />
        <Stat label="Misses" icon={ScanSearch} value={stats?.misses ?? 0} accent="accent" />
        <Stat label="Injection risk" icon={Syringe} value={inject ? `${(inject.score * 100).toFixed(0)}%` : "—"}
          accent={injHigh ? "rose" : "emerald"} hint={inject ? `${inject.matched.length} pattern(s)` : undefined} />
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-2">
        <div className="card p-4">
          <div className="mb-3 flex items-center gap-2 text-sm font-medium"><ScanSearch size={15} /> Screen text</div>
          <textarea className="input min-h-28" value={text} onChange={(e) => setText(e.target.value)}
            placeholder="text to moderate / screen…" />
          <div className="mt-3 flex gap-2">
            <button className="btn-primary" onClick={check}><ScanSearch size={14} /> Check</button>
            <button className="btn-ghost" onClick={() => cache(true)}><Save size={14} /> Cache safe</button>
            <button className="btn-ghost" onClick={() => cache(false)}><Save size={14} /> Cache unsafe</button>
          </div>
          {msg && <div className="mt-2 text-xs text-slate-400">{msg}</div>}
        </div>

        <div className="space-y-4">
          <div className="card p-4">
            <div className="mb-2 flex items-center gap-2 text-sm font-medium"><ShieldCheck size={15} /> Cached verdict</div>
            {verdict === null ? (
              <div className="text-sm text-slate-500">Run a check to see the cached verdict.</div>
            ) : !verdict.hit ? (
              <div className="text-sm text-slate-400">Cache miss — no verdict stored for this text yet.</div>
            ) : (
              <div className="flex items-center gap-3 text-sm">
                {verdict.result?.safe ? (
                  <><ShieldCheck size={18} className="text-emerald-400" /><span className="hit">safe</span></>
                ) : (
                  <><ShieldAlert size={18} className="text-rose-400" /><span className="miss">unsafe</span></>
                )}
                <span className="pill">score {verdict.result?.score?.toFixed(2)}</span>
                {verdict.result?.categories?.length ? (
                  <span className="text-xs text-slate-400">{verdict.result.categories.join(", ")}</span>
                ) : null}
              </div>
            )}
          </div>

          <div className="card p-4">
            <div className="mb-2 flex items-center gap-2 text-sm font-medium"><Syringe size={15} /> Prompt-injection scan</div>
            {inject === null ? (
              <div className="text-sm text-slate-500">Run a check to scan for injection patterns.</div>
            ) : (
              <div>
                <div className="flex items-center gap-2">
                  <div className="h-2 w-full overflow-hidden rounded-full bg-bg">
                    <div className={"h-full " + (injHigh ? "bg-rose-500" : (inject.score > 0.2 ? "bg-amber-400" : "bg-emerald-400"))}
                      style={{ width: `${Math.min(100, inject.score * 100)}%` }} />
                  </div>
                  <span className={"shrink-0 text-sm font-medium " + (injHigh ? "text-rose-300" : "text-slate-300")}>
                    {(inject.score * 100).toFixed(0)}%
                  </span>
                </div>
                {inject.matched.length > 0 ? (
                  <div className="mt-2 flex flex-wrap gap-1.5">
                    {inject.matched.map((m, i) => (
                      <span key={i} className="rounded bg-rose-500/15 px-1.5 py-0.5 text-[11px] text-rose-300">{m}</span>
                    ))}
                  </div>
                ) : <div className="mt-2 text-xs text-slate-500">No injection patterns matched.</div>}
              </div>
            )}
          </div>
        </div>
      </div>
    </>
  );
}
