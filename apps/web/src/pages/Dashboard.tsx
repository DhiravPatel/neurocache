import { Activity, Database, Brain, Zap, Sparkles, Cpu } from "lucide-react";
import { usePolling } from "../lib/usePolling";
import { api } from "../lib/api";
import { Stat, PageHeader } from "../components/Stat";

function fmt(n: number) {
  return new Intl.NumberFormat().format(n);
}

function fmtBytes(n: number) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(2)} MB`;
}

function fmtUptime(sec: number) {
  if (sec < 60) return `${sec.toFixed(0)}s`;
  if (sec < 3600) return `${(sec / 60).toFixed(0)}m`;
  return `${(sec / 3600).toFixed(1)}h`;
}

export default function Dashboard() {
  const { data: info, error } = usePolling(api.info, 2000);

  if (error) {
    return (
      <div className="card p-6 text-sm text-rose-400">
        Engine unreachable at <span className="font-mono">{import.meta.env.VITE_API_URL ?? "localhost:8080"}</span>
        <div className="mt-2 text-slate-400">{error.message}</div>
      </div>
    );
  }

  if (!info) {
    return <div className="text-sm text-slate-500">loading…</div>;
  }

  return (
    <>
      <PageHeader
        title="Engine Dashboard"
        subtitle="Live stats streamed from the Go engine every 2 seconds."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Uptime"   value={fmtUptime(info.uptime_seconds)} icon={Activity} />
        <Stat label="Commands" value={fmt(info.commands)}             icon={Cpu}      accent="primary" />
        <Stat label="KV Keys"  value={fmt(info.kv.keys)}              icon={Database} accent="accent" />
        <Stat label="Memory"   value={fmtBytes(info.kv.bytes)}        icon={Database} />
      </div>

      <h2 className="mt-8 mb-3 text-sm font-semibold text-slate-300">AI Subsystems</h2>
      <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
        <div className="card p-4">
          <div className="flex items-center gap-2 text-sm">
            <Sparkles size={15} className="text-primary" />
            <span className="font-medium">Semantic Cache</span>
            <span className="pill ml-auto">{info.semantic.size} entries</span>
          </div>
          <div className="mt-4 grid grid-cols-3 gap-3 text-center">
            <div>
              <div className="text-xs text-slate-500">Hits</div>
              <div className="text-lg font-semibold hit">{fmt(info.semantic.hits)}</div>
            </div>
            <div>
              <div className="text-xs text-slate-500">Misses</div>
              <div className="text-lg font-semibold miss">{fmt(info.semantic.misses)}</div>
            </div>
            <div>
              <div className="text-xs text-slate-500">Hit Rate</div>
              <div className="text-lg font-semibold">
                {(info.semantic.hit_rate * 100).toFixed(0)}%
              </div>
            </div>
          </div>
        </div>

        <div className="card p-4">
          <div className="flex items-center gap-2 text-sm">
            <Zap size={15} className="text-accent" />
            <span className="font-medium">LLM Response Cache</span>
            <span className="pill ml-auto">{info.llm.size} entries</span>
          </div>
          <div className="mt-4 grid grid-cols-3 gap-3 text-center">
            <div>
              <div className="text-xs text-slate-500">Hits</div>
              <div className="text-lg font-semibold hit">{fmt(info.llm.hits)}</div>
            </div>
            <div>
              <div className="text-xs text-slate-500">Misses</div>
              <div className="text-lg font-semibold miss">{fmt(info.llm.misses)}</div>
            </div>
            <div>
              <div className="text-xs text-slate-500">Hit Rate</div>
              <div className="text-lg font-semibold">
                {(info.llm.hit_rate * 100).toFixed(0)}%
              </div>
            </div>
          </div>
        </div>

        <div className="card p-4">
          <div className="flex items-center gap-2 text-sm">
            <Brain size={15} className="text-emerald-400" />
            <span className="font-medium">User Memory</span>
            <span className="pill ml-auto">{info.memory.users} users</span>
          </div>
          <div className="mt-4 grid grid-cols-2 gap-3 text-center">
            <div>
              <div className="text-xs text-slate-500">Entries</div>
              <div className="text-lg font-semibold">{fmt(info.memory.entries)}</div>
            </div>
            <div>
              <div className="text-xs text-slate-500">Users</div>
              <div className="text-lg font-semibold">{fmt(info.memory.users)}</div>
            </div>
          </div>
        </div>
      </div>

      <h2 className="mt-8 mb-3 text-sm font-semibold text-slate-300">Runtime</h2>
      <div className="card p-4 font-mono text-xs text-slate-400">
        <div>go_version   : {info.runtime.go_version}</div>
        <div>goroutines   : {info.runtime.goroutines}</div>
        <div>heap_mb      : {info.runtime.heap_mb} MB</div>
        <div>eviction     : {info.eviction}</div>
        <div>version      : {info.version}</div>
      </div>
    </>
  );
}
