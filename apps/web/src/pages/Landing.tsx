import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import {
  ArrowRight, Sparkles, Zap, Brain, Database, Activity, Check,
  Github, Terminal, Rocket, ServerCog, LineChart, ShieldCheck,
} from "lucide-react";
import { api } from "../lib/api";
import { SiteHeader, SiteFooter } from "../components/SiteHeader";
import { Code, C } from "../components/Code";

type EngineStatus =
  | { kind: "checking" }
  | { kind: "online"; uptime: number; commands: number; keys: number }
  | { kind: "offline" };

function useEngineStatus(): EngineStatus {
  const [status, setStatus] = useState<EngineStatus>({ kind: "checking" });
  useEffect(() => {
    let cancelled = false;
    api
      .info()
      .then((info) => {
        if (!cancelled) {
          setStatus({
            kind: "online",
            uptime: info.uptime_seconds,
            commands: info.commands,
            keys: info.kv.keys,
          });
        }
      })
      .catch(() => {
        if (!cancelled) setStatus({ kind: "offline" });
      });
    return () => {
      cancelled = true;
    };
  }, []);
  return status;
}

export default function Landing() {
  return (
    <div className="min-h-full">
      <SiteHeader />
      <Hero />
      <Features />
      <LiveDemo />
      <Install />
      <CTA />
      <SiteFooter />
    </div>
  );
}

function Hero() {
  const status = useEngineStatus();
  return (
    <section className="relative overflow-hidden">
      <div
        aria-hidden
        className="pointer-events-none absolute inset-x-0 top-0 -z-10 h-[600px]"
        style={{
          background:
            "radial-gradient(ellipse 60% 50% at 50% 0%, rgba(124,92,255,.22), transparent 70%), radial-gradient(ellipse 40% 40% at 80% 10%, rgba(34,211,238,.12), transparent)",
        }}
      />
      <div className="mx-auto max-w-6xl px-6 pb-16 pt-16 md:pt-24">
        <div className="flex flex-col items-center text-center">
          <StatusPill status={status} />
          <h1 className="mt-6 bg-gradient-to-br from-white via-slate-200 to-slate-500 bg-clip-text text-4xl font-bold leading-tight tracking-tight text-transparent md:text-6xl">
            The memory layer for
            <br />
            <span className="bg-gradient-to-r from-primary via-fuchsia-400 to-accent bg-clip-text text-transparent">
              AI applications
            </span>
          </h1>
          <p className="mt-5 max-w-2xl text-base leading-relaxed text-slate-400 md:text-lg">
            Redis-compatible in-memory data store that understands the{" "}
            <span className="text-slate-200">meaning</span> of your queries.
            Ship semantic caching, LLM response reuse, and per-user memory —
            with a built-in analytics dashboard.
          </p>
          <div className="mt-8 flex flex-wrap items-center justify-center gap-3">
            <Link to="/dashboard" className="btn-primary px-5 py-2.5 text-sm">
              Open Dashboard <ArrowRight size={14} />
            </Link>
            <Link to="/docs" className="btn-ghost border border-border px-5 py-2.5 text-sm">
              Read Docs
            </Link>
            <a
              href="https://github.com/dhiravpatel/neurocache"
              target="_blank"
              rel="noreferrer"
              className="btn-ghost px-5 py-2.5 text-sm"
            >
              <Github size={14} /> Star on GitHub
            </a>
          </div>
          <div className="mt-6 text-xs text-slate-500">
            <span className="font-mono">curl -fsSL https://neurocache.dev/install.sh | sh</span>
          </div>
        </div>

        {/* Before / After side-by-side */}
        <div className="mt-16 grid gap-4 md:grid-cols-2">
          <div className="card p-5">
            <div className="mb-3 flex items-center gap-2 text-xs uppercase tracking-wider text-slate-500">
              <Database size={13} /> Standard Redis
            </div>
            <Code lang="redis">{`SET "best phone under 50k" "iPhone 13"
GET "best phone under 50k"
# → "iPhone 13"   ✓

GET "good phones below 50000"
# → (nil)         ✗  different string, cache miss`}</Code>
          </div>
          <div className="card border-primary/40 p-5">
            <div className="mb-3 flex items-center gap-2 text-xs uppercase tracking-wider text-primary">
              <Sparkles size={13} /> NeuroCache
            </div>
            <Code lang="redis">{`SEMANTIC_SET "best phone under 50k" "iPhone 13"
SEMANTIC_GET "best phone under 50k"
# → "iPhone 13"   ✓

SEMANTIC_GET "good phones below 50000"
# → "iPhone 13"   ✓  understands meaning`}</Code>
          </div>
        </div>
      </div>
    </section>
  );
}

function StatusPill({ status }: { status: EngineStatus }) {
  if (status.kind === "checking") {
    return (
      <div className="pill">
        <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-slate-400" />
        checking engine…
      </div>
    );
  }
  if (status.kind === "online") {
    return (
      <div className="pill text-emerald-400">
        <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-emerald-400" />
        engine online · {fmt(status.commands)} cmds · {fmt(status.keys)} keys
      </div>
    );
  }
  return (
    <div className="pill text-slate-400">
      <span className="h-1.5 w-1.5 rounded-full bg-slate-500" />
      not installed yet — run the installer below
    </div>
  );
}

