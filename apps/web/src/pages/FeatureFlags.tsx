import { useEffect, useState } from "react";
import { Flag, ToggleRight, Plus, Trash2, UserCheck, Percent } from "lucide-react";
import { api, type FlagState } from "../lib/api";
import { usePolling } from "../lib/usePolling";
import { PageHeader, Stat } from "../components/Stat";

export default function FeatureFlagsPage() {
  const { data: list } = usePolling(api.flagList, 2000);
  const names = list?.flags ?? [];

  const [selected, setSelected] = useState("");
  const [creating, setCreating] = useState(false);
  const [state, setState] = useState<FlagState | null>(null);
  const [newName, setNewName] = useState("");
  const [on, setOn] = useState(true);
  const [pct, setPct] = useState("50");
  const [testUser, setTestUser] = useState("user-42");
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [msg, setMsg] = useState<string | null>(null);

  // In "creating" mode there's no active flag — show the name input.
  const active = creating ? "" : (selected || names[0] || "");

  const load = async (name: string) => {
    if (!name) { setState(null); return; }
    try {
      const s = await api.flagGet(name);
      setState(s); setOn(s.on); setPct(String(s.percentage));
    } catch { setState(null); }
  };
  useEffect(() => { load(active); /* eslint-disable-next-line */ }, [active, names.length]);

  const save = async () => {
    const name = (creating ? newName : active).trim();
    if (!name) { setMsg("flag name required"); return; }
    await api.flagSet(name, on, Math.max(0, Math.min(100, Number(pct) || 0)), state?.allow, state?.deny);
    setMsg(`Saved ${name} — ${on ? "on" : "off"} @ ${pct}%`);
    setCreating(false); setSelected(name); setNewName(""); load(name);
  };
  const test = async () => {
    if (!active || !testUser.trim()) return;
    setEnabled((await api.flagIs(active, testUser.trim())).enabled);
  };
  const del = async (name: string) => {
    await api.flagDelete(name);
    if (name === active) { setSelected(""); setState(null); }
  };

  const rolloutPct = (s: FlagState) => (s.evals > 0 ? (s.enabled / s.evals) * 100 : 0);

  return (
    <>
      <PageHeader
        title="Feature Flags"
        subtitle="Percentage rollouts with sticky per-user bucketing plus allow/deny overrides — flip features for a cohort without a deploy, and see live eval/enable counts."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Flags" icon={Flag} value={names.length} accent="primary" />
        <Stat label="Selected" icon={ToggleRight} value={active || "—"} accent="accent" />
        <Stat label="State" icon={ToggleRight} value={state ? (state.on ? "on" : "off") : "—"}
          accent={state?.on ? "emerald" : "rose"} hint={state ? `${state.percentage}% rollout` : undefined} />
        <Stat label="Live enable rate" icon={Percent}
          value={state ? `${rolloutPct(state).toFixed(0)}%` : "—"} accent="emerald"
          hint={state ? `${state.enabled}/${state.evals} evals` : undefined} />
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-[300px_1fr]">
        <div className="card flex flex-col p-0">
          <div className="flex items-center justify-between border-b border-border px-4 py-2.5 text-sm font-medium">
            Flags
            <button onClick={() => { setCreating(true); setSelected(""); setNewName(""); setState(null); setOn(true); setPct("50"); setEnabled(null); }}
              className="text-slate-400 hover:text-primary" title="New flag"><Plus size={15} /></button>
          </div>
          <div className="max-h-[60vh] overflow-y-auto p-2">
            {names.length === 0 ? (
              <div className="px-2 py-6 text-center text-sm text-slate-500">No flags yet.</div>
            ) : names.map((n) => (
              <button key={n} onClick={() => { setCreating(false); setSelected(n); }}
                className={"flex w-full items-center justify-between rounded-md px-2 py-1.5 text-left text-sm " +
                  (n === active ? "bg-primary/10 text-primary" : "text-slate-300 hover:bg-white/5")}>
                <span className="truncate font-mono text-[13px]">{n}</span>
                <Trash2 size={13} className="shrink-0 text-slate-500 hover:text-rose-400"
                  onClick={(e) => { e.stopPropagation(); del(n); }} />
              </button>
            ))}
          </div>
        </div>

        <div className="space-y-4">
          <div className="card p-4">
            <div className="mb-3 flex items-center gap-2 text-sm font-medium"><Flag size={15} /> Configure</div>
            {(creating || !active) && (
              <input className="input mb-3" value={newName} onChange={(e) => setNewName(e.target.value)}
                placeholder="flag name e.g. new-checkout" autoFocus />
            )}
            <div className="flex items-center gap-4">
              <label className="flex items-center gap-2 text-sm">
                <button onClick={() => setOn(!on)}
                  className={"relative h-6 w-11 rounded-full transition-colors " + (on ? "bg-emerald-500" : "bg-slate-600")}>
                  <span className={"absolute top-0.5 h-5 w-5 rounded-full bg-white transition-all " + (on ? "left-[22px]" : "left-0.5")} />
                </button>
                {on ? "Enabled" : "Disabled"}
              </label>
              <label className="flex flex-1 items-center gap-2 text-sm">
                <Percent size={14} className="text-slate-500" />
                <input type="range" min={0} max={100} value={pct} onChange={(e) => setPct(e.target.value)} className="flex-1" />
                <span className="w-10 text-right tabular-nums">{pct}%</span>
              </label>
            </div>
            <button className="btn-primary mt-3 w-full" onClick={save}>Save flag</button>
            {state?.allow?.length || state?.deny?.length ? (
              <div className="mt-3 flex flex-wrap gap-2 text-xs">
                {state.allow?.map((u) => <span key={u} className="rounded bg-emerald-500/15 px-1.5 py-0.5 text-emerald-300">+{u}</span>)}
                {state.deny?.map((u) => <span key={u} className="rounded bg-rose-500/15 px-1.5 py-0.5 text-rose-300">−{u}</span>)}
              </div>
            ) : null}
            {msg && <div className="mt-2 text-xs text-slate-400">{msg}</div>}
          </div>

          <div className="card p-4">
            <div className="mb-3 flex items-center gap-2 text-sm font-medium"><UserCheck size={15} /> Test for a user</div>
            <div className="flex gap-2">
              <input className="input flex-1" value={testUser} onChange={(e) => setTestUser(e.target.value)} placeholder="user id" />
              <button className="btn-ghost" onClick={test} disabled={!active}>Evaluate</button>
            </div>
            {enabled !== null && (
              <div className="mt-3 flex items-center gap-2 text-sm">
                {enabled ? (
                  <><ToggleRight size={18} className="text-emerald-400" /><span className="hit">enabled</span></>
                ) : (
                  <><ToggleRight size={18} className="rotate-180 text-slate-500" /><span className="miss">disabled</span></>
                )}
                <span className="text-xs text-slate-500">for {testUser} on “{active}”</span>
              </div>
            )}
          </div>
        </div>
      </div>
    </>
  );
}
