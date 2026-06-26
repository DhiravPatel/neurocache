import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function ConversationsDocs() {
  return (
    <>
      <h1>Conversations</h1>
      <p className="lead">
        Multi-turn chat context, server-side. Append turns to a session, read a
        token-bounded window for the next LLM call, and compress old history into
        a running summary — so every worker and device shares one session state
        instead of re-sending the whole transcript.
      </p>

      <h2>The lifecycle</h2>
      <Code lang="bash">{`# Append turns to a session key
curl localhost:8080/api/conv/session:42 -d '{"role":"user","content":"hi"}'
curl localhost:8080/api/conv/session:42 -d '{"role":"assistant","content":"hello!"}'

# Read the recent window (optionally cap by tokens) to build the next prompt
curl "localhost:8080/api/conv/session:42?max_tokens=2000"
# → {"turns":[{"role","content","tokens","created_at"}, …]}

# Compress: replace old turns with a summary, keep the most recent tokens
curl localhost:8080/api/conv/session:42/summarize \\
  -d '{"summary":"User greeted; assistant responded.","keep_tokens":500}'

curl localhost:8080/api/conv           # active sessions + count
curl -X DELETE localhost:8080/api/conv/session:42`}</Code>

      <h2>From the SDK</h2>
      <Code lang="ts">{`const c = cache.conversations;

await c.append("session:42", "user", "What's my order status?");
await c.append("session:42", "assistant", "It ships tomorrow.");

// Build the next prompt from the windowed history
const { turns } = await c.window("session:42", 2000);
const messages = turns.map((t) => ({ role: t.role, content: t.content }));

// Keep context bounded as the chat grows
await c.summarize("session:42", "Order shipped tomorrow.", 500);`}</Code>
      <Callout type="tip" title="Summarize to stay within the context window">
        <code>summarize</code> drops older turns and prepends your summary, so a
        long-running session keeps its gist without blowing the model's context
        limit. Pair it with <Link to="/docs/memory">User Memory</Link> for
        durable per-user facts that outlive a single session.
      </Callout>

      <h2>In the dashboard</h2>
      <p>
        The <Link to="/dashboard/conversations">Conversations page</Link> lists
        active sessions, shows the turn-by-turn transcript with token counts, and
        lets you append turns and summarize live.
      </p>
    </>
  );
}