function fmt(n: number) { return new Intl.NumberFormat().format(n); }

const features = [
  {
    icon: Sparkles,
    title: "Semantic Cache",
    body: "Store values by their meaning. Paraphrased queries still hit the cache via a built-in embedding model and HNSW-style index.",
  },
  {
    icon: Zap,
    title: "LLM Response Cache",
    body: "Wrap your OpenAI / Anthropic calls. Similar prompts reuse cached answers — watch cost savings accumulate in real time.",
  },
  {
    icon: Brain,
    title: "Per-user Memory",
    body: "MEMORY_ADD / MEMORY_QUERY gives every user persistent context your LLM can reason over, across sessions.",
  },
  {
    icon: Database,
    title: "Redis Compatible",
    body: "Speaks RESP on :6379. redis-cli, ioredis, go-redis, redis-py — every existing client just works.",
  },
  {
    icon: LineChart,
    title: "Built-in Analytics",
    body: "Command rate, hit-rate timeline, p50/p95 latency, hot keys and LLM savings — shipped in the engine binary.",
  },
  {
    icon: ShieldCheck,
    title: "Single Binary",
    body: "One docker run. Dashboard + RESP server + HTTP API in a 20 MB image. No external dependencies.",
  },
];

function Features() {
  return (
    <section className="mx-auto max-w-6xl px-6 py-16">
      <div className="mb-10 text-center">
        <div className="pill inline-flex text-slate-400">Features</div>
        <h2 className="mt-3 text-3xl font-semibold tracking-tight">Everything your AI app needs</h2>
        <p className="mt-2 text-sm text-slate-500">
          Designed around the patterns you actually run against LLMs.
        </p>
      </div>
      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
        {features.map((f) => (
          <div key={f.title} className="card p-5 transition-colors hover:border-primary/40">
            <f.icon size={18} className="text-primary" />
            <div className="mt-3 text-[15px] font-semibold text-slate-100">{f.title}</div>
            <p className="mt-1.5 text-sm leading-relaxed text-slate-400">{f.body}</p>
          </div>
        ))}
      </div>
    </section>
  );
}

function LiveDemo() {
  return (
    <section className="mx-auto max-w-6xl px-6 py-16">
      <div className="grid gap-10 md:grid-cols-[1fr_1.2fr] md:items-center">
        <div>
          <div className="pill inline-flex text-slate-400">
            <Activity size={12} /> Live analytics
          </div>
          <h2 className="mt-3 text-3xl font-semibold tracking-tight">
            See what your cache is actually doing
          </h2>
          <p className="mt-3 text-sm leading-relaxed text-slate-400">
            A real-time dashboard is built into the engine. Watch commands per
            second, hit rate, p50/p95 latency, your hottest keys, and the
            running dollar estimate of how much you've saved by not calling
            the LLM twice.
          </p>
          <ul className="mt-5 space-y-2 text-sm">
            {[
              "Commands-per-second timeline (rolling 60s)",
              "Semantic + LLM cache hit rate",
              "p50 / p95 latency distribution",
              "Hot keys with hit counts",
              "Estimated LLM savings ($ per hit)",
            ].map((t) => (
              <li key={t} className="flex items-center gap-2 text-slate-300">
                <Check size={14} className="text-emerald-400" /> {t}
              </li>
            ))}
          </ul>
          <div className="mt-6 flex gap-3">
            <Link to="/dashboard/analytics" className="btn-primary px-4 py-2 text-sm">
              View Analytics <ArrowRight size={14} />
            </Link>
            <Link
              to="/docs/quickstart"
              className="btn-ghost border border-border px-4 py-2 text-sm"
            >
              Quick Start
            </Link>
          </div>
        </div>
        <div className="card overflow-hidden p-0">
          <div className="flex items-center gap-1.5 border-b border-border px-3 py-2">
            <span className="h-2.5 w-2.5 rounded-full bg-rose-500/60" />
            <span className="h-2.5 w-2.5 rounded-full bg-amber-500/60" />
            <span className="h-2.5 w-2.5 rounded-full bg-emerald-500/60" />
            <span className="ml-3 text-xs text-slate-500">neurocache dashboard · /analytics</span>
          </div>
          <div className="grid grid-cols-2 gap-3 p-4">
            {[
              { label: "Commands", value: "12,482", color: "text-primary" },
              { label: "Hit Rate",    value: "87%",    color: "text-accent" },
              { label: "LLM Savings", value: "$2.41",  color: "text-emerald-400" },
              { label: "p95 Latency", value: "3.1 ms", color: "text-slate-200" },
            ].map((m) => (
              <div key={m.label} className="rounded-md border border-border bg-bg/50 p-3">
                <div className="text-[11px] uppercase tracking-wider text-slate-500">
                  {m.label}
                </div>
                <div className={`mt-1 text-xl font-semibold tabular-nums ${m.color}`}>
                  {m.value}
                </div>
              </div>
            ))}
          </div>
          <div className="px-4 pb-4">
            <svg viewBox="0 0 400 80" className="h-24 w-full">
              <defs>
                <linearGradient id="grad" x1="0" x2="0" y1="0" y2="1">
                  <stop offset="0%" stopColor="#7c5cff" stopOpacity=".5" />
                  <stop offset="100%" stopColor="#7c5cff" stopOpacity="0" />
                </linearGradient>
              </defs>
              <path
                d="M0,55 C40,50 60,30 100,35 C140,40 160,10 200,15 C240,20 260,45 300,40 C340,35 360,15 400,25 L400,80 L0,80 Z"
                fill="url(#grad)"
              />
              <path
                d="M0,55 C40,50 60,30 100,35 C140,40 160,10 200,15 C240,20 260,45 300,40 C340,35 360,15 400,25"
                stroke="#7c5cff"
                strokeWidth="2"
                fill="none"
              />
            </svg>
          </div>
        </div>
      </div>
    </section>
  );
}

