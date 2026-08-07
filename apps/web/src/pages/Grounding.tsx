import { useEffect, useRef, useState } from "react";
import { ShieldCheck, ShieldAlert, Sparkles, FileText, Gauge, CheckCircle2, XCircle } from "lucide-react";
import { api, type VerifyResult } from "../lib/api";
import { usePolling } from "../lib/usePolling";
import { PageHeader, Stat } from "../components/Stat";

const EXAMPLE_CONTEXT = [
  "NeuroCache shards the keyspace across all available CPU cores.",
  "Replies are encoded with zero heap allocations on the hot path.",
  "It speaks the Redis RESP protocol over TCP on port 6379.",
].join("\n");

const EXAMPLE_ANSWER =
  "NeuroCache shards the keyspace across all available CPU cores. " +
  "Replies are encoded with zero heap allocations on the hot path. " +
  "It is written in Rust and ships a built-in SQL query planner.";

function pct(n: number) {
  return `${Math.round(n * 100)}%`;
}

// Support bar — green when the claim clears threshold, rose when it doesn't.
function SupportBar({ support, supported }: { support: number; supported: boolean }) {
  const w = Math.max(2, Math.round(support * 100));
  return (
    <div className="h-1.5 w-full overflow-hidden rounded-full bg-white/5">
      <div
        className={"h-full rounded-full " + (supported ? "bg-emerald-400" : "bg-rose-400")}
        style={{ width: `${w}%` }}
      />
    </div>
  );
}

