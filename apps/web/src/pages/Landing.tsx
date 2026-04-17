import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import {
  ArrowRight, Sparkles, Zap, Brain, Database, Activity, Check,
  Github, Terminal, Rocket, ServerCog, LineChart, ShieldCheck,
  Cpu, KeyRound,
} from "lucide-react";
import { api } from "../lib/api";
import { useCountUp } from "../lib/useCountUp";
import { SiteHeader, SiteFooter } from "../components/SiteHeader";
import { Code, C } from "../components/Code";
import { AnimatedBackground, Marquee } from "../components/AnimatedBackground";
import { Reveal } from "../components/Reveal";

type EngineStatus =
  | { kind: "checking" }
  | { kind: "online"; uptime: number; commands: number; keys: number; hitRate: number }
  | { kind: "offline" };

function useLiveStatus(): EngineStatus {
  const [status, setStatus] = useState<EngineStatus>({ kind: "checking" });
  useEffect(() => {
    let cancelled = false;
    const fetchOnce = () =>
      api
        .info()
        .then((info) => {
          if (cancelled) return;
          setStatus({
            kind: "online",
            uptime: info.uptime_seconds,
            commands: info.commands,
            keys: info.kv.keys,
            hitRate: info.semantic.hit_rate,
          });
        })
        .catch(() => {
          if (!cancelled) setStatus({ kind: "offline" });
        });
    fetchOnce();
    const t = setInterval(fetchOnce, 3000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, []);
  return status;
}

export default function Landing() {
  return (
    <div className="min-h-full">
      <SiteHeader />
      <Hero />
      <Marquees />
      <Features />
      <Demo />
      <Snippets />
      <Install />
      <CTA />
      <SiteFooter />
    </div>
  );
}

/* ─── Hero ───────────────────────────────────────────────────────── */
function Hero() {
  const status = useLiveStatus();
  return (
    <section className="relative overflow-hidden">
      <AnimatedBackground />
      <div className="mx-auto max-w-6xl px-6 pb-20 pt-20 md:pt-28">
        <div className="flex flex-col items-center text-center">
          <div className="animate-fade-up">
            <StatusPill status={status} />
          </div>

          <h1
            className="mt-6 text-4xl font-bold leading-[1.05] tracking-tight md:text-7xl animate-fade-up"
            style={{ animationDelay: "80ms" }}
          >
            <span className="block text-slate-100">The memory layer</span>
            <span className="block text-gradient">for AI applications</span>
          </h1>

          <p
            className="mt-6 max-w-2xl text-base leading-relaxed text-slate-400 md:text-lg animate-fade-up"
            style={{ animationDelay: "160ms" }}
          >
            Redis-compatible in-memory data store that understands the{" "}
            <span className="text-slate-200">meaning</span> of your queries.
            Ship semantic caching, LLM response reuse, and per-user memory —
            with a built-in real-time analytics dashboard.
          </p>

          <div
            className="mt-9 flex flex-wrap items-center justify-center gap-3 animate-fade-up"
            style={{ animationDelay: "240ms" }}
          >
            <Link to="/dashboard" className="btn-primary px-5 py-3 text-sm">
              Open Dashboard <ArrowRight size={14} />
            </Link>
            <Link
              to="/docs"
              className="btn-ghost border border-border px-5 py-3 text-sm hover:border-primary/40"
            >
              Read the Docs
            </Link>
            <a
              href="https://github.com/dhiravpatel/neurocache"
              target="_blank"
              rel="noreferrer"
              className="btn-ghost px-5 py-3 text-sm"
            >
              <Github size={14} /> Star on GitHub
            </a>
          </div>

          <div
            className="mt-7 flex items-center gap-2 rounded-full border border-border bg-surface/50
                       px-4 py-2 font-mono text-xs text-slate-400 backdrop-blur animate-fade-up"
            style={{ animationDelay: "320ms" }}
          >
            <Terminal size={12} className="text-primary" />
            curl -fsSL https://neurocache.dev/install.sh | sh
          </div>

          {/* Live mini-stats — animated when the engine is reachable */}
          <div
            className="mt-12 grid w-full max-w-3xl grid-cols-2 gap-3 md:grid-cols-4 animate-fade-up"
            style={{ animationDelay: "400ms" }}
          >
            <LiveStat label="Commands" value={status.kind === "online" ? status.commands : 0} icon={Cpu} />
            <LiveStat label="KV Keys"   value={status.kind === "online" ? status.keys : 0}     icon={KeyRound} />
            <LiveStat label="Uptime"    value={status.kind === "online" ? Math.floor(status.uptime) : 0} suffix="s" icon={Activity} />
            <LiveStat label="Hit Rate"  value={status.kind === "online" ? Math.round(status.hitRate * 100) : 0} suffix="%" icon={Sparkles} />
          </div>
        </div>

        {/* Before / After */}
        <div className="mt-20 grid gap-4 md:grid-cols-2">
          <Reveal>
            <div className="card p-5 transition-transform duration-300 hover:-translate-y-1">
              <div className="mb-3 flex items-center gap-2 text-xs uppercase tracking-wider text-slate-500">
                <Database size={13} /> Standard Redis
              </div>
              <Code lang="redis">{`SET "best phone under 50k" "iPhone 13"
GET "best phone under 50k"
# → "iPhone 13"   ✓

GET "good phones below 50000"
# → (nil)         ✗  different string`}</Code>
            </div>
          </Reveal>
          <Reveal delay={120}>
            <div className="card glow-border p-5 transition-transform duration-300 hover:-translate-y-1">
              <div className="mb-3 flex items-center gap-2 text-xs uppercase tracking-wider text-primary">
                <Sparkles size={13} /> NeuroCache
              </div>
              <Code lang="redis">{`SEMANTIC_SET "best phone under 50k" "iPhone 13"
SEMANTIC_GET "best phone under 50k"
# → "iPhone 13"   ✓

SEMANTIC_GET "good phones below 50000"
# → "iPhone 13"   ✓  understands meaning`}</Code>
            </div>
          </Reveal>
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
      <div className="relative inline-flex items-center gap-2 rounded-full border border-emerald-500/40
                      bg-emerald-500/10 px-3 py-1 text-xs font-medium text-emerald-500 dark:text-emerald-400">
        <span className="relative flex h-2 w-2">
          <span className="absolute inline-flex h-full w-full animate-pulse-ring rounded-full bg-emerald-400" />
          <span className="relative inline-flex h-2 w-2 rounded-full bg-emerald-400" />
        </span>
        engine online
      </div>
    );
  }
  return (
    <div className="pill">
      <span className="h-1.5 w-1.5 rounded-full bg-slate-500" />
      not installed yet — try the installer below
    </div>
  );
}

function LiveStat({
  label, value, suffix, icon: Icon,
}: { label: string; value: number; suffix?: string; icon: typeof Cpu }) {
  const animated = useCountUp(value, 700);
  return (
    <div className="card p-3 text-left transition-colors hover:border-primary/30">
      <div className="flex items-center gap-1.5 text-[11px] uppercase tracking-wider text-slate-500">
        <Icon size={12} /> {label}
      </div>
      <div className="mt-1 font-mono text-2xl font-semibold tabular-nums text-slate-100">
        {new Intl.NumberFormat().format(animated)}
        {suffix && <span className="text-slate-400">{suffix}</span>}
      </div>
    </div>
  );
}

/* ─── Marquee row of supported clients & commands ───────────────── */
function Marquees() {
  const items = [
    "redis-cli", "ioredis", "go-redis", "redis-py", "node-redis",
    "SEMANTIC_GET", "CACHE_LLM", "MEMORY_QUERY", "INCR", "EXPIRE",
    "RESP protocol", "OpenAI", "Anthropic", "HNSW index",
  ];
  return (
    <section className="border-y border-border bg-surface/40 py-6">
      <Marquee>
        {items.map((t) => (
          <span
            key={t}
            className="inline-flex items-center gap-2 rounded-full border border-border bg-bg/50 px-4 py-1.5 text-sm font-mono text-slate-400"
          >
            {t}
          </span>
        ))}
      </Marquee>
    </section>
  );
}

/* ─── Feature grid ───────────────────────────────────────────────── */
const features = [
  { icon: Sparkles,    title: "Semantic Cache",    body: "Store values by meaning. Paraphrased queries hit the cache via a built-in embedding model and a fast vector index." },
  { icon: Zap,         title: "LLM Response Cache", body: "Wrap your OpenAI / Anthropic calls. Similar prompts reuse cached answers — watch cost savings accumulate live." },
  { icon: Brain,       title: "Per-user Memory",    body: "MEMORY_ADD / MEMORY_QUERY gives every user persistent context your LLM can reason over, across sessions." },
  { icon: Database,    title: "Redis Compatible",   body: "Speaks RESP on :6379. redis-cli, ioredis, go-redis, redis-py — every existing client just works." },
  { icon: LineChart,   title: "Built-in Analytics", body: "Command rate, hit-rate timeline, p50/p95 latency, hot keys and LLM savings — shipped in the engine binary." },
  { icon: ShieldCheck, title: "Single Binary",      body: "One docker run. Dashboard + RESP server + HTTP API in a 20 MB image. No external dependencies." },
];

function Features() {
  return (
    <section className="mx-auto max-w-6xl px-6 py-20">
      <Reveal className="mb-12 text-center">
        <div className="pill inline-flex">Features</div>
        <h2 className="mt-3 text-3xl font-semibold tracking-tight md:text-4xl">
          Everything your AI app needs
        </h2>
        <p className="mt-2 text-sm text-slate-500">
          Designed around the patterns you actually run against LLMs.
        </p>
      </Reveal>
      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
        {features.map((f, i) => (
          <Reveal key={f.title} delay={i * 60}>
            <div className="card group relative h-full overflow-hidden p-5 transition-all duration-300
                            hover:-translate-y-1 hover:border-primary/40
                            hover:shadow-[0_0_36px_-12px_rgb(var(--primary)/0.45)]">
              <div className="pointer-events-none absolute -right-8 -top-8 h-28 w-28 rounded-full bg-primary/10 blur-2xl
                              opacity-0 transition-opacity duration-500 group-hover:opacity-100" />
              <div className="relative">
                <div className="inline-flex h-9 w-9 items-center justify-center rounded-lg
                                bg-primary/10 text-primary ring-1 ring-primary/20">
                  <f.icon size={16} />
                </div>
                <div className="mt-4 text-[15px] font-semibold text-slate-100">{f.title}</div>
                <p className="mt-1.5 text-sm leading-relaxed text-slate-400">{f.body}</p>
              </div>
            </div>
          </Reveal>
        ))}
      </div>
    </section>
  );
}

/* ─── Live demo / fake dashboard preview ─────────────────────────── */
function Demo() {
  return (
    <section className="mx-auto max-w-6xl px-6 py-20">
      <div className="grid gap-12 md:grid-cols-[1fr_1.2fr] md:items-center">
        <Reveal>
          <div className="pill inline-flex"><Activity size={12} /> Live analytics</div>
          <h2 className="mt-3 text-3xl font-semibold tracking-tight md:text-4xl">
            See what your cache is actually doing
          </h2>
          <p className="mt-3 text-sm leading-relaxed text-slate-400">
            A real-time dashboard is built into the engine. Watch commands
            per second, hit rate, p50/p95 latency, your hottest keys, and the
            running dollar estimate of how much you've saved by not calling
            the LLM twice.
          </p>
          <ul className="mt-5 space-y-2 text-sm text-slate-300">
            {[
              "Commands-per-second timeline (rolling 60s)",
              "Semantic + LLM cache hit rate",
              "p50 / p95 latency distribution",
              "Hot keys with hit counts",
              "Estimated LLM savings ($ per hit)",
            ].map((t, i) => (
              <li key={t} className="flex items-center gap-2 animate-fade-up" style={{ animationDelay: `${i * 60}ms` }}>
                <Check size={14} className="text-emerald-500 dark:text-emerald-400" /> {t}
              </li>
            ))}
          </ul>
          <div className="mt-7 flex gap-3">
            <Link to="/dashboard/analytics" className="btn-primary px-4 py-2 text-sm">
              View Analytics <ArrowRight size={14} />
            </Link>
            <Link to="/docs/quickstart" className="btn-ghost border border-border px-4 py-2 text-sm">
              Quick Start
            </Link>
          </div>
        </Reveal>

        <Reveal delay={120}>
          <DashboardPreview />
        </Reveal>
      </div>
    </section>
  );
}

function DashboardPreview() {
  // Static-but-dynamic-looking sparkline values — the SVG path is fixed
  // but the gradient + labels animate to give a "live" feel.
  return (
    <div className="card overflow-hidden p-0 transition-transform duration-500 hover:-translate-y-1">
      <div className="flex items-center gap-1.5 border-b border-border px-3 py-2">
        <span className="h-2.5 w-2.5 rounded-full bg-rose-500/60" />
        <span className="h-2.5 w-2.5 rounded-full bg-amber-500/60" />
        <span className="h-2.5 w-2.5 rounded-full bg-emerald-500/60" />
        <span className="ml-3 text-xs text-slate-500">neurocache · /analytics</span>
        <span className="ml-auto inline-flex items-center gap-1 text-[10px] text-emerald-500 dark:text-emerald-400">
          <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-emerald-500 dark:bg-emerald-400" /> live
        </span>
      </div>

      <div className="grid grid-cols-2 gap-3 p-4">
        <DemoMetric label="Commands"    value={12482} color="text-primary" />
        <DemoMetric label="Hit Rate"    value={87}    color="text-accent" suffix="%" />
        <DemoMetric label="LLM Savings" value={241}   color="text-emerald-500 dark:text-emerald-400" prefix="$" divisor={100} />
        <DemoMetric label="p95 Latency" value={31}    color="text-slate-200" suffix=" ms" divisor={10} />
      </div>

      <div className="px-4 pb-5">
        <svg viewBox="0 0 400 100" className="h-28 w-full">
          <defs>
            <linearGradient id="sparkFill" x1="0" x2="0" y1="0" y2="1">
              <stop offset="0%" stopColor="rgb(var(--primary))" stopOpacity=".55" />
              <stop offset="100%" stopColor="rgb(var(--primary))" stopOpacity="0" />
            </linearGradient>
            <linearGradient id="sparkLine" x1="0" x2="1" y1="0" y2="0">
              <stop offset="0%"   stopColor="rgb(var(--primary))" />
              <stop offset="100%" stopColor="rgb(var(--accent))" />
            </linearGradient>
          </defs>
          <path
            d="M0,70 C40,60 60,30 100,40 C140,50 160,12 200,20 C240,28 260,55 300,48 C340,42 360,18 400,30 L400,100 L0,100 Z"
            fill="url(#sparkFill)"
          />
          <path
            d="M0,70 C40,60 60,30 100,40 C140,50 160,12 200,20 C240,28 260,55 300,48 C340,42 360,18 400,30"
            stroke="url(#sparkLine)"
            strokeWidth="2.5"
            fill="none"
          />
          {/* moving cursor dot */}
          <circle r="4" fill="rgb(var(--accent))">
            <animateMotion dur="6s" repeatCount="indefinite" rotate="auto"
              path="M0,70 C40,60 60,30 100,40 C140,50 160,12 200,20 C240,28 260,55 300,48 C340,42 360,18 400,30" />
          </circle>
        </svg>
      </div>
    </div>
  );
}

function DemoMetric({
  label, value, color, prefix, suffix, divisor,
}: {
  label: string; value: number; color: string;
  prefix?: string; suffix?: string; divisor?: number;
}) {
  const animated = useCountUp(value, 1200);
  const display = divisor
    ? (animated / divisor).toFixed(2)
    : new Intl.NumberFormat().format(animated);
  return (
    <div className="rounded-md border border-border bg-bg/50 p-3">
      <div className="text-[11px] uppercase tracking-wider text-slate-500">{label}</div>
      <div className={`mt-1 font-mono text-xl font-semibold tabular-nums ${color}`}>
        {prefix}{display}{suffix}
      </div>
    </div>
  );
}

/* ─── Three-column code snippets ─────────────────────────────────── */
function Snippets() {
  const [tab, setTab] = useState<"semantic" | "llm" | "memory">("semantic");
  const samples = {
    semantic: `// store + retrieve by meaning
await cache.semanticSet(
  "best backend language for APIs",
  "Go is ideal for high-performance APIs",
);

const hit = await cache.semanticGet(
  "what language for backend services",
);
// → { hit: true, value: "Go is ideal for…", score: 0.83 }`,
    llm: `// wrap any LLM call in one helper
const { value, hit } = await cache.cacheLLMAround(
  prompt,
  async () => openai.chat.completions
    .create({ model: "gpt-4o-mini",
              messages: [{ role: "user", content: prompt }] })
    .then(r => r.choices[0].message.content!),
  { threshold: 0.88 },
);`,
    memory: `// per-user memory + semantic recall
await cache.memory.add("user:dhirav", "Prefers Go + React + Tailwind");
await cache.memory.add("user:dhirav", "Building NeuroCache");

const { context } = await cache.memory.query(
  "user:dhirav",
  "what is this user working on?",
);
// → "Based on stored context: Building NeuroCache; ..."`,
  };

  return (
    <section className="mx-auto max-w-6xl px-6 py-20">
      <Reveal className="mb-8 text-center">
        <div className="pill inline-flex">SDK</div>
        <h2 className="mt-3 text-3xl font-semibold tracking-tight md:text-4xl">
          A few lines, everywhere it matters
        </h2>
        <p className="mt-2 text-sm text-slate-500">
          One TypeScript SDK, three superpowers for your AI app.
        </p>
      </Reveal>

      <Reveal>
        <div className="card p-4">
          <div className="mb-3 flex flex-wrap gap-1 border-b border-border">
            {([
              ["semantic", "Semantic Cache"],
              ["llm",      "LLM Response Cache"],
              ["memory",   "Per-user Memory"],
            ] as const).map(([k, label]) => (
              <button
                key={k}
                onClick={() => setTab(k)}
                className={`relative border-b-2 px-3 py-2 text-sm transition-colors ${
                  tab === k
                    ? "border-primary text-slate-100"
                    : "border-transparent text-slate-500 hover:text-slate-200"
                }`}
              >
                {label}
              </button>
            ))}
          </div>
          <Code lang="ts">{samples[tab]}</Code>
        </div>
      </Reveal>
    </section>
  );
}

/* ─── Install ─────────────────────────────────────────────────────── */
function Install() {
  const [tab, setTab] = useState<"curl" | "docker" | "compose">("curl");
  return (
    <section className="mx-auto max-w-6xl px-6 py-20">
      <Reveal className="mb-10 text-center">
        <div className="pill inline-flex"><Rocket size={12} /> Install</div>
        <h2 className="mt-3 text-3xl font-semibold tracking-tight md:text-4xl">
          One install, one process
        </h2>
        <p className="mt-2 text-sm text-slate-500">
          Ships as a single container — dashboard, API, and RESP server bundled together.
        </p>
      </Reveal>

      <Reveal>
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
      </Reveal>

      <div className="mt-6 grid gap-3 md:grid-cols-3">
        <Reveal delay={0}>
          <InfoCard icon={Terminal} title="Port 6379 — RESP">
            Every Redis client works. <C>redis-cli -p 6379 ping</C>
          </InfoCard>
        </Reveal>
        <Reveal delay={80}>
          <InfoCard icon={ServerCog} title="Port 8080 — HTTP + UI">
            Dashboard + JSON API on the same port. Great for apps and ops.
          </InfoCard>
        </Reveal>
        <Reveal delay={160}>
          <InfoCard icon={Brain} title="Data persisted">
            Volume <C>/data</C> holds the AOF log. Survives container restarts.
          </InfoCard>
        </Reveal>
      </div>
    </section>
  );
}

function InfoCard({
  icon: Icon, title, children,
}: { icon: typeof Terminal; title: string; children: React.ReactNode }) {
  return (
    <div className="card h-full p-4 transition-all duration-300 hover:-translate-y-1 hover:border-primary/40">
      <Icon size={16} className="text-primary" />
      <div className="mt-2 text-sm font-medium text-slate-100">{title}</div>
      <div className="mt-1 text-xs leading-relaxed text-slate-400">{children}</div>
    </div>
  );
}

/* ─── Closing CTA ─────────────────────────────────────────────────── */
function CTA() {
  return (
    <section className="mx-auto max-w-6xl px-6 py-20">
      <Reveal>
        <div className="card relative overflow-hidden p-12 text-center">
          <div
            aria-hidden
            className="pointer-events-none absolute inset-0 -z-10 opacity-70"
            style={{
              background:
                "radial-gradient(ellipse 60% 80% at 50% 50%, rgb(var(--primary) / .18), transparent 70%)",
            }}
          />
          <div className="pointer-events-none absolute -top-10 left-1/2 -z-10 h-40 w-[80%] -translate-x-1/2 rounded-full bg-primary/30 blur-3xl animate-glow" />
          <h2 className="text-3xl font-semibold tracking-tight md:text-4xl">
            Give your AI app a memory.
          </h2>
          <p className="mx-auto mt-3 max-w-xl text-sm text-slate-400">
            Self-host in a single container. Free, MIT licensed, no account required.
          </p>
          <div className="mt-7 flex flex-wrap items-center justify-center gap-3">
            <Link to="/docs/installation" className="btn-primary px-5 py-3 text-sm">
              Get Started <ArrowRight size={14} />
            </Link>
            <Link to="/docs" className="btn-ghost border border-border px-5 py-3 text-sm">
              Read the Docs
            </Link>
          </div>
        </div>
      </Reveal>
    </section>
  );
}