function Install() {
  const [tab, setTab] = useState<"curl" | "docker" | "compose">("curl");
  return (
    <section className="mx-auto max-w-6xl px-6 py-16">
      <div className="mb-8 text-center">
        <div className="pill inline-flex text-slate-400">
          <Rocket size={12} /> Install
        </div>
        <h2 className="mt-3 text-3xl font-semibold tracking-tight">One install, one process</h2>
        <p className="mt-2 text-sm text-slate-500">
          Ships as a single container — dashboard, API, and RESP server bundled together.
        </p>
      </div>

      <div className="card p-4">
        <div className="mb-3 flex gap-1 border-b border-border">
          {(["curl", "docker", "compose"] as const).map((t) => (
            <button
              key={t}
              onClick={() => setTab(t)}
              className={`border-b-2 px-3 py-2 text-sm transition-colors ${
                tab === t
                  ? "border-primary text-slate-100"
                  : "border-transparent text-slate-500 hover:text-slate-200"
              }`}
            >
              {t === "curl" ? "curl | sh" : t === "docker" ? "docker run" : "docker-compose"}
            </button>
          ))}
        </div>
        {tab === "curl" ? (
          <Code lang="bash">{`curl -fsSL https://neurocache.dev/install.sh | sh

# Then open http://localhost:8080`}</Code>
        ) : tab === "docker" ? (
          <Code lang="bash">{`docker run -d \\
  --name neurocache \\
  -p 8080:8080 \\
  -p 6379:6379 \\
  -v neurocache-data:/data \\
  neurocache/engine:latest`}</Code>
        ) : (
          <Code lang="yaml">{`services:
  neurocache:
    image: neurocache/engine:latest
    ports:
      - "8080:8080"   # dashboard + HTTP API
      - "6379:6379"   # RESP (redis-cli compatible)
    volumes:
      - neurocache-data:/data
    restart: unless-stopped
volumes:
  neurocache-data:`}</Code>
        )}
      </div>

      <div className="mt-6 grid gap-3 md:grid-cols-3">
        <InfoCard icon={Terminal} title="Port 6379 — RESP">
          Every Redis client works. <C>redis-cli -p 6379 ping</C>
        </InfoCard>
        <InfoCard icon={ServerCog} title="Port 8080 — HTTP + UI">
          Dashboard + JSON API on the same port. Great for apps and ops.
        </InfoCard>
        <InfoCard icon={Brain} title="Data persisted">
          Volume <C>/data</C> holds the AOF log. Survives container restarts.
        </InfoCard>
      </div>
    </section>
  );
}

function InfoCard({
  icon: Icon,
  title,
  children,
}: {
  icon: typeof Terminal;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div className="card p-4">
      <Icon size={16} className="text-primary" />
      <div className="mt-2 text-sm font-medium text-slate-100">{title}</div>
      <div className="mt-1 text-xs leading-relaxed text-slate-400">{children}</div>
    </div>
  );
}

function CTA() {
  return (
    <section className="mx-auto max-w-6xl px-6 py-16">
      <div className="card relative overflow-hidden p-10 text-center">
        <div
          aria-hidden
          className="pointer-events-none absolute inset-0 -z-10"
          style={{
            background:
              "radial-gradient(ellipse 80% 80% at 50% 50%, rgba(124,92,255,.14), transparent)",
          }}
        />
        <h2 className="text-3xl font-semibold tracking-tight">Give your AI app a memory.</h2>
        <p className="mx-auto mt-3 max-w-xl text-sm text-slate-400">
          Self-host in a single container. Free, MIT licensed, no account
          required.
        </p>
        <div className="mt-6 flex flex-wrap items-center justify-center gap-3">
          <Link to="/docs/installation" className="btn-primary px-5 py-2.5 text-sm">
            Get Started <ArrowRight size={14} />
          </Link>
          <Link to="/docs" className="btn-ghost border border-border px-5 py-2.5 text-sm">
            Read the Docs
          </Link>
        </div>
      </div>
    </section>
  );
}
