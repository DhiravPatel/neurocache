import { useEffect, useState } from "react";
import { FlaskConical, Plus, Trophy, Dices, Target } from "lucide-react";
import { api, type ExperimentStats } from "../lib/api";
import { usePolling } from "../lib/usePolling";
import { PageHeader, Stat } from "../components/Stat";

export default function ExperimentsPage() {
  const { data: list } = usePolling(api.abList, 2000);
  const names = list?.experiments ?? [];

  const [selected, setSelected] = useState("");
  const [stats, setStats] = useState<ExperimentStats | null>(null);
  const [newName, setNewName] = useState("prompt.greeting");
  const [variants, setVariants] = useState("control, variant-b");
  const [user, setUser] = useState("user-42");
  const [msg, setMsg] = useState<string | null>(null);

  const active = selected || names[0] || "";

  // Live per-experiment stats, re-subscribed whenever the active experiment
  // changes (usePolling captures its fn once, so it can't follow `active`).
  useEffect(() => {
    if (!active) { setStats(null); return; }
    let alive = true;
    const tick = async () => {
      try { const s = await api.abStats(active); if (alive) setStats(s); }
      catch { if (alive) setStats(null); }
    };
    tick();
    const id = setInterval(tick, 1500);
    return () => { alive = false; clearInterval(id); };
  }, [active]);

  const define = async () => {
    const vs = variants.split(",").map((s) => s.trim()).filter(Boolean);
    if (!newName.trim() || vs.length < 2) { setMsg("name + at least 2 variants required"); return; }
    await api.abDefine(newName.trim(), vs);
    setSelected(newName.trim());
    setMsg(`Defined "${newName.trim()}" with ${vs.length} variants`);
  };
  // Simulate: assign the user, count it as an exposure, and record a random win.
  const simulate = async () => {
    if (!active || !user.trim()) return;
    let variant: string | undefined;
    try { variant = (await api.abAssign(active, user.trim())).variant; }
    catch { setMsg("assign failed"); return; }
    if (!variant) { setMsg("assign failed"); return; }
    await api.abExpose(active, variant);
    await api.abRecord(active, variant, Math.random() < 0.5 ? 1 : 0);
    setMsg(`${user.trim()} → ${variant} (exposed + outcome recorded)`);
    try { setStats(await api.abStats(active)); } catch { /* interval will refresh */ }
  };

  const variantsArr = stats?.variants ?? [];

  return (
    <>
      <PageHeader
        title="A/B Experiments"
        subtitle="Deterministic variant assignment with server-side exposure + conversion tracking. Split prompts, models, or thresholds and let win-rate pick the leader."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Experiments" icon={FlaskConical} value={names.length} accent="primary" />
        <Stat label="Selected" icon={Target} value={active || "—"} accent="accent" />
        <Stat label="Variants" icon={Dices} value={variantsArr.length} accent="emerald" />
        <Stat label="Leader" icon={Trophy} value={stats?.winner || "—"} accent="emerald"
          hint={stats?.winner ? "≥30 exposures" : "not enough data"} />
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-[300px_1fr]">
        <div className="space-y-4">
          <div className="card p-4">
            <div className="mb-3 flex items-center gap-2 text-sm font-medium"><Plus size={15} /> Define experiment</div>
            <label className="block">
              <span className="text-xs text-slate-400">Name</span>
              <input className="input mt-1" value={newName} onChange={(e) => setNewName(e.target.value)} />
            </label>
            <label className="mt-2 block">
              <span className="text-xs text-slate-400">Variants (comma-separated)</span>
              <input className="input mt-1" value={variants} onChange={(e) => setVariants(e.target.value)} />
            </label>
            <button className="btn-primary mt-3 w-full" onClick={define}>Define</button>
          </div>

          <div className="card p-4">
            <div className="mb-2 text-[11px] font-semibold uppercase tracking-wider text-slate-500">Experiments</div>
            {names.length === 0 ? (
              <div className="text-sm text-slate-500">None yet.</div>
            ) : (
              <div className="space-y-1">
                {names.map((n) => (
                  <button key={n} onClick={() => setSelected(n)}
                    className={"block w-full truncate rounded-md px-2 py-1.5 text-left font-mono text-[13px] " +
                      (n === active ? "bg-primary/10 text-primary" : "text-slate-300 hover:bg-white/5")}>
                    {n}
                  </button>
                ))}
              </div>
            )}
          </div>

          <div className="card p-4">
            <div className="mb-2 flex items-center gap-2 text-sm font-medium"><Dices size={15} /> Simulate traffic</div>
            <div className="flex gap-2">
              <input className="input" value={user} onChange={(e) => setUser(e.target.value)} placeholder="user id" />
              <button className="btn-ghost" onClick={simulate} disabled={!active}>Assign + record</button>
            </div>
            {msg && <div className="mt-2 text-xs text-slate-400">{msg}</div>}
          </div>
        </div>

        <div className="card flex flex-col p-0">
          <div className="flex items-center gap-2 border-b border-border px-4 py-2.5 text-sm font-medium">
            <FlaskConical size={15} /> {active || "experiment"} · results
            <span className="ml-auto text-xs text-slate-500">live</span>
          </div>
          {variantsArr.length === 0 ? (
            <div className="flex h-48 items-center justify-center text-sm text-slate-500">
              {active ? "No exposures yet — simulate some traffic." : "Define or select an experiment."}
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead className="bg-surface text-left text-xs uppercase tracking-wider text-slate-500">
                  <tr>
                    <th className="px-4 py-2">Variant</th>
                    <th className="px-4 py-2 text-right">Exposures</th>
                    <th className="px-4 py-2 text-right">Wins</th>
                    <th className="px-4 py-2 text-right">Win rate</th>
                    <th className="px-4 py-2 text-right">Avg value</th>
                  </tr>
                </thead>
                <tbody>
                  {variantsArr.map((v) => {
                    const isWinner = v.variant === stats?.winner;
                    return (
                      <tr key={v.variant} className="border-t border-border">
                        <td className="px-4 py-2 font-mono text-[13px]">
                          {isWinner && <Trophy size={13} className="mr-1 inline text-amber-400" />}
                          <span className={isWinner ? "text-amber-300" : "text-slate-200"}>{v.variant}</span>
                        </td>
                        <td className="px-4 py-2 text-right tabular-nums text-slate-300">{v.exposures}</td>
                        <td className="px-4 py-2 text-right tabular-nums text-slate-300">{v.wins}</td>
                        <td className="px-4 py-2 text-right">
                          <div className="flex items-center justify-end gap-2">
                            <div className="h-1.5 w-20 overflow-hidden rounded-full bg-bg">
                              <div className="h-full bg-emerald-400" style={{ width: `${Math.min(100, v.win_rate * 100)}%` }} />
                            </div>
                            <span className="tabular-nums text-accent">{(v.win_rate * 100).toFixed(0)}%</span>
                          </div>
                        </td>
                        <td className="px-4 py-2 text-right tabular-nums text-slate-400">{v.avg_value.toFixed(2)}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </div>
    </>
  );
}
