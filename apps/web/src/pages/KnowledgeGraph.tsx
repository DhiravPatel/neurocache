import { useEffect, useState } from "react";
import { Network, Plus, Search, Route, Boxes, ArrowRight } from "lucide-react";
import { api, type GraphNeighbor } from "../lib/api";
import { usePolling } from "../lib/usePolling";
import { PageHeader, Stat } from "../components/Stat";

export default function KnowledgeGraphPage() {
  const { data: stats } = usePolling(api.graphStats, 2000);
  const { data: subjData } = usePolling(api.graphSubjects, 2000);
  const subjects = subjData?.subjects ?? [];

  // add-triple form
  const [subject, setSubject] = useState("alice");
  const [predicate, setPredicate] = useState("knows");
  const [object, setObject] = useState("bob");
  const [msg, setMsg] = useState<string | null>(null);

  // explore
  const [explore, setExplore] = useState("alice");
  const [neighbors, setNeighbors] = useState<GraphNeighbor[]>([]);

  // path
  const [from, setFrom] = useState("alice");
  const [to, setTo] = useState("");
  const [path, setPath] = useState<{ found: boolean; path?: GraphNeighbor[]; from?: string } | null>(null);

  const loadNeighbors = async (s: string) => {
    if (!s.trim()) { setNeighbors([]); return; }
    try { setNeighbors((await api.graphNeighbors(s.trim())).neighbors ?? []); } catch { setNeighbors([]); }
  };
  useEffect(() => { loadNeighbors(explore); /* eslint-disable-next-line */ }, [explore, stats?.edges]);

  const link = async () => {
    if (!subject.trim() || !predicate.trim() || !object.trim()) { setMsg("subject, predicate, object required"); return; }
    await api.graphLink(subject.trim(), predicate.trim(), object.trim());
    setMsg(`Linked ${subject.trim()} —${predicate.trim()}→ ${object.trim()}`);
    setExplore(subject.trim());
    loadNeighbors(subject.trim());
  };
  const findPath = async () => {
    if (!from.trim() || !to.trim()) { setMsg("from and to required"); return; }
    const r = await api.graphPath(from.trim(), to.trim(), 6);
    setPath({ ...r, from: from.trim() });
  };

  return (
    <>
      <PageHeader
        title="Knowledge Graph"
        subtitle="Subject–predicate–object triples with neighbor lookup and shortest-path traversal — link entities and walk the relationships an LLM app needs for grounded retrieval."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Subjects" icon={Boxes} value={subjects.length} accent="primary" />
        <Stat label="Edges" icon={Network} value={stats?.edges ?? 0} accent="accent" />
        <Stat label="Objects" icon={Boxes} value={stats?.objects ?? 0} accent="emerald" />
        <Stat label="Exploring" icon={Search} value={explore || "—"} accent="primary" />
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-[340px_1fr]">
        <div className="space-y-4">
          <div className="card p-4">
            <div className="mb-3 flex items-center gap-2 text-sm font-medium"><Plus size={15} /> Add a triple</div>
            <div className="space-y-2">
              <input className="input" value={subject} onChange={(e) => setSubject(e.target.value)} placeholder="subject" />
              <input className="input" value={predicate} onChange={(e) => setPredicate(e.target.value)} placeholder="predicate" />
              <input className="input" value={object} onChange={(e) => setObject(e.target.value)} placeholder="object" />
              <button className="btn-primary w-full" onClick={link}><Plus size={14} /> Link</button>
            </div>
            {msg && <div className="mt-2 text-xs text-slate-400">{msg}</div>}
          </div>

          <div className="card p-4">
            <div className="mb-3 flex items-center gap-2 text-sm font-medium"><Route size={15} /> Shortest path</div>
            <div className="flex gap-2">
              <input className="input" value={from} onChange={(e) => setFrom(e.target.value)} placeholder="from" />
              <input className="input" value={to} onChange={(e) => setTo(e.target.value)} placeholder="to" />
              <button className="btn-ghost" onClick={findPath}>Find</button>
            </div>
            {path && (
              <div className="mt-3 text-sm">
                {path.found && path.path ? (
                  <div className="flex flex-wrap items-center gap-1 font-mono text-[13px]">
                    <span className="rounded bg-primary/15 px-1.5 py-0.5 text-primary">{path.from}</span>
                    {path.path.map((e, i) => (
                      <span key={i} className="flex items-center gap-1">
                        <span className="text-[11px] text-slate-500">—{e.predicate}→</span>
                        <span className="rounded bg-primary/15 px-1.5 py-0.5 text-primary">{e.object}</span>
                      </span>
                    ))}
                  </div>
                ) : <span className="text-rose-300">No path found.</span>}
              </div>
            )}
          </div>

          <div className="card p-4">
            <div className="mb-2 text-[11px] font-semibold uppercase tracking-wider text-slate-500">Subjects</div>
            {subjects.length === 0 ? (
              <div className="text-sm text-slate-500">None yet.</div>
            ) : (
              <div className="flex flex-wrap gap-1.5">
                {subjects.map((s) => (
                  <button key={s} onClick={() => setExplore(s)}
                    className={"rounded-full border px-2.5 py-1 font-mono text-xs " +
                      (s === explore ? "border-primary/50 bg-primary/15 text-primary" : "border-border text-slate-400 hover:text-slate-200")}>
                    {s}
                  </button>
                ))}
              </div>
            )}
          </div>
        </div>

        <div className="card flex flex-col p-0">
          <div className="flex items-center gap-2 border-b border-border px-4 py-2.5 text-sm font-medium">
            <Network size={15} /> Neighbors of
            <input className="input ml-1 h-7 w-40 py-0" value={explore} onChange={(e) => setExplore(e.target.value)} />
          </div>
          {neighbors.length === 0 ? (
            <div className="flex h-48 items-center justify-center text-sm text-slate-500">
              No outgoing edges from <span className="mx-1 font-mono text-slate-400">{explore || "—"}</span>.
            </div>
          ) : (
            <ul className="divide-y divide-border">
              {neighbors.map((n, i) => (
                <li key={i} className="flex items-center gap-2 px-4 py-2.5 font-mono text-[13px]">
                  <span className="rounded bg-primary/15 px-1.5 py-0.5 text-primary">{explore}</span>
                  <span className="text-slate-500">—</span>
                  <span className="rounded bg-accent/15 px-1.5 py-0.5 text-accent">{n.predicate}</span>
                  <ArrowRight size={13} className="text-slate-500" />
                  <button onClick={() => setExplore(n.object)}
                    className="rounded bg-emerald-500/15 px-1.5 py-0.5 text-emerald-300 hover:bg-emerald-500/25">
                    {n.object}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </>
  );
}
