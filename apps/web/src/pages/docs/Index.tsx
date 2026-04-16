import { Link } from "react-router-dom";
import { Rocket, BookOpen, Sparkles, Zap, Brain, Database, Gauge, Wrench } from "lucide-react";
import { Code } from "../../components/Code";

export default function DocsIndex() {
  return (
    <>
      <h1>Introduction</h1>
      <p className="lead">
        NeuroCache is an AI-aware, Redis-compatible in-memory data store.
        It does everything Redis does for KV caching, plus three commands
        that make LLM applications dramatically cheaper and more coherent:
        semantic caching, LLM response caching, and per-user memory.
      </p>

      <h2>Why another cache?</h2>
      <p>
        Standard Redis matches keys by exact bytes. That's fine for session
        tokens and counters, but it falls apart the moment you want to cache
        <em> anything a user typed</em> — natural language varies, but the
        meaning is the same. Similarly, vector databases can find similar
        items but were not built to also act as a low-latency KV store, and
        they don't understand TTLs, eviction, or the fact that you want
        <em> last-write-wins semantics</em>, not "append another row".
      </p>
      <p>
        NeuroCache is a single in-memory engine that speaks both worlds:
        exact-match KV on the hot path, and a semantic index alongside for
        meaning-based retrieval.
      </p>

      <h2>What you get</h2>
      <div className="not-prose mt-4 grid gap-3 md:grid-cols-2">
        <Card to="/docs/installation"   icon={Rocket}   title="Install">
          Docker, docker-compose or a one-line curl script.
        </Card>
        <Card to="/docs/quickstart"     icon={BookOpen} title="Quick Start">
          From zero to first semantic cache hit in five minutes.
        </Card>
        <Card to="/docs/semantic-cache" icon={Sparkles} title="Semantic Cache">
          <code>SEMANTIC_SET</code> / <code>SEMANTIC_GET</code> and how scoring works.
        </Card>
        <Card to="/docs/llm-cache"      icon={Zap}      title="LLM Cache">
          Wrap OpenAI / Anthropic calls and watch hit-rate climb.
        </Card>
        <Card to="/docs/memory"         icon={Brain}    title="User Memory">
          Persistent per-user context with semantic recall.
        </Card>
        <Card to="/docs/commands"       icon={Database} title="Commands">
          Reference for every command, both Redis and AI-native.
        </Card>
        <Card to="/docs/configuration"  icon={Wrench}   title="Configuration">
          Environment variables, eviction policies, CORS, logging.
        </Card>
        <Card to="/docs/deployment"     icon={Gauge}    title="Deployment">
          Render, Fly, Railway, self-host — production checklists.
        </Card>
      </div>

      <h2 className="mt-10">Try it right now</h2>
      <p>If you already ran the installer (or are reading this from the embedded dashboard),
        open a terminal and paste:</p>
      <Code lang="bash">{`redis-cli -p 6379 PING            # → PONG
redis-cli -p 6379 SEMANTIC_SET "best backend language" "Go"
redis-cli -p 6379 SEMANTIC_GET "top programming language for APIs"
# → "Go"`}</Code>
      <p className="mt-4 text-sm text-slate-500">
        Not installed yet? Jump to{" "}
        <Link to="/docs/installation">Installation</Link> or try commands
        in the <Link to="/dashboard/playground">Playground</Link>.
      </p>
    </>
  );
}

function Card({
  to, icon: Icon, title, children,
}: {
  to: string; icon: typeof Rocket; title: string; children: React.ReactNode;
}) {
  return (
    <Link
      to={to}
      className="card block p-4 no-underline transition-colors hover:border-primary/40"
    >
      <Icon size={16} className="text-primary" />
      <div className="mt-2 text-[15px] font-semibold text-slate-100">{title}</div>
      <div className="mt-1 text-sm text-slate-400">{children}</div>
    </Link>
  );
}
