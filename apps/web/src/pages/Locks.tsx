import { useState } from "react";
import {
  Lock, KeyRound, Hash, Plus, RefreshCw, Unlock, ScanSearch, Clock,
} from "lucide-react";
import { api } from "../lib/api";
import { usePolling } from "../lib/usePolling";
import { PageHeader, Stat } from "../components/Stat";

function randomOwner() {
  return "owner-" + Math.random().toString(36).slice(2, 8);
}
function secs(ms: number) {
  return Math.max(0, Math.ceil(ms / 1000)) + "s";
}

export default function LocksPage() {
  const { data: lockData } = usePolling(api.lockList, 1000);
  const locks = lockData?.locks ?? [];
  const maxToken = locks.reduce((m, l) => Math.max(m, l.token), 0);

  const [name, setName] = useState("job:reindex");
  const [owner, setOwner] = useState(randomOwner);
  const [ttl, setTtl] = useState("30");
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null);

  const ttlMs = () => Math.max(1, Number(ttl) || 0) * 1000;

  const run = async (
    label: string,
    fn: () => Promise<{ ok: boolean; text: string }>,
  ) => {
    if (!name.trim() || !owner.trim()) {
      setResult({ ok: false, text: "name and owner are required" });
      return;
    }
    try {
      setResult(await fn());
    } catch (e) {
      setResult({ ok: false, text: `${label}: ${(e as Error).message}` });
    }
  };

  const acquire = () =>
    run("acquire", async () => {
      const r = await api.lockAcquire(name.trim(), owner.trim(), ttlMs());
      return r.acquired
        ? { ok: true, text: `Acquired — fencing token #${r.token}` }
        : { ok: false, text: "Denied — held by another owner" };
    });
  const extend = () =>
    run("extend", async () => {
      const r = await api.lockExtend(name.trim(), owner.trim(), ttlMs());
      return { ok: r.extended, text: r.extended ? "Lease extended" : "Not extended — you don't hold it" };
    });
  const release = () =>
    run("release", async () => {
      const r = await api.lockRelease(name.trim(), owner.trim());
      return { ok: r.released, text: r.released ? "Released" : "Not released — you don't hold it" };
    });
  const check = () =>
    run("check", async () => {
      const r = await api.lockCheck(name.trim());
      return r.held
        ? { ok: true, text: `Held by ${r.owner} · token #${r.token} · expires in ${secs(r.remaining_ms ?? 0)}` }
        : { ok: true, text: "Free — not currently held" };
    });

  const forceRelease = async (lockName: string, lockOwner: string) => {
    try { await api.lockRelease(lockName, lockOwner); } catch { /* poll reconciles */ }
  };

  return (
    <>
      <PageHeader
        title="Distributed Locks"
        subtitle="Lease-based locks with monotonic fencing tokens — the safety mechanism plain SETNX locks lack. Acquire returns a strictly increasing token so downstream services can fence stale holders."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Held locks" icon={Lock} value={locks.length} accent="primary" />
        <Stat label="Highest token" icon={Hash} value={maxToken} accent="accent"
          hint="monotonic fencing token" />
        <Stat label="Owners" icon={KeyRound} value={new Set(locks.map((l) => l.owner)).size} accent="emerald" />
        <Stat label="Soonest expiry" icon={Clock}
          value={locks.length ? secs(Math.min(...locks.map((l) => l.remaining_ms))) : "—"} accent="rose" />
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-[360px_1fr]">
        {/* ── Acquire / manage console ── */}
        <div className="card p-4">
          <div className="mb-3 flex items-center gap-2 text-sm font-medium">
            <KeyRound size={15} /> Acquire / manage
          </div>
          <div className="space-y-3">
            <label className="block">
              <span className="text-xs text-slate-400">Lock name</span>
              <input className="input mt-1" value={name} onChange={(e) => setName(e.target.value)}
                placeholder="job:reindex" />
            </label>
            <label className="block">
              <span className="text-xs text-slate-400">Owner</span>
              <div className="mt-1 flex gap-2">
                <input className="input" value={owner} onChange={(e) => setOwner(e.target.value)} />
                <button className="btn-ghost px-2" title="Randomize owner" onClick={() => setOwner(randomOwner())}>
                  <RefreshCw size={14} />
                </button>
              </div>
            </label>
            <label className="block">
              <span className="text-xs text-slate-400">Lease TTL (seconds)</span>
              <input className="input mt-1" type="number" min={1} value={ttl}
                onChange={(e) => setTtl(e.target.value)} />
            </label>
            <div className="grid grid-cols-2 gap-2">
              <button className="btn-primary" onClick={acquire}><Plus size={14} /> Acquire</button>
              <button className="btn-ghost" onClick={extend}><RefreshCw size={14} /> Extend</button>
              <button className="btn-ghost" onClick={release}><Unlock size={14} /> Release</button>
              <button className="btn-ghost" onClick={check}><ScanSearch size={14} /> Check</button>
            </div>
            {result ? (
              <div className={"rounded-md border px-3 py-2 text-sm " +
                (result.ok
                  ? "border-emerald-400/30 bg-emerald-500/10 text-emerald-300"
                  : "border-rose-400/30 bg-rose-500/10 text-rose-300")}>
                {result.text}
              </div>
            ) : null}
          </div>
        </div>

        {/* ── Live held-locks table ── */}
        <div className="card flex flex-col p-0">
          <div className="flex items-center gap-2 border-b border-border px-4 py-2.5 text-sm font-medium">
            <Lock size={15} /> Held locks
            <span className="ml-auto text-xs text-slate-500">refreshes every 1s</span>
          </div>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="bg-surface text-left text-xs uppercase tracking-wider text-slate-500">
                <tr>
                  <th className="px-4 py-2">Lock</th>
                  <th className="px-4 py-2">Owner</th>
                  <th className="px-4 py-2 text-right">Token</th>
                  <th className="px-4 py-2 text-right">Expires</th>
                  <th className="px-4 py-2 text-right">Action</th>
                </tr>
              </thead>
              <tbody>
                {locks.length === 0 ? (
                  <tr><td colSpan={5} className="px-4 py-10 text-center text-slate-500">
                    No locks held. Acquire one on the left.
                  </td></tr>
                ) : (
                  locks.map((l) => (
                    <tr key={l.name} className="border-t border-border">
                      <td className="px-4 py-2 font-mono text-[13px] text-slate-200">{l.name}</td>
                      <td className="px-4 py-2 font-mono text-[13px] text-slate-400">{l.owner}</td>
                      <td className="px-4 py-2 text-right">
                        <span className="rounded bg-accent/15 px-1.5 py-0.5 font-mono text-[12px] text-accent">#{l.token}</span>
                      </td>
                      <td className="px-4 py-2 text-right tabular-nums text-slate-300">{secs(l.remaining_ms)}</td>
                      <td className="px-4 py-2 text-right">
                        <button
                          onClick={() => forceRelease(l.name, l.owner)}
                          className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-slate-400 hover:bg-white/5 hover:text-rose-300"
                          title="Force-release this lock"
                        >
                          <Unlock size={13} /> Release
                        </button>
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </>
  );
}
