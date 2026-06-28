import { useState } from "react";
import { Clock, Plus, X, Timer, CalendarClock } from "lucide-react";
import { api, type ScheduledTask } from "../lib/api";
import { usePolling } from "../lib/usePolling";
import { PageHeader, Stat } from "../components/Stat";

function fmtIn(fireAt: string) {
  const ms = new Date(fireAt).getTime() - Date.now();
  if (ms <= 0) return "due";
  if (ms < 60000) return `${Math.ceil(ms / 1000)}s`;
  if (ms < 3600000) return `${Math.ceil(ms / 60000)}m`;
  return `${(ms / 3600000).toFixed(1)}h`;
}

export default function SchedulerPage() {
  const { data, error } = usePolling(api.scheduleList, 1000);
  const { data: stats } = usePolling(api.scheduleStats, 2500);
  const tasks: ScheduledTask[] = data?.tasks ?? [];

  const [cmd, setCmd] = useState("PING");
  const [args, setArgs] = useState("");
  const [delay, setDelay] = useState("30");
  const [msg, setMsg] = useState<string | null>(null);

  const schedule = async () => {
    if (!cmd.trim()) { setMsg("command required"); return; }
    const argList = args.split(/\s+/).map((s) => s.trim()).filter(Boolean);
    const r = await api.scheduleIn(Math.max(1, Number(delay) || 0) * 1000, cmd.trim().toUpperCase(), argList);
    setMsg(`Scheduled task #${r.id} — ${cmd.toUpperCase()} in ${delay}s`);
  };
  const cancel = async (id: number) => { try { await api.scheduleCancel(id); } catch { /* poll reconciles */ } };

  const soonest = tasks.length ? Math.min(...tasks.map((t) => new Date(t.fire_at).getTime())) : 0;

  return (
    <>
      <PageHeader
        title="Scheduler"
        subtitle="Run any command at a future time or after a delay — server-side, durable, and replicated. Schedule cache warms, cleanups, or reminders without an external cron or job runner."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Pending tasks" icon={Clock} value={tasks.length} accent="primary" />
        <Stat label="Next fires in" icon={Timer}
          value={soonest ? fmtIn(new Date(soonest).toISOString()) : "—"} accent="accent" />
        <Stat label="Scheduled (total)" icon={CalendarClock} value={stats?.total_scheduled ?? 0} accent="emerald" />
        <Stat label="Completed" icon={CalendarClock}
          value={Math.max(0, (stats?.total_scheduled ?? 0) - (stats?.pending ?? tasks.length))} accent="emerald"
          hint="fired or cancelled" />
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-[340px_1fr]">
        <div className="card p-4">
          <div className="mb-3 flex items-center gap-2 text-sm font-medium"><Plus size={15} /> Schedule a command</div>
          <div className="space-y-2">
            <label className="block">
              <span className="text-xs text-slate-400">Command</span>
              <input className="input mt-1 font-mono" value={cmd} onChange={(e) => setCmd(e.target.value)} placeholder="SET" />
            </label>
            <label className="block">
              <span className="text-xs text-slate-400">Arguments (space-separated)</span>
              <input className="input mt-1 font-mono" value={args} onChange={(e) => setArgs(e.target.value)} placeholder="key value" />
            </label>
            <label className="block">
              <span className="text-xs text-slate-400">Run in (seconds)</span>
              <input className="input mt-1" type="number" min={1} value={delay} onChange={(e) => setDelay(e.target.value)} />
            </label>
            <button className="btn-primary w-full" onClick={schedule}><Clock size={14} /> Schedule</button>
          </div>
          {msg && <div className="mt-2 text-xs text-slate-400">{msg}</div>}
        </div>

        <div className="card flex flex-col p-0">
          <div className="flex items-center gap-2 border-b border-border px-4 py-2.5 text-sm font-medium">
            <Clock size={15} /> Pending tasks
            <span className="ml-auto text-xs text-slate-500">refreshes every 1s</span>
          </div>
          {error ? (
            <div className="flex h-40 items-center justify-center text-sm text-rose-400">Failed to load tasks.</div>
          ) : tasks.length === 0 ? (
            <div className="flex h-40 items-center justify-center text-sm text-slate-500">
              No pending tasks — schedule one on the left.
            </div>
          ) : (
            <table className="w-full text-sm">
              <thead className="bg-surface text-left text-xs uppercase tracking-wider text-slate-500">
                <tr>
                  <th className="px-4 py-2">#</th>
                  <th className="px-4 py-2">Command</th>
                  <th className="px-4 py-2 text-right">Fires in</th>
                  <th className="px-4 py-2 text-right">Action</th>
                </tr>
              </thead>
              <tbody>
                {tasks.map((t) => (
                  <tr key={t.id} className="border-t border-border">
                    <td className="px-4 py-2 font-mono text-[12px] text-slate-500">{t.id}</td>
                    <td className="px-4 py-2 font-mono text-[13px] text-slate-200">
                      {t.cmd}{t.args?.length ? <span className="text-slate-400"> {t.args.join(" ")}</span> : null}
                    </td>
                    <td className="px-4 py-2 text-right tabular-nums text-accent">{fmtIn(t.fire_at)}</td>
                    <td className="px-4 py-2 text-right">
                      <button onClick={() => cancel(t.id)}
                        className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-slate-400 hover:bg-white/5 hover:text-rose-300">
                        <X size={13} /> Cancel
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </>
  );
}
