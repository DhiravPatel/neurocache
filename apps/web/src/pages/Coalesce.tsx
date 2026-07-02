import { useEffect, useRef, useState } from "react";
import {
  GitMerge,
  Users,
  Zap,
  TrendingDown,
  Crown,
  Clock,
  CheckCircle2,
  Play,
  Trash2,
} from "lucide-react";
import { api, type CoalesceStatus } from "../lib/api";
import { usePolling } from "../lib/usePolling";
import { PageHeader, Stat } from "../components/Stat";

function pct(n: number) {
  return `${Math.round(n * 100)}%`;
}

// One participant tile in the herd visualization.
function Caller({ role, label }: { role: "leader" | "follower"; label: string }) {
  const leader = role === "leader";
  return (
    <div
      className={
        "flex items-center gap-2 rounded-lg border px-3 py-2 text-xs " +
        (leader
          ? "border-emerald-400/30 bg-emerald-500/10 text-emerald-200"
          : "border-amber-400/25 bg-amber-500/[0.07] text-amber-200")
      }
    >
      {leader ? <Crown size={14} className="shrink-0" /> : <Clock size={14} className="shrink-0" />}
      <span className="truncate">{label}</span>
    </div>
  );
}

export default function CoalescePage() {
  const { data: stats } = usePolling(api.coalesceStats, 2000);
  const { data: keysResp } = usePolling(api.coalesceKeys, 2000);

  const [key, setKey] = useState("llm:answer:latest-news");
  const [followers, setFollowers] = useState(5);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  // Demo outcome: how many followers were coalesced onto the one leader call.
  const [demo, setDemo] = useState<{ saved: number; result: string } | null>(null);
  const [status, setStatus] = useState<CoalesceStatus | null>(null);

  // Simulate a burst of identical concurrent misses. The first caller wins
  // the lock (does the "upstream call"); the rest contend and would WAIT.
  // Publishing hands them all the single shared result — every contended
  // caller is one upstream call we did NOT make.
  const runHerd = async () => {
    setBusy(true);
    setErr(null);
    setDemo(null);
    try {
      await api.coalesceForget(key).catch(() => {});
      const lead = await api.coalesceLock(key, 10000);
      if (!lead.owner) {
        // Someone already owns it — still a valid (contended) outcome.
        setErr("Key already in-flight — try a fresh key.");
        setBusy(false);
        return;
      }
      let saved = 0;
      for (let i = 0; i < followers; i++) {
        const f = await api.coalesceLock(key, 10000);
        if (!f.owner) saved++;
      }
      const answer = `Shared answer computed once at ${new Date().toLocaleTimeString()}`;
      await api.coalescePublish(key, lead.token, answer);
      setDemo({ saved, result: answer });
      const st = await api.coalesceStatus(key);
      setStatus(st.exists ? st.status ?? null : null);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const refreshStatus = async () => {
    const st = await api.coalesceStatus(key);
    setStatus(st.exists ? st.status ?? null : null);
    if (!st.exists) setErr("No in-flight entry for that key.");
  };

  const forget = async () => {
    await api.coalesceForget(key);
    setStatus(null);
    setDemo(null);
  };

  // Auto-run the demo once so the page tells its story on first visit.
  const didAutoRun = useRef(false);
  useEffect(() => {
    if (didAutoRun.current) return;
    didAutoRun.current = true;
    runHerd();
    /* eslint-disable-next-line react-hooks/exhaustive-deps */
  }, []);

  const activeKeys = keysResp?.keys ?? [];

  return (
    <>
      <PageHeader
        title="Request Coalescing"
        subtitle="Single-flight protection against thundering herds. When a burst of identical requests all miss at once, the first caller does the one upstream LLM call and everyone else is handed the same answer — so you pay for one, not a hundred."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat
          label="Save rate"
          icon={TrendingDown}
          value={stats ? pct(stats.save_rate) : "—"}
          accent="emerald"
          hint="calls deduplicated"
        />
        <Stat
          label="Active herds"
          icon={Users}
          value={stats?.active ?? 0}
          accent="primary"
          hint="keys in-flight"
        />
        <Stat
          label="Calls saved"
          icon={Zap}
          value={stats?.total_contended ?? 0}
          accent="accent"
          hint="contended locks"
        />
        <Stat
          label="Published"
          icon={CheckCircle2}
          value={stats?.total_publishes ?? 0}
          accent="primary"
          hint="results fanned out"
        />
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-[minmax(0,420px)_1fr]">
        {/* ── controls ── */}
        <div className="space-y-4">
          <div className="card p-4">
            <div className="mb-3 flex items-center gap-2 text-sm font-medium">
              <GitMerge size={15} /> Simulate a herd
            </div>

            <label className="text-xs text-slate-400">Cache key</label>
            <input
              className="input mt-1 w-full font-mono text-[12px]"
              value={key}
              onChange={(e) => setKey(e.target.value)}
              placeholder="llm:answer:some-question"
            />

            <div className="mt-4">
              <div className="flex items-center justify-between text-xs text-slate-400">
                <span>Concurrent callers (followers)</span>
                <span className="font-mono text-slate-200">{followers}</span>
              </div>
              <input
                type="range"
                min={1}
                max={50}
                step={1}
                value={followers}
                onChange={(e) => setFollowers(Number(e.target.value))}
                className="mt-1 w-full accent-primary"
              />
            </div>

            <div className="mt-4 flex flex-wrap gap-2">
              <button className="btn-primary flex-1" onClick={runHerd} disabled={busy}>
                <Play size={14} /> {busy ? "Running…" : "Run herd"}
              </button>
              <button className="btn-ghost" onClick={refreshStatus} disabled={busy}>
                Status
              </button>
              <button className="btn-ghost" onClick={forget} disabled={busy}>
                <Trash2 size={13} /> Forget
              </button>
            </div>
            {err && <div className="mt-2 text-xs text-rose-400">{err}</div>}

            {status && (
              <div className="mt-4 rounded-lg border border-border bg-white/[0.02] p-3 text-xs">
                <div className="mb-1 font-mono text-slate-300">{status.key}</div>
                <div className="flex flex-wrap gap-3 text-slate-400">
                  <span>
                    state:{" "}
                    <span
                      className={
                        status.state === "published"
                          ? "text-emerald-300"
                          : status.state === "stale"
                            ? "text-rose-300"
                            : "text-amber-300"
                      }
                    >
                      {status.state}
                    </span>
                  </span>
                  <span>timeout: {status.timeout_ms}ms</span>
                  <span>result: {status.has_result ? "ready" : "pending"}</span>
                </div>
              </div>
            )}
          </div>

          {/* active herds */}
          <div className="card p-4">
            <div className="mb-3 flex items-center gap-2 text-sm font-medium">
              <Users size={15} /> Active herds
              <span className="ml-auto text-[11px] text-slate-500">{activeKeys.length}</span>
            </div>
            {activeKeys.length === 0 ? (
              <div className="py-4 text-center text-xs text-slate-500">No keys in-flight.</div>
            ) : (
              <div className="flex flex-wrap gap-1.5">
                {activeKeys.map((k) => (
                  <span
                    key={k}
                    className="rounded-md border border-border bg-white/[0.03] px-2 py-1 font-mono text-[11px] text-slate-300"
                  >
                    {k}
                  </span>
                ))}
              </div>
            )}
          </div>
        </div>

        {/* ── herd visualization ── */}
        <div className="card flex flex-col p-0">
          <div className="flex items-center gap-2 border-b border-border px-4 py-2.5 text-sm font-medium">
            <GitMerge size={15} /> What the coalescer did
          </div>

          {!demo ? (
            <div className="flex h-64 flex-col items-center justify-center gap-2 text-sm text-slate-500">
              <GitMerge size={26} className="text-slate-600" />
              Run a herd to see the fan-in.
            </div>
          ) : (
            <div className="space-y-4 p-4">
              <div className="flex items-center gap-3 rounded-lg border border-emerald-400/30 bg-emerald-500/10 px-4 py-3">
                <TrendingDown className="text-emerald-400" size={22} />
                <div>
                  <div className="text-sm font-semibold text-emerald-300">
                    Saved {demo.saved} upstream call{demo.saved === 1 ? "" : "s"}
                  </div>
                  <div className="text-xs text-slate-400">
                    {demo.saved + 1} callers, 1 real upstream request — the rest were served the
                    leader's result.
                  </div>
                </div>
                <div className="ml-auto text-right">
                  <div className="font-mono text-lg tabular-nums text-slate-100">
                    {demo.saved + 1}→1
                  </div>
                  <div className="text-[10px] uppercase tracking-wider text-slate-500">fan-in</div>
                </div>
              </div>

              <div>
                <div className="mb-2 text-xs font-medium text-slate-400">
                  The one caller that did the work
                </div>
                <Caller role="leader" label="Leader — ran the upstream LLM call, published the answer" />
              </div>

              <div>
                <div className="mb-2 text-xs font-medium text-slate-400">
                  Coalesced onto the leader ({demo.saved})
                </div>
                <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
                  {Array.from({ length: demo.saved }).map((_, i) => (
                    <Caller key={i} role="follower" label={`Follower #${i + 1} — waited, no call`} />
                  ))}
                </div>
              </div>

              <div className="rounded-lg border border-border bg-white/[0.02] p-3">
                <div className="mb-1 text-[10px] uppercase tracking-wide text-slate-500">
                  Published result
                </div>
                <div className="font-mono text-[12px] text-slate-300">{demo.result}</div>
              </div>
            </div>
          )}
        </div>
      </div>
    </>
  );
}
