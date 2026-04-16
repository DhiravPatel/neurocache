import { useEffect, useState } from "react";
import { Trash2, Plus, RefreshCw } from "lucide-react";
import { api } from "../lib/api";
import { PageHeader } from "../components/Stat";

export default function KVPage() {
  const [keys, setKeys] = useState<{ key: string; value: string }[]>([]);
  const [newKey, setNewKey] = useState("");
  const [newVal, setNewVal] = useState("");
  const [ttl, setTtl] = useState("");
  const [err, setErr] = useState<string | null>(null);

  const refresh = async () => {
    try {
      const r = await api.kvList("", 100);
      setKeys(r.keys);
      setErr(null);
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  useEffect(() => {
    refresh();
  }, []);

  const add = async () => {
    if (!newKey) return;
    await api.kvSet(newKey, newVal, ttl ? Number(ttl) : 0);
    setNewKey("");
    setNewVal("");
    setTtl("");
    refresh();
  };

  const del = async (k: string) => {
    await api.kvDel(k);
    refresh();
  };

  return (
    <>
      <PageHeader
        title="Key-Value Store"
        subtitle="Standard Redis-compatible SET / GET / DEL / EXPIRE."
      />

      <div className="card p-4">
        <div className="grid grid-cols-1 gap-2 md:grid-cols-[1fr_1fr_120px_auto]">
          <input className="input" placeholder="key" value={newKey} onChange={(e) => setNewKey(e.target.value)} />
          <input className="input" placeholder="value" value={newVal} onChange={(e) => setNewVal(e.target.value)} />
          <input className="input" placeholder="TTL (sec)" value={ttl} onChange={(e) => setTtl(e.target.value)} />
          <button className="btn-primary" onClick={add}>
            <Plus size={14} /> SET
          </button>
        </div>
      </div>

      <div className="mt-4 flex items-center justify-between">
        <div className="text-sm text-slate-400">{keys.length} keys</div>
        <button className="btn-ghost" onClick={refresh}>
          <RefreshCw size={14} /> Refresh
        </button>
      </div>

      {err ? <div className="mt-3 text-sm text-rose-400">{err}</div> : null}

      <div className="mt-3 card divide-y divide-border">
        {keys.length === 0 ? (
          <div className="p-6 text-center text-sm text-slate-500">No keys yet — set one above.</div>
        ) : (
          keys.map((k) => (
            <div key={k.key} className="flex items-center gap-3 px-4 py-2.5">
              <div className="font-mono text-sm text-primary">{k.key}</div>
              <div className="flex-1 truncate font-mono text-sm text-slate-300">{k.value}</div>
              <button className="btn-ghost text-rose-400 hover:text-rose-300" onClick={() => del(k.key)}>
                <Trash2 size={14} />
              </button>
            </div>
          ))
        )}
      </div>
    </>
  );
}
