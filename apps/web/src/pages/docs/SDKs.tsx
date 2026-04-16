import { Code } from "../../components/Code";

export default function SDKs() {
  return (
    <>
      <h1>SDKs & Clients</h1>
      <p className="lead">
        NeuroCache ships with a TypeScript SDK. Because it also speaks RESP
        on port 6379, every Redis client in every language works for the
        standard commands — the AI-native commands go through each
        client's generic <code>command()</code> / <code>do()</code> API.
      </p>

      <h2>TypeScript / JavaScript</h2>
      <Code lang="bash">{`pnpm add @neurocache/sdk
# or: npm i @neurocache/sdk / yarn add @neurocache/sdk`}</Code>
      <Code lang="ts">{`import { NeuroCache } from "@neurocache/sdk";

const cache = new NeuroCache({ baseUrl: "http://localhost:8080" });

// KV
await cache.set("user:name", "Dhirav", 3600);
const { value } = await cache.get("user:name");

// Semantic cache
await cache.semanticSet("best language for APIs", "Go");
const hit = await cache.semanticGet("top backend language", 0.8);

// LLM cache — wrap any async fn
const { value: answer, hit: cached } = await cache.cacheLLMAround(
  prompt,
  async () => callOpenAI(prompt),
  { threshold: 0.88 },
);

// Memory
await cache.memory.add("user:dhirav", "Prefers Go + React");
const { context } = await cache.memory.query("user:dhirav", "tech prefs?");

// Raw command (anything, same dispatcher as redis-cli)
const { result } = await cache.exec("MEMORY_LIST", "user:dhirav");`}</Code>

      <h2>Go (via go-redis)</h2>
      <Code lang="go">{`import (
  "context"
  "github.com/redis/go-redis/v9"
)

rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
ctx := context.Background()

// Standard commands
rdb.Set(ctx, "greeting", "hello", 0)
val, _ := rdb.Get(ctx, "greeting").Result()

// AI-native commands via Do()
rdb.Do(ctx, "SEMANTIC_SET", "best go framework", "Gin is fast and minimal")
out, _ := rdb.Do(ctx, "SEMANTIC_GET", "top go web framework").Text()

rdb.Do(ctx, "MEMORY_ADD", "user:1", "prefers minimalist design")
ctx1, _ := rdb.Do(ctx, "MEMORY_QUERY", "user:1", "design style").Text()`}</Code>

      <h2>Python (via redis-py)</h2>
      <Code lang="python">{`import redis

r = redis.Redis(host="localhost", port=6379, decode_responses=True)

# Standard commands
r.set("greeting", "hello", ex=3600)
r.get("greeting")

# AI-native commands via execute_command()
r.execute_command("SEMANTIC_SET", "best python framework", "FastAPI")
r.execute_command("SEMANTIC_GET", "top python web framework")
# → "FastAPI"

r.execute_command("CACHE_LLM", "write a haiku about mondays", "Dull is the Monday...")
r.execute_command("CACHE_LLM_GET", "compose a haiku themed on mondays")`}</Code>

      <h2>Node.js (via ioredis)</h2>
      <Code lang="ts">{`import Redis from "ioredis";

const r = new Redis(6379);

await r.set("greeting", "hello", "EX", 3600);
await r.get("greeting");

// AI-native via call()
await r.call("SEMANTIC_SET", "best JS framework", "Next.js");
const hit = await r.call("SEMANTIC_GET", "top javascript framework");`}</Code>

      <h2>HTTP (any language, curl)</h2>
      <p>
        Every command is a JSON endpoint. Great for edge runtimes,
        serverless, and browsers where a raw TCP client isn't an option.
      </p>
      <Code lang="bash">{`# Set
curl -X POST http://localhost:8080/api/kv \\
  -H 'Content-Type: application/json' \\
  -d '{"key":"greeting","value":"hello","ttl":3600}'

# Semantic get
curl "http://localhost:8080/api/semantic?q=best+backend+lang&threshold=0.8"

# Memory query
curl "http://localhost:8080/api/memory/alice?q=preferences&k=5"`}</Code>
    </>
  );
}
