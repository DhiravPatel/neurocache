import { useEffect, useMemo, useState } from "react";
import { TrendingUp, Zap, Flame, Gauge, DollarSign } from "lucide-react";
import {
  Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis,
  Area, AreaChart, CartesianGrid, BarChart, Bar,
} from "recharts";
import { api, type TimelineSample, type HotKey, type CommandCount, type MetricsSummary } from "../lib/api";
import { usePolling } from "../lib/usePolling";
import { PageHeader, Stat } from "../components/Stat";

function fmt(n: number) { return new Intl.NumberFormat().format(n); }
function pct(r: number) { return (r * 100).toFixed(0) + "%"; }
function usd(n: number) { return "$" + n.toFixed(4); }

function secondsAgo(iso: string): string {
  const d = new Date(iso);
  const diff = Math.max(0, (Date.now() - d.getTime()) / 1000);
  return `-${Math.round(diff)}s`;
}

const chartColors = {
  primary: "#7c5cff",
  accent:  "#22d3ee",
  hit:     "#34d399",
  miss:    "#fb7185",
  grid:    "#1f2430",
  axis:    "#475569",
};

function emptySamples(n = 60): TimelineSample[] {
  const now = Date.now();
  return Array.from({ length: n }, (_, i) => ({
    t: new Date(now - (n - i) * 1000).toISOString(),
    commands: 0, sem_hits: 0, sem_misses: 0, llm_hits: 0, llm_misses: 0,
    kv_hits: 0, kv_misses: 0, p50_ms: 0, p95_ms: 0,
  }));
}

