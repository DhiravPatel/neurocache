import { useEffect, useState } from "react";
import { Logs, Plus, Sigma, Play } from "lucide-react";
import { api } from "../lib/api";
import { usePolling } from "../lib/usePolling";
import { PageHeader, Stat } from "../components/Stat";

const REDUCERS = ["count", "sum", "avg", "min", "max", "last"];

export default function EventsPage() {
  const [stream, setStream] = useState("orders");
  const s = stream.trim() || "orders";

  const { data: lenData } = usePolling(() => api.eventLen(s).catch(() => null), 1500);
  const { data: rangeData } = usePolling(() => api.eventRange(s, 0, 0).catch(() => null), 1500);
  const events = (rangeData?.events ?? []) as unknown[];

  const [payload, setPayload] = useState('{ "type": "placed", "amount": 50 }');
  const [projName, setProjName] = useState("revenue");
  const [reducer, setReducer] = useState("sum");
  const [field, setField] = useState("amount");
  const [projValue, setProjValue] = useState<unknown>(null);
  const [msg, setMsg] = useState<string | null>(null);

  const append = async () => {
    let obj: unknown;
    try { obj = JSON.parse(payload); } catch { setMsg("payload must be valid JSON"); return; }
    const r = await api.eventAppend(s, obj);
    setMsg(`Appended event #${r.seq}`);
  };
  const project = async () => {
    if (!projName.trim()) { setMsg("projection name required"); return; }
    await api.eventProject(s, projName.trim(), reducer, field.trim() || undefined);
    setMsg(`Projection "${projName.trim()}" (${reducer}${field ? ` of ${field}` : ""}) registered`);
    readProj();
  };
  const readProj = async () => {
    try { setProjValue((await api.eventProjection(s, projName.trim())).projection); }
    catch { setProjValue(null); }
  };
  useEffect(() => { if (projName.trim()) readProj(); /* eslint-disable-next-line */ }, [lenData?.len, projName]);

  return (
    <>
      <PageHeader
        title="Event Sourcing"
        subtitle="An append-only event log with continuously-maintained projections. Append domain events, register a reducer (sum, count, last…), and read the live aggregate — no batch job to recompute."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Stream length" icon={Logs} value={lenData?.len ?? 0} accent="primary" hint={s} />
        <Stat label="Projection" icon={Sigma} value={projName || "—"} accent="accent" />
        <Stat label="Reducer" icon={Sigma} value={reducer} accent="emerald" hint={field ? `of ${field}` : undefined} />
        <Stat label="Live value" icon={Play}
          value={projValue === null ? "—" : (typeof projValue === "object" ? "obj" : String(projValue))} accent="emerald" />
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-[360px_1fr]">
        <div className="space-y-4">
          <div className="card p-4">
            <div className="mb-3 flex items-center gap-2 text-sm font-medium"><Plus size={15} /> Append event</div>
            <label className="block">
              <span className="text-xs text-slate-400">Stream</span>
              <input className="input mt-1" value={stream} onChange={(e) => setStream(e.target.value)} />
            </label>
            <textarea className="input mt-2 min-h-24 font-mono text-[13px]" value={payload}
              onChange={(e) => setPayload(e.target.value)} />
            <button className="btn-primary mt-2 w-full" onClick={append}><Plus size={14} /> Append</button>
          </div>

          <div className="card p-4">
            <div className="mb-3 flex items-center gap-2 text-sm font-medium"><Sigma size={15} /> Projection</div>
            <div className="space-y-2">
              <input className="input" value={projName} onChange={(e) => setProjName(e.target.value)} placeholder="projection name" />
              <div className="grid grid-cols-2 gap-2">
                <select className="input" value={reducer} onChange={(e) => setReducer(e.target.value)}>
                  {REDUCERS.map((r) => <option key={r} value={r}>{r}</option>)}
                </select>
                <input className="input font-mono" value={field} onChange={(e) => setField(e.target.value)} placeholder="field (e.g. amount)" />
              </div>
              <button className="btn-ghost w-full" onClick={project}><Play size={14} /> Register / refresh</button>
            </div>
            {projValue !== null && (
              <div className="mt-3 rounded-md border border-border bg-bg/40 p-3 text-center">
                <div className="text-[11px] uppercase tracking-wider text-slate-500">{projName} = </div>
                <div className="mt-1 font-mono text-lg text-emerald-300">{typeof projValue === "object" ? JSON.stringify(projValue) : String(projValue)}</div>
              </div>
            )}
            {msg && <div className="mt-2 text-xs text-slate-400">{msg}</div>}
          </div>
        </div>

        <div className="card flex flex-col p-0">
          <div className="flex items-center gap-2 border-b border-border px-4 py-2.5 text-sm font-medium">
            <Logs size={15} /> {s} · events
            <span className="ml-auto text-xs text-slate-500">{lenData?.len ?? 0} total</span>
          </div>
          <div className="max-h-[60vh] overflow-y-auto p-3 font-mono text-[12px]">
            {events.length === 0 ? (
              <div className="flex h-40 items-center justify-center text-sm text-slate-500">
                No events — append one on the left.
              </div>
            ) : (
              <ul className="space-y-1">
                {events.slice().reverse().map((e, i) => (
                  <li key={i} className="rounded px-2 py-1 text-slate-300 hover:bg-white/5">
                    {typeof e === "object" ? JSON.stringify(e) : String(e)}
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>
      </div>
    </>
  );
}
