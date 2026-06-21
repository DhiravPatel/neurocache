import { useEffect, useMemo, useState } from "react";
import {
  DollarSign, Gauge, PiggyBank, Sliders, Wallet,
  Plus, RotateCcw, ShieldCheck, ShieldAlert,
} from "lucide-react";
import {
  Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis,
} from "recharts";
import { api, type MetricsSummary, type TenantUsage, type TimelineSample } from "../lib/api";
import { usePolling } from "../lib/usePolling";
import { PageHeader, Stat } from "../components/Stat";

function fmt(n: number) { return new Intl.NumberFormat().format(n); }
function pct(r: number) { return (r * 100).toFixed(0) + "%"; }
/** Adaptive USD formatting — cents-precision for big numbers, more digits for tiny ones. */
function money(n: number) {
  if (n >= 1) return "$" + n.toFixed(2);
  if (n >= 0.01) return "$" + n.toFixed(4);
  return "$" + n.toFixed(6);
}
function humanWindow(ms: number) {
  if (ms <= 0) return "—";
  const s = ms / 1000;
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${(s / 60).toFixed(s % 60 ? 1 : 0)}m`;
  if (s < 86400) return `${(s / 3600).toFixed(s % 3600 ? 1 : 0)}h`;
  return `${(s / 86400).toFixed(s % 86400 ? 1 : 0)}d`;
}

const chartColors = { savings: "#34d399", grid: "#1f2430" };

export default function Costs() {
  const { data: summary } = usePolling<MetricsSummary>(api.metricsSummary, 2000);
  const { data: info } = usePolling(api.info, 5000);
  const { data: costList } = usePolling(api.costList, 3000);
  const { data: timeline } = usePolling(api.metricsTimeline, 2000);

  const tenants: TenantUsage[] = costList?.tenants ?? [];
  const savings = summary?.estimated_savings_usd ?? 0;
  const uptime = info?.uptime_seconds ?? 0;

  // Project savings to a 30-day run-rate from observed savings since start.
  const projected30d = uptime > 5 ? (savings / uptime) * 86_400 * 30 : 0;

  // Per-interval savings derived from LLM hits × the live cost model.
  const perHitUsd =
    ((summary?.tokens_per_hit ?? 0) * (summary?.usd_per_million_tokens ?? 0)) / 1_000_000;
  const samples = useMemo(() => {
    const raw: TimelineSample[] = timeline?.samples ?? [];
    return raw.map((s) => ({
      label: `-${Math.max(0, Math.round((Date.now() - new Date(s.t).getTime()) / 1000))}s`,
      savings: +(s.llm_hits * perHitUsd).toFixed(6),
    }));
  }, [timeline, perHitUsd]);

  return (
    <>
      <PageHeader
        title="Cost & Budgets"
        subtitle="Track LLM spend avoided by caching, tune the savings cost model live, and cap per-tenant spend with budgets that fail fast."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Est. LLM Savings" icon={DollarSign} value={money(savings)} accent="emerald"
          hint="cumulative since start" />
        <Stat label="Projected / 30d" icon={PiggyBank} value={money(projected30d)} accent="emerald"
          hint="at current run-rate" />
        <Stat label="LLM Hit Rate" icon={Gauge} value={pct(summary?.llm_hit_rate ?? 0)} accent="accent"
          hint={`${fmt(summary?.llm_hits ?? 0)} calls avoided`} />
        <Stat label="Tenants w/ budgets" icon={Wallet} value={fmt(tenants.length)} accent="primary"
          hint={tenants.length ? "see table below" : "none configured"} />
      </div>

      <h2 className="mb-3 mt-8 text-sm font-semibold text-slate-300">Savings rate (per second)</h2>
      <div className="card p-4">
        <div className="h-48">
          <ResponsiveContainer>
            <AreaChart data={samples}>
              <defs>
                <linearGradient id="savingsFill" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor={chartColors.savings} stopOpacity={0.5} />
                  <stop offset="100%" stopColor={chartColors.savings} stopOpacity={0} />
                </linearGradient>
              </defs>
              <CartesianGrid stroke={chartColors.grid} strokeDasharray="3 3" />
              <XAxis dataKey="label" tick={{ fontSize: 10, fill: "#475569" }} interval={9} />
              <YAxis tick={{ fontSize: 10, fill: "#475569" }} width={56}
                tickFormatter={(v) => money(Number(v))} />
              <Tooltip
                contentStyle={{ background: "#11141c", border: "1px solid #1f2430", borderRadius: 8, fontSize: 12 }}
                formatter={(v) => [money(Number(v)), "saved"]}
              />
              <Area type="monotone" dataKey="savings" stroke={chartColors.savings}
                strokeWidth={2} fill="url(#savingsFill)" isAnimationActive={false} />
            </AreaChart>
          </ResponsiveContainer>
        </div>
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-[340px_1fr]">
        <CostModelCard summary={summary} />
        <BudgetsCard tenants={tenants} />
      </div>
    </>
  );
}

/* ─── Cost-model editor (the runtime-tunable LLM savings assumptions) ── */
function CostModelCard({ summary }: { summary: MetricsSummary | null }) {
  const [tokens, setTokens] = useState("");
  const [usd, setUsd] = useState("");
  const [seeded, setSeeded] = useState(false);
  const [saving, setSaving] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);

  // Seed the inputs once from the live model, then leave them to the user.
  useEffect(() => {
    if (!seeded && summary) {
      setTokens(String(summary.tokens_per_hit));
      setUsd(String(summary.usd_per_million_tokens));
      setSeeded(true);
    }
  }, [summary, seeded]);

  const save = async () => {
    const t = Number(tokens);
    const u = Number(usd);
    if (!Number.isFinite(t) || !Number.isFinite(u) || t <= 0 || u <= 0) {
      setMsg("Enter positive numbers for both fields.");
      return;
    }
    setSaving(true);
    setMsg(null);
    try {
      await api.costSetModel(t, u);
      setMsg("Saved — savings now priced live.");
    } catch (e) {
      setMsg((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="card p-4">
      <div className="mb-1 flex items-center gap-2 text-sm font-medium">
        <Sliders size={15} /> Savings cost model
      </div>
      <p className="mb-3 text-xs text-slate-500">
        How each cache hit is priced. Updates apply live to future hits — no restart needed.
      </p>
      <div className="space-y-3">
        <label className="block">
          <span className="text-xs text-slate-400">Tokens saved per hit</span>
          <input className="input mt-1" type="number" min={1} value={tokens}
            onChange={(e) => setTokens(e.target.value)} placeholder="1000" />
        </label>
        <label className="block">
          <span className="text-xs text-slate-400">USD per million tokens</span>
          <input className="input mt-1" type="number" min={0} step="0.01" value={usd}
            onChange={(e) => setUsd(e.target.value)} placeholder="10.00" />
        </label>
        <div className="rounded-md border border-border bg-bg/40 p-2 text-center text-xs text-slate-400">
          ≈ <span className="font-medium text-emerald-400">
            {money(((Number(tokens) || 0) * (Number(usd) || 0)) / 1_000_000)}
          </span> saved per cache hit
        </div>
        <button className="btn-primary w-full" onClick={save} disabled={saving}>
          {saving ? "Saving…" : "Apply cost model"}
        </button>
        {msg ? <div className="text-xs text-slate-400">{msg}</div> : null}
      </div>
    </div>
  );
}

/* ─── Per-tenant budget management ──────────────────────────────────── */
function BudgetsCard({ tenants }: { tenants: TenantUsage[] }) {
  const [tenant, setTenant] = useState("");
  const [maxUsd, setMaxUsd] = useState("");
  const [windowSec, setWindowSec] = useState("60");
  const [err, setErr] = useState<string | null>(null);

  // Inline "record a charge" demo to show the budget gate in action.
  const [chargeTenant, setChargeTenant] = useState("");
  const [chargeUsd, setChargeUsd] = useState("");
  const [chargeResult, setChargeResult] =
    useState<{ allowed: boolean; remaining: number } | null>(null);

  const setBudget = async () => {
    const m = Number(maxUsd);
    const w = Number(windowSec);
    if (!tenant.trim() || !Number.isFinite(m) || m <= 0 || !Number.isFinite(w) || w <= 0) {
      setErr("Tenant, a positive max ($), and a positive window (s) are required.");
      return;
    }
    setErr(null);
    try {
      await api.costSetBudget(tenant.trim(), m, Math.round(w * 1000));
      setTenant("");
      setMaxUsd("");
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  const reset = async (t: string) => {
    try { await api.costReset(t); } catch { /* poll will reconcile */ }
  };

  const charge = async () => {
    const u = Number(chargeUsd);
    if (!chargeTenant.trim() || !Number.isFinite(u) || u < 0) {
      setErr("Pick a tenant and a non-negative amount to charge.");
      return;
    }
    setErr(null);
    try {
      setChargeResult(await api.costCharge(chargeTenant.trim(), u));
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  return (
    <div className="card p-4">
      <div className="mb-1 flex items-center gap-2 text-sm font-medium">
        <Wallet size={15} /> Per-tenant budgets
      </div>
      <p className="mb-3 text-xs text-slate-500">
        Cap spend per tenant over a sliding window. Charges past the cap are rejected before you pay.
      </p>

      {/* set / update budget */}
      <div className="grid grid-cols-[1fr_90px_90px_auto] gap-2">
        <input className="input" placeholder="tenant id" value={tenant}
          onChange={(e) => setTenant(e.target.value)} />
        <input className="input" type="number" min={0} step="0.01" placeholder="max $" value={maxUsd}
          onChange={(e) => setMaxUsd(e.target.value)} />
        <input className="input" type="number" min={1} placeholder="window s" value={windowSec}
          onChange={(e) => setWindowSec(e.target.value)} />
        <button className="btn-primary px-3" onClick={setBudget} title="Set / update budget">
          <Plus size={15} />
        </button>
      </div>

      {/* table */}
      <div className="mt-4 overflow-hidden rounded-md border border-border">
        <table className="w-full text-sm">
          <thead className="bg-surface text-left text-xs uppercase tracking-wider text-slate-500">
            <tr>
              <th className="px-3 py-2">Tenant</th>
              <th className="px-3 py-2">Usage</th>
              <th className="px-3 py-2 text-right">Window</th>
              <th className="px-3 py-2 text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            {tenants.length === 0 ? (
              <tr><td colSpan={4} className="px-3 py-6 text-center text-slate-500">
                No budgets yet — add one above.
              </td></tr>
            ) : (
              tenants.map((t) => {
                const frac = t.max > 0 ? Math.min(1, t.used / t.max) : 0;
                const over = t.max > 0 && t.used >= t.max;
                return (
                  <tr key={t.tenant} className="border-t border-border">
                    <td className="px-3 py-2 font-mono text-[13px]">{t.tenant}</td>
                    <td className="px-3 py-2">
                      <div className="flex items-center justify-between text-xs text-slate-400">
                        <span>{money(t.used)} / {money(t.max)}</span>
                        <span className={over ? "miss" : "text-slate-500"}>{pct(frac)}</span>
                      </div>
                      <div className="mt-1 h-1.5 w-full overflow-hidden rounded-full bg-bg">
                        <div
                          className={over ? "h-full bg-rose-500" : frac > 0.8 ? "h-full bg-amber-400" : "h-full bg-emerald-400"}
                          style={{ width: `${frac * 100}%` }}
                        />
                      </div>
                    </td>
                    <td className="px-3 py-2 text-right text-xs text-slate-400">{humanWindow(t.window_ms)}</td>
                    <td className="px-3 py-2 text-right">
                      <button
                        onClick={() => reset(t.tenant)}
                        className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-slate-400 hover:bg-white/5 hover:text-slate-100"
                        title="Reset this tenant's spend log"
                      >
                        <RotateCcw size={13} /> Reset
                      </button>
                    </td>
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>

      {/* record a charge (demonstrates the gate) */}
      <div className="mt-4 border-t border-border pt-4">
        <div className="mb-2 text-xs font-medium text-slate-400">Record a charge</div>
        <div className="grid grid-cols-[1fr_90px_auto] gap-2">
          <input className="input" placeholder="tenant id" value={chargeTenant}
            onChange={(e) => setChargeTenant(e.target.value)} />
          <input className="input" type="number" min={0} step="0.01" placeholder="$" value={chargeUsd}
            onChange={(e) => setChargeUsd(e.target.value)} />
          <button className="btn-ghost px-3" onClick={charge}>Charge</button>
        </div>
        {chargeResult ? (
          <div className="mt-2 flex items-center gap-2 text-sm">
            {chargeResult.allowed ? (
              <><ShieldCheck size={15} className="text-emerald-400" /><span className="hit">allowed</span></>
            ) : (
              <><ShieldAlert size={15} className="text-rose-400" /><span className="miss">rejected — over budget</span></>
            )}
            <span className="pill ml-auto">{money(chargeResult.remaining)} left</span>
          </div>
        ) : null}
      </div>

      {err ? <div className="mt-3 text-sm text-rose-400">{err}</div> : null}
    </div>
  );
}
