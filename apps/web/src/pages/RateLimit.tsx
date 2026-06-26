import { useState } from "react";
import { Gauge, Send, Zap, RotateCcw } from "lucide-react";
import { api } from "../lib/api";
import { PageHeader, Stat } from "../components/Stat";

type Result = { allowed: boolean; remaining: number; retry_after_ms: number; reset_ms: number };
type Dot = { allowed: boolean };

export default function RateLimitPage() {
  const [key, setKey] = useState("user:42");
  const [windowS, setWindowS] = useState("10");
  const [max, setMax] = useState("5");
  const [last, setLast] = useState<Result | null>(null);
  const [dots, setDots] = useState<Dot[]>([]);
  const [busy, setBusy] = useState(false);

  const windowMs = () => Math.max(1, Number(windowS) || 0) * 1000;
  const maxN = () => Math.max(1, Number(max) || 0);

  const fire = async () => {
    const r = await api.rateLimit(key.trim(), windowMs(), maxN());
    setLast(r);
    setDots((d) => [{ allowed: r.allowed }, ...d].slice(0, 40));
    return r;
  };

  const burst = async () => {
    setBusy(true);
    try {
      for (let i = 0; i < 10; i++) await fire();
    } finally {
      setBusy(false);
    }
  };

  const reset = async () => {
    await api.rateLimitReset(key.trim());
    setDots([]);
    setLast(null);
  };

  const allowedCount = dots.filter((d) => d.allowed).length;

  return (
    <>
      <PageHeader
        title="Rate Limiting"
        subtitle="A GCRA limiter (smooth bursts, exact recovery rate, O(1) memory per key). Throttle by any key — user, IP, tenant, or route — and get retry hints on every decision."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Last decision" icon={Gauge}
          value={last ? (last.allowed ? "allowed" : "denied") : "—"}
          accent={last ? (last.allowed ? "emerald" : "rose") : "primary"} />
        <Stat label="Remaining" icon={Zap} value={last?.remaining ?? "—"} accent="accent" />
        <Stat label="Retry after" icon={RotateCcw}
          value={last ? `${last.retry_after_ms}ms` : "—"} accent="rose"
          hint={last && last.allowed ? "n/a while allowed" : undefined} />
        <Stat label="Allowed / shown" icon={Send}
          value={dots.length ? `${allowedCount}/${dots.length}` : "—"} accent="primary" />
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-[360px_1fr]">
        <div className="card p-4">
          <div className="mb-3 flex items-center gap-2 text-sm font-medium">
            <Gauge size={15} /> Limiter
          </div>
          <div className="space-y-3">
            <label className="block">
              <span className="text-xs text-slate-400">Key</span>
              <input className="input mt-1" value={key} onChange={(e) => setKey(e.target.value)} />
            </label>
            <div className="grid grid-cols-2 gap-2">
              <label className="block">
                <span className="text-xs text-slate-400">Window (s)</span>
                <input className="input mt-1" type="number" min={1} value={windowS}
                  onChange={(e) => setWindowS(e.target.value)} />
              </label>
              <label className="block">
                <span className="text-xs text-slate-400">Max / window</span>
                <input className="input mt-1" type="number" min={1} value={max}
                  onChange={(e) => setMax(e.target.value)} />
              </label>
            </div>
            <div className="grid grid-cols-2 gap-2">
              <button className="btn-primary" onClick={fire} disabled={busy}>
                <Send size={14} /> Send 1
              </button>
              <button className="btn-ghost" onClick={burst} disabled={busy}>
                <Zap size={14} /> Burst ×10
              </button>
            </div>
            <button className="btn-ghost w-full" onClick={reset}>
              <RotateCcw size={14} /> Reset key
            </button>
            <p className="text-xs text-slate-500">
              Allowed up to {maxN()} per {windowS}s, then denied with a retry-after until a slot recovers.
            </p>
          </div>
        </div>

        <div className="card p-4">
          <div className="mb-3 text-[11px] font-semibold uppercase tracking-wider text-slate-500">
            Recent decisions (newest first)
          </div>
          {dots.length === 0 ? (
            <div className="flex h-40 items-center justify-center text-sm text-slate-500">
              Fire some requests to see allow / deny decisions.
            </div>
          ) : (
            <div className="flex flex-wrap gap-1.5">
              {dots.map((d, i) => (
                <span key={i}
                  title={d.allowed ? "allowed" : "denied (429)"}
                  className={"h-6 w-6 rounded " + (d.allowed ? "bg-emerald-500/80" : "bg-rose-500/80")} />
              ))}
            </div>
          )}
        </div>
      </div>
    </>
  );
}
