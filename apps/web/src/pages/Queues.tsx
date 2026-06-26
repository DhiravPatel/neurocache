import { useState } from "react";
import { ListChecks, Plus, Play, AlertTriangle, RotateCcw, Layers } from "lucide-react";
import { api, type QueueJob } from "../lib/api";
import { usePolling } from "../lib/usePolling";
import { PageHeader, Stat } from "../components/Stat";

export default function QueuesPage() {
  const [queue, setQueue] = useState("emails");
  const [payload, setPayload] = useState("");
  const [priority, setPriority] = useState("0");
  const [msg, setMsg] = useState<string | null>(null);

  const q = queue.trim() || "emails";
  const { data: stats } = usePolling(() => api.queueStats(q).catch(() => null), 1000);
  const { data: dlqData } = usePolling(() => api.queueDLQ(q).catch(() => null), 2000);
  const { data: list } = usePolling(api.queueList, 2000);
  const dlq: QueueJob[] = dlqData?.jobs ?? [];

  const enqueue = async () => {
    if (!payload.trim()) { setMsg("payload required"); return; }
    try { const r = await api.queueEnqueue(q, payload.trim(), Number(priority) || 0); setMsg(`Enqueued job #${r.id}`); setPayload(""); }
    catch (e) { setMsg((e as Error).message); }
  };
  // "Process one" = reserve + ack, to demonstrate the consumer path.
  const processOne = async () => {
    try {
      const { job } = await api.queueDequeue(q);
      if (!job) { setMsg("Queue empty"); return; }
      await api.queueAck(q, job.id);
      setMsg(`Processed job #${job.id}: ${job.payload}`);
    } catch (e) { setMsg((e as Error).message); }
  };
  const requeue = async (id: number) => { try { await api.queueRequeue(q, id); } catch { /* poll */ } };

  return (
    <>
      <PageHeader
        title="Queues"
        subtitle="A durable job queue: priorities, retries with backoff, a visibility timeout for at-least-once delivery, idempotency keys, and a dead-letter queue."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Pending" icon={Layers} value={stats?.pending ?? 0} accent="primary" hint="waiting to be reserved" />
        <Stat label="In flight" icon={Play} value={stats?.reserved ?? 0} accent="accent" hint="reserved by a worker" />
        <Stat label="Dead-letter" icon={AlertTriangle} value={stats?.dlq ?? 0} accent="rose" hint={`max ${stats?.max_attempts ?? 5} attempts`} />
        <Stat label="Queues" icon={ListChecks} value={list?.queues?.length ?? 0} accent="emerald" />
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-[360px_1fr]">
        <div className="space-y-4">
          <div className="card p-4">
            <div className="mb-3 flex items-center gap-2 text-sm font-medium">
              <Plus size={15} /> Enqueue a job
            </div>
            <div className="space-y-3">
              <label className="block">
                <span className="text-xs text-slate-400">Queue</span>
                <input className="input mt-1" value={queue} onChange={(e) => setQueue(e.target.value)} />
              </label>
              <label className="block">
                <span className="text-xs text-slate-400">Payload</span>
                <textarea className="input mt-1 min-h-20" value={payload} onChange={(e) => setPayload(e.target.value)}
                  placeholder='{"to":"a@b.com"}' />
              </label>
              <label className="block">
                <span className="text-xs text-slate-400">Priority (higher first)</span>
                <input className="input mt-1" type="number" value={priority} onChange={(e) => setPriority(e.target.value)} />
              </label>
              <div className="grid grid-cols-2 gap-2">
                <button className="btn-primary" onClick={enqueue}><Plus size={14} /> Enqueue</button>
                <button className="btn-ghost" onClick={processOne}><Play size={14} /> Process one</button>
              </div>
              {msg ? <div className="text-xs text-slate-400">{msg}</div> : null}
            </div>
          </div>

          {list?.queues?.length ? (
            <div className="card p-4">
              <div className="mb-2 text-[11px] font-semibold uppercase tracking-wider text-slate-500">Active queues</div>
              <div className="flex flex-wrap gap-2">
                {list.queues.map((name) => (
                  <button key={name} onClick={() => setQueue(name)}
                    className={"rounded-full border px-2.5 py-1 text-xs " +
                      (name === q ? "border-primary/50 bg-primary/15 text-primary" : "border-border text-slate-400 hover:text-slate-200")}>
                    {name}
                  </button>
                ))}
              </div>
            </div>
          ) : null}
        </div>

        <div className="card flex flex-col p-0">
          <div className="flex items-center gap-2 border-b border-border px-4 py-2.5 text-sm font-medium">
            <AlertTriangle size={15} className="text-rose-400" /> Dead-letter queue · {q}
            <span className="ml-auto text-xs text-slate-500">jobs that exhausted their retries</span>
          </div>
          {dlq.length === 0 ? (
            <div className="flex h-40 items-center justify-center text-sm text-slate-500">
              No dead-lettered jobs. 🎉
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead className="bg-surface text-left text-xs uppercase tracking-wider text-slate-500">
                  <tr>
                    <th className="px-4 py-2">Job</th>
                    <th className="px-4 py-2">Payload</th>
                    <th className="px-4 py-2 text-right">Attempts</th>
                    <th className="px-4 py-2">Last error</th>
                    <th className="px-4 py-2 text-right">Action</th>
                  </tr>
                </thead>
                <tbody>
                  {dlq.map((j) => (
                    <tr key={j.id} className="border-t border-border">
                      <td className="px-4 py-2 font-mono text-[12px] text-slate-400">#{j.id}</td>
                      <td className="px-4 py-2 font-mono text-[12px] text-slate-200 max-w-[16rem] truncate">{j.payload}</td>
                      <td className="px-4 py-2 text-right tabular-nums text-slate-400">{j.attempts}</td>
                      <td className="px-4 py-2 text-[12px] text-rose-300 max-w-[12rem] truncate">{j.last_error || "—"}</td>
                      <td className="px-4 py-2 text-right">
                        <button onClick={() => requeue(j.id)}
                          className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-slate-400 hover:bg-white/5 hover:text-emerald-300">
                          <RotateCcw size={13} /> Requeue
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </div>
    </>
  );
}
