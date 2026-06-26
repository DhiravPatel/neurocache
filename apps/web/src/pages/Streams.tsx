import { useEffect, useRef, useState } from "react";
import { Waves, Plus, Radio, WifiOff, Hash } from "lucide-react";
import { api } from "../lib/api";
import { usePolling } from "../lib/usePolling";
import { PageHeader, Stat } from "../components/Stat";

type Entry = { id: string; fields: Record<string, string> };

export default function StreamsPage() {
  const [key, setKey] = useState("events");
  const [field, setField] = useState("type");
  const [value, setValue] = useState("");
  const [tailing, setTailing] = useState(false);
  const [live, setLive] = useState<Entry[]>([]);
  const esRef = useRef<EventSource | null>(null);

  const k = key.trim() || "events";
  const { data: range } = usePolling(() => api.streamRange(k, 50, true).catch(() => null), 1500);
  const entries = range?.entries ?? [];

  useEffect(() => () => esRef.current?.close(), []);

  const stopTail = () => { esRef.current?.close(); esRef.current = null; setTailing(false); };
  const startTail = () => {
    stopTail();
    setLive([]);
    const es = new EventSource(api.streamTailUrl(k, "$"));
    es.onopen = () => setTailing(true);
    es.onmessage = (e) => {
      try { const ent = JSON.parse(e.data) as Entry; setLive((p) => [ent, ...p].slice(0, 100)); }
      catch { /* ignore */ }
    };
    es.onerror = () => setTailing(false);
    esRef.current = es;
  };

  const add = async () => {
    if (!field.trim()) return;
    await api.streamAdd(k, { [field.trim()]: value });
    setValue("");
  };

  const fieldsStr = (f: Record<string, string>) =>
    Object.entries(f).map(([a, b]) => `${a}=${b}`).join("  ");

  return (
    <>
      <PageHeader
        title="Streams"
        subtitle="An append-only log with monotonic IDs — append events, range-scan history, and follow new entries live over Server-Sent Events."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Length" icon={Hash} value={range?.length ?? 0} accent="primary" hint={k} />
        <Stat label="Tail" icon={tailing ? Radio : WifiOff} value={tailing ? "live" : "idle"}
          accent={tailing ? "emerald" : "rose"} />
        <Stat label="Live entries" icon={Waves} value={live.length} accent="accent" />
        <Stat label="Latest id" icon={Hash} value={entries[0]?.id ?? "—"} accent="primary" />
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-[340px_1fr]">
        <div className="space-y-4">
          <div className="card p-4">
            <div className="mb-3 flex items-center gap-2 text-sm font-medium"><Plus size={15} /> Append entry</div>
            <div className="space-y-3">
              <label className="block">
                <span className="text-xs text-slate-400">Stream key</span>
                <input className="input mt-1" value={key} onChange={(e) => setKey(e.target.value)} />
              </label>
              <div className="grid grid-cols-2 gap-2">
                <label className="block">
                  <span className="text-xs text-slate-400">Field</span>
                  <input className="input mt-1" value={field} onChange={(e) => setField(e.target.value)} />
                </label>
                <label className="block">
                  <span className="text-xs text-slate-400">Value</span>
                  <input className="input mt-1" value={value} onChange={(e) => setValue(e.target.value)} placeholder="signup" />
                </label>
              </div>
              <button className="btn-primary w-full" onClick={add}><Plus size={14} /> XADD</button>
            </div>
          </div>
          <div className="card p-4">
            <div className="mb-3 flex items-center gap-2 text-sm font-medium"><Radio size={15} /> Live tail</div>
            {tailing ? (
              <button className="btn-ghost w-full" onClick={stopTail}><WifiOff size={14} /> Stop tailing</button>
            ) : (
              <button className="btn-primary w-full" onClick={startTail}><Radio size={14} /> Tail new entries</button>
            )}
            <div className="mt-3 max-h-72 overflow-y-auto font-mono text-[12px]">
              {live.length === 0 ? (
                <div className="py-6 text-center text-slate-500">{tailing ? "Waiting for new entries…" : "Not tailing."}</div>
              ) : (
                <ul className="space-y-1">
                  {live.map((e, i) => (
                    <li key={i} className="rounded px-2 py-1 hover:bg-white/5">
                      <span className="text-emerald-400">{e.id}</span> <span className="text-slate-300">{fieldsStr(e.fields)}</span>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </div>
        </div>

        <div className="card flex flex-col p-0">
          <div className="flex items-center gap-2 border-b border-border px-4 py-2.5 text-sm font-medium">
            <Waves size={15} /> {k} · history
            <span className="ml-auto text-xs text-slate-500">newest first · {range?.length ?? 0} total</span>
          </div>
          <div className="max-h-[60vh] overflow-y-auto">
            {entries.length === 0 ? (
              <div className="flex h-40 items-center justify-center text-sm text-slate-500">
                Empty stream — append an entry on the left.
              </div>
            ) : (
              <table className="w-full text-sm">
                <thead className="bg-surface text-left text-xs uppercase tracking-wider text-slate-500">
                  <tr><th className="px-4 py-2">ID</th><th className="px-4 py-2">Fields</th></tr>
                </thead>
                <tbody>
                  {entries.map((e) => (
                    <tr key={e.id} className="border-t border-border">
                      <td className="px-4 py-2 font-mono text-[12px] text-emerald-400 whitespace-nowrap">{e.id}</td>
                      <td className="px-4 py-2 font-mono text-[12px] text-slate-300">{fieldsStr(e.fields)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </div>
      </div>
    </>
  );
}