export default function Analytics() {
  const { data: summary } = usePolling<MetricsSummary>(api.metricsSummary, 2000);
  const { data: timelineData } = usePolling(api.metricsTimeline, 1000);
  const { data: hotKeysData } = usePolling(api.metricsHotKeys, 3000);
  const [breakdown, setBreakdown] = useState<CommandCount[]>([]);

  useEffect(() => {
    api.metricsBreakdown().then((r) => setBreakdown(r.commands ?? [])).catch(() => {});
  }, [summary?.commands]);

  const samples = useMemo(() => {
    const raw = timelineData?.samples ?? [];
    const padded = raw.length < 60
      ? [...emptySamples(60 - raw.length), ...raw]
      : raw;
    return padded.map((s) => ({
      ...s,
      label: secondsAgo(s.t),
      cache_total: s.sem_hits + s.sem_misses + s.llm_hits + s.llm_misses,
      hit_rate:
        s.sem_hits + s.sem_misses + s.llm_hits + s.llm_misses === 0
          ? 0
          : (s.sem_hits + s.llm_hits) /
            (s.sem_hits + s.sem_misses + s.llm_hits + s.llm_misses),
    }));
  }, [timelineData]);

  const hotKeys: HotKey[] = hotKeysData?.keys ?? [];
  const topCommands = breakdown.slice(0, 8);

  return (
    <>
      <PageHeader
        title="Analytics"
        subtitle="Live usage analysis — command rate, cache hit rate, hot keys, and estimated LLM savings."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat
          label="Total Commands"
          icon={TrendingUp}
          value={fmt(summary?.commands ?? 0)}
          accent="primary"
        />
        <Stat
          label="Semantic Hit Rate"
          icon={Zap}
          value={pct(summary?.sem_hit_rate ?? 0)}
          hint={`${fmt(summary?.sem_hits ?? 0)} hits / ${fmt(summary?.sem_misses ?? 0)} miss`}
          accent="accent"
        />
        <Stat
          label="LLM Cache Hit Rate"
          icon={Gauge}
          value={pct(summary?.llm_hit_rate ?? 0)}
          hint={`${fmt(summary?.llm_hits ?? 0)} hits`}
          accent="emerald"
        />
        <Stat
          label="Est. LLM Savings"
          icon={DollarSign}
          value={usd(summary?.estimated_savings_usd ?? 0)}
          hint={`@ $${(summary?.usd_per_million_tokens ?? 0).toFixed(2)}/M tok · ${summary?.tokens_per_hit ?? 0} tok/hit`}
          accent="emerald"
        />
      </div>

      <h2 className="mt-8 mb-3 text-sm font-semibold text-slate-300">Commands per second</h2>
      <div className="card p-4">
        <div className="h-56">
          <ResponsiveContainer>
            <AreaChart data={samples}>
              <defs>
                <linearGradient id="cmdFill" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%"  stopColor={chartColors.primary} stopOpacity={0.5} />
                  <stop offset="100%" stopColor={chartColors.primary} stopOpacity={0} />
                </linearGradient>
              </defs>
              <CartesianGrid stroke={chartColors.grid} strokeDasharray="3 3" />
              <XAxis dataKey="label" tick={{ fill: chartColors.axis, fontSize: 11 }} minTickGap={30} />
              <YAxis allowDecimals={false} tick={{ fill: chartColors.axis, fontSize: 11 }} />
              <Tooltip
                contentStyle={{ background: "#11141c", border: "1px solid #1f2430", borderRadius: 8 }}
                labelStyle={{ color: "#94a3b8" }}
              />
              <Area type="monotone" dataKey="commands" stroke={chartColors.primary} fill="url(#cmdFill)" strokeWidth={2} />
            </AreaChart>
          </ResponsiveContainer>
        </div>
      </div>

      <div className="mt-6 grid gap-4 md:grid-cols-2">
        <div className="card p-4">
          <div className="mb-2 flex items-center gap-2 text-sm font-medium">
            <Zap size={15} /> Cache hit rate (rolling 60s)
          </div>
          <div className="h-48">
            <ResponsiveContainer>
              <LineChart data={samples}>
                <CartesianGrid stroke={chartColors.grid} strokeDasharray="3 3" />
                <XAxis dataKey="label" tick={{ fill: chartColors.axis, fontSize: 11 }} minTickGap={30} />
                <YAxis
                  tick={{ fill: chartColors.axis, fontSize: 11 }}
                  domain={[0, 1]}
                  tickFormatter={(v) => `${(v * 100).toFixed(0)}%`}
                />
                <Tooltip
                  contentStyle={{ background: "#11141c", border: "1px solid #1f2430", borderRadius: 8 }}
                  formatter={(v: number) => `${(v * 100).toFixed(0)}%`}
                />
                <Line type="monotone" dataKey="hit_rate" stroke={chartColors.hit} strokeWidth={2} dot={false} />
              </LineChart>
            </ResponsiveContainer>
          </div>
        </div>

        <div className="card p-4">
          <div className="mb-2 flex items-center gap-2 text-sm font-medium">
            <Gauge size={15} /> Latency p50 / p95 (ms)
          </div>
          <div className="h-48">
            <ResponsiveContainer>
              <LineChart data={samples}>
                <CartesianGrid stroke={chartColors.grid} strokeDasharray="3 3" />
                <XAxis dataKey="label" tick={{ fill: chartColors.axis, fontSize: 11 }} minTickGap={30} />
                <YAxis tick={{ fill: chartColors.axis, fontSize: 11 }} />
                <Tooltip
                  contentStyle={{ background: "#11141c", border: "1px solid #1f2430", borderRadius: 8 }}
                />
                <Line type="monotone" dataKey="p50_ms" stroke={chartColors.accent} strokeWidth={2} dot={false} />
                <Line type="monotone" dataKey="p95_ms" stroke={chartColors.miss} strokeWidth={2} dot={false} />
              </LineChart>
            </ResponsiveContainer>
          </div>
        </div>
      </div>

      <div className="mt-6 grid gap-4 md:grid-cols-2">
        <div className="card p-4">
          <div className="mb-3 flex items-center gap-2 text-sm font-medium">
            <Flame size={15} className="text-rose-400" /> Hot Keys
          </div>
          {hotKeys.length === 0 ? (
            <div className="py-6 text-center text-sm text-slate-500">No GET activity yet.</div>
          ) : (
            <div className="space-y-1.5">
              {hotKeys.map((k) => (
                <div key={k.key} className="flex items-center gap-3 rounded-md border border-border bg-bg/40 px-3 py-2">
                  <div className="flex-1 truncate font-mono text-sm text-primary">{k.key}</div>
                  <span className="pill">{fmt(k.hits)} hits</span>
                </div>
              ))}
            </div>
          )}
        </div>

        <div className="card p-4">
          <div className="mb-3 text-sm font-medium">Command breakdown</div>
          {topCommands.length === 0 ? (
            <div className="py-6 text-center text-sm text-slate-500">No commands issued yet.</div>
          ) : (
            <div className="h-48">
              <ResponsiveContainer>
                <BarChart data={topCommands} layout="vertical" margin={{ left: 40 }}>
                  <CartesianGrid stroke={chartColors.grid} strokeDasharray="3 3" />
                  <XAxis type="number" tick={{ fill: chartColors.axis, fontSize: 11 }} />
                  <YAxis type="category" dataKey="command" width={110} tick={{ fill: chartColors.axis, fontSize: 11 }} />
                  <Tooltip
                    contentStyle={{ background: "#11141c", border: "1px solid #1f2430", borderRadius: 8 }}
                  />
                  <Bar dataKey="count" fill={chartColors.primary} radius={[0, 4, 4, 0]} />
                </BarChart>
              </ResponsiveContainer>
            </div>
          )}
        </div>
      </div>
    </>
  );
}
