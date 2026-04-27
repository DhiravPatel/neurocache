import { useEffect, useState } from "react";
import { Compass, RefreshCw, Search } from "lucide-react";
import { api } from "../lib/api";
import { PageHeader } from "../components/Stat";

// VectorSet mirrors the /api/vector/sets payload — every key of
// TypeVector with its index configuration + cardinality.
type VectorSet = {
  key: string;
  algo: string;
  dim: number;
  metric: string;
  m: number;
  ef_construct: number;
  ef_runtime: number;
  card: number;
  bytes_approx: number;
};

function fmt(n: number) { return new Intl.NumberFormat().format(n); }
function fmtBytes(n: number) {
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
  return (n / 1024 / 1024).toFixed(1) + " MB";
}

export default function VectorSetsPage() {
  const [sets, setSets] = useState<VectorSet[] | null>(null);
  const [err, setErr] = useState<string>("");
  const [filter, setFilter] = useState<string>("");
  const [busy, setBusy] = useState(false);

  // Probe panel — pick a set, paste a vector, run VSIM.
  const [probeKey, setProbeKey] = useState<string>("");
  const [probeVec, setProbeVec] = useState<string>("");
  const [probeCount, setProbeCount] = useState<string>("5");
  const [probeRows, setProbeRows] = useState<string[][]>([]);
  const [probeErr, setProbeErr] = useState<string>("");

  async function refresh() {
    setBusy(true);
    setErr("");
    try {
      const r = await api.vectorSets();
      setSets(r.sets ?? []);
    } catch (e: any) {
      setErr(String(e?.message ?? e));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => { refresh(); }, []);

  async function runProbe() {
    setProbeErr("");
    setProbeRows([]);
    if (!probeKey || !probeVec) {
      setProbeErr("pick a key and paste a comma-separated vector");
      return;
    }
    try {
      const r = await api.exec("VSIM", [probeKey, probeVec, "COUNT", probeCount, "WITHSCORES"]);
      // The reply is a flat [id, score, id, score, ...] array.
      const flat = (r.result as string[]) ?? [];
      const rows: string[][] = [];
      for (let i = 0; i + 1 < flat.length; i += 2) {
        rows.push([flat[i], flat[i + 1]]);
      }
      setProbeRows(rows);
    } catch (e: any) {
      setProbeErr(String(e?.message ?? e));
    }
  }

  const filtered = (sets ?? []).filter((s) =>
    !filter || s.key.toLowerCase().includes(filter.toLowerCase()),
  );

  return (
    <>
      <PageHeader
        title="Vector Sets"
        subtitle="First-class vector-set keys (V*). HNSW or FLAT backing index, COSINE / L2 / IP metrics, optional JSON attributes per member."
      />

      <div className="mb-4 flex items-center gap-3">
        <div className="flex-1 max-w-sm">
          <input
            type="text"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder="Filter keys…"
            className="input w-full"
          />
        </div>
        <button onClick={refresh} className="btn flex items-center gap-2" disabled={busy}>
          <RefreshCw size={14} className={busy ? "animate-spin" : ""} />
          Refresh
        </button>
      </div>

      {err && <div className="mb-3 rounded-md border border-rose-500/40 bg-rose-500/10 p-3 text-sm text-rose-300">{err}</div>}

      <div className="card overflow-hidden">
        <table className="w-full text-sm">
          <thead>
            <tr className="bg-bg/40 text-left text-xs uppercase tracking-wide text-slate-400">
              <th className="px-4 py-2">Key</th>
              <th className="px-4 py-2">Algo</th>
              <th className="px-4 py-2">Dim</th>
              <th className="px-4 py-2">Metric</th>
              <th className="px-4 py-2">HNSW</th>
              <th className="px-4 py-2">Members</th>
              <th className="px-4 py-2">Memory</th>
            </tr>
          </thead>
          <tbody>
            {filtered.length === 0 ? (
              <tr>
                <td colSpan={7} className="py-10 text-center text-sm text-slate-500">
                  {sets === null ? "Loading…" : "No vector sets yet. Run VADD to create one."}
                </td>
              </tr>
            ) : (
              filtered.map((s) => (
                <tr key={s.key} className="border-t border-border hover:bg-white/[.02]">
                  <td className="px-4 py-2 font-mono text-primary">
                    <button
                      className="hover:underline"
                      onClick={() => setProbeKey(s.key)}
                      title="Use as probe key"
                    >
                      {s.key}
                    </button>
                  </td>
                  <td className="px-4 py-2">{s.algo}</td>
                  <td className="px-4 py-2">{s.dim}</td>
                  <td className="px-4 py-2">{s.metric}</td>
                  <td className="px-4 py-2 text-xs text-slate-400">
                    {s.algo === "HNSW" ? `M=${s.m} efC=${s.ef_construct} efR=${s.ef_runtime}` : "—"}
                  </td>
                  <td className="px-4 py-2">{fmt(s.card)}</td>
                  <td className="px-4 py-2 text-slate-400">{fmtBytes(s.bytes_approx)}</td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>

      <div className="mt-6 card p-4">
        <div className="mb-3 flex items-center gap-2 text-sm font-medium">
          <Search size={15} className="text-accent" /> KNN probe
        </div>
        <div className="grid gap-3 md:grid-cols-3">
          <input
            value={probeKey}
            onChange={(e) => setProbeKey(e.target.value)}
            placeholder="vector-set key"
            className="input md:col-span-1"
          />
          <input
            value={probeVec}
            onChange={(e) => setProbeVec(e.target.value)}
            placeholder="comma-separated query vector (e.g. 0.1,0.2,0.3)"
            className="input md:col-span-2"
          />
          <input
            value={probeCount}
            onChange={(e) => setProbeCount(e.target.value)}
            placeholder="count"
            className="input"
          />
          <div className="md:col-span-2 flex justify-end">
            <button onClick={runProbe} className="btn flex items-center gap-2">
              <Compass size={14} /> Run VSIM
            </button>
          </div>
        </div>
        {probeErr && (
          <div className="mt-3 rounded-md border border-rose-500/40 bg-rose-500/10 p-3 text-sm text-rose-300">
            {probeErr}
          </div>
        )}
        {probeRows.length > 0 && (
          <div className="mt-3 space-y-1.5">
            {probeRows.map(([id, score], i) => (
              <div
                key={`${id}-${i}`}
                className="flex items-center gap-3 rounded-md border border-border bg-bg/40 px-3 py-2"
              >
                <div className="flex-1 truncate font-mono text-sm text-primary">{id}</div>
                <span className="pill">distance {Number(score).toFixed(4)}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </>
  );
}