export default function GroundingPage() {
  const { data: stats } = usePolling(api.groundStats, 2500);

  const [answer, setAnswer] = useState(EXAMPLE_ANSWER);
  const [context, setContext] = useState(EXAMPLE_CONTEXT);
  const [minSupport, setMinSupport] = useState(0.5);
  const [result, setResult] = useState<VerifyResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const verify = async () => {
    const chunks = context.split("\n").map((s) => s.trim()).filter(Boolean);
    if (!answer.trim()) { setErr("Answer is required."); return; }
    setBusy(true); setErr(null);
    try {
      setResult(await api.groundVerify(answer.trim(), chunks, minSupport));
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  // Run the prefilled example once on mount so the report isn't empty on first
  // visit — an instant, self-explanatory demo of grounded vs hallucinated claims.
  // The ref guard avoids a duplicate request under React 18 StrictMode in dev.
  const didAutoRun = useRef(false);
  useEffect(() => {
    if (didAutoRun.current) return;
    didAutoRun.current = true;
    verify();
    /* eslint-disable-next-line react-hooks/exhaustive-deps */
  }, []);

  return (
    <>
      <PageHeader
        title="Grounding & Verification"
        subtitle="Catch hallucinations before they ship. Score an LLM answer against the context it was given — every sentence that no source supports is flagged. RAG faithfulness and citation checking, server-side, with no extra model call."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Verifications" icon={ShieldCheck} value={stats?.total_verify ?? 0} accent="primary"
          hint="answers scored" />
        <Stat label="Mean support" icon={Gauge}
          value={result ? pct(result.mean_score) : "—"} accent="emerald"
          hint="last answer" />
        <Stat label="Flagged claims" icon={ShieldAlert}
          value={result ? result.unsupported.length : "—"} accent="rose"
          hint="last answer" />
        <Stat label="Scorer" icon={Sparkles} value={stats?.scorer ?? "cosine"} accent="accent"
          hint={stats ? `${stats.dim}-dim` : undefined} />
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-[minmax(0,440px)_1fr]">
        {/* ── input ── */}
        <div className="space-y-4">
          <div className="card p-4">
            <div className="mb-3 flex items-center gap-2 text-sm font-medium">
              <FileText size={15} /> Context sources
              <span className="ml-auto text-[11px] text-slate-500">one chunk per line</span>
            </div>
            <textarea
              className="input min-h-[120px] w-full resize-y font-mono text-[12px] leading-relaxed"
              value={context}
              onChange={(e) => setContext(e.target.value)}
              placeholder={"Retrieved chunk 1\nRetrieved chunk 2"}
            />
            <div className="mt-4 mb-2 flex items-center gap-2 text-sm font-medium">
              <Sparkles size={15} /> LLM answer
            </div>
            <textarea
              className="input min-h-[120px] w-full resize-y text-[13px] leading-relaxed"
              value={answer}
              onChange={(e) => setAnswer(e.target.value)}
              placeholder="The answer your model produced…"
            />

            <div className="mt-4">
              <div className="flex items-center justify-between text-xs text-slate-400">
                <span>Min support threshold</span>
                <span className="font-mono text-slate-200">{minSupport.toFixed(2)}</span>
              </div>
              <input type="range" min={0} max={1} step={0.05} value={minSupport}
                onChange={(e) => setMinSupport(Number(e.target.value))}
                className="mt-1 w-full accent-primary" />
            </div>

            <div className="mt-4 flex gap-2">
              <button className="btn-primary flex-1" onClick={verify} disabled={busy}>
                <ShieldCheck size={14} /> {busy ? "Verifying…" : "Verify answer"}
              </button>
              <button className="btn-ghost"
                onClick={() => { setAnswer(EXAMPLE_ANSWER); setContext(EXAMPLE_CONTEXT); setResult(null); }}>
                Reset example
              </button>
            </div>
            {err && <div className="mt-2 text-xs text-rose-400">{err}</div>}
          </div>
        </div>

        {/* ── result ── */}
        <div className="card flex flex-col p-0">
          <div className="flex items-center gap-2 border-b border-border px-4 py-2.5 text-sm font-medium">
            <Gauge size={15} /> Groundedness report
          </div>

          {!result ? (
            <div className="flex h-64 flex-col items-center justify-center gap-2 text-sm text-slate-500">
              <ShieldCheck size={26} className="text-slate-600" />
              Verify an answer to see its per-claim grounding.
            </div>
          ) : (
            <div className="space-y-4 p-4">
              {/* verdict banner */}
              <div className={"flex items-center gap-3 rounded-lg border px-4 py-3 " +
                (result.grounded
                  ? "border-emerald-400/30 bg-emerald-500/10"
                  : "border-rose-400/30 bg-rose-500/10")}>
                {result.grounded
                  ? <CheckCircle2 className="text-emerald-400" size={22} />
                  : <XCircle className="text-rose-400" size={22} />}
                <div>
                  <div className={"text-sm font-semibold " + (result.grounded ? "text-emerald-300" : "text-rose-300")}>
                    {result.grounded ? "Grounded" : "Ungrounded — possible hallucination"}
                  </div>
                  <div className="text-xs text-slate-400">
                    {result.grounded
                      ? "Every claim is backed by a source."
                      : `${result.unsupported.length} claim(s) not supported by any source.`}
                  </div>
                </div>
                <div className="ml-auto text-right">
                  <div className="font-mono text-lg tabular-nums text-slate-100">{pct(result.doc_score)}</div>
                  <div className="text-[10px] uppercase tracking-wider text-slate-500">doc score</div>
                </div>
              </div>

              <div className="grid grid-cols-3 gap-2 text-center">
                <div className="rounded-md border border-border bg-white/[0.02] px-2 py-2">
                  <div className="font-mono text-sm text-slate-100">{pct(result.mean_score)}</div>
                  <div className="text-[10px] uppercase tracking-wide text-slate-500">mean support</div>
                </div>
                <div className="rounded-md border border-border bg-white/[0.02] px-2 py-2">
                  <div className="font-mono text-sm text-slate-100">{pct(result.min_support)}</div>
                  <div className="text-[10px] uppercase tracking-wide text-slate-500">threshold</div>
                </div>
                <div className="rounded-md border border-border bg-white/[0.02] px-2 py-2">
                  <div className="font-mono text-sm text-slate-100">
                    {result.sentences.filter((s) => s.supported).length}/{result.sentences.length}
                  </div>
                  <div className="text-[10px] uppercase tracking-wide text-slate-500">claims ok</div>
                </div>
              </div>

              {/* per-claim breakdown */}
              <div className="space-y-2">
                {result.sentences.map((s, i) => (
                  <div key={i}
                    className={"rounded-lg border p-3 " +
                      (s.supported ? "border-border bg-white/[0.02]" : "border-rose-400/25 bg-rose-500/[0.06]")}>
                    <div className="flex items-start gap-2">
                      {s.supported
                        ? <CheckCircle2 size={15} className="mt-0.5 shrink-0 text-emerald-400" />
                        : <ShieldAlert size={15} className="mt-0.5 shrink-0 text-rose-400" />}
                      <span className="text-[13px] leading-relaxed text-slate-200">{s.sentence}</span>
                      <span className="ml-auto shrink-0 font-mono text-xs tabular-nums text-slate-400">
                        {pct(s.support)}
                      </span>
                    </div>
                    <div className="mt-2 flex items-center gap-2">
                      <SupportBar support={s.support} supported={s.supported} />
                      <span className="shrink-0 text-[10px] text-slate-500">
                        {s.best_chunk >= 0 ? `src #${s.best_chunk + 1}` : "no source"}
                      </span>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>
    </>
  );
}
