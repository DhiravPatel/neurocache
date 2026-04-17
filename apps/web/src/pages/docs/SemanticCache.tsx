import { Link } from "react-router-dom";
import { Code } from "../../components/Code";

export default function SemanticCache() {
  return (
    <>
      <h1>Semantic Cache</h1>
      <p className="lead">
        Semantic caching turns your cache from an exact-match lookup table
        into a meaning-aware store. Keys are embedded into a vector space;
        reads find the closest match above a similarity threshold.
      </p>

      <h2>When to use it</h2>
      <ul>
        <li><strong>User queries</strong> — "best phone under 50k" and "good phones below 50000" should hit the same cache entry.</li>
        <li><strong>FAQ / support bots</strong> — many wordings, one canonical answer.</li>
        <li><strong>Suggested completions</strong> — similar prefixes reuse the same result set.</li>
      </ul>

      <h2>Set and get</h2>
      <Code lang="bash">{`SEMANTIC_SET "best backend language for APIs" "Go is ideal for high-performance APIs"
SEMANTIC_GET "what language should I use for backend services"
# → "Go is ideal for high-performance APIs"`}</Code>

      <h2>How scoring works</h2>
      <p>
        Each <code>SEMANTIC_SET</code> computes an embedding of the key (not
        the value) and stores it alongside the value in an in-memory vector
        index. On <code>SEMANTIC_GET</code>, the query is embedded, cosine
        similarity against every stored key is computed, and the top match
        is returned — but only if its similarity ≥ the threshold.
      </p>
      <p>
        The default embedding is a feature-hashed word + character-trigram
        vector (384 dimensions, L2-normalized). It is dependency-free and
        runs locally in microseconds. For higher semantic quality, see{" "}
        <Link to="/docs/architecture">Architecture</Link> for how to
        swap in ONNX or OpenAI embeddings.
      </p>

      <h2>Tuning the threshold</h2>
      <p>
        Similarity is a number in <code>[0, 1]</code> where 1.0 is
        identical. The default is <code>0.75</code>. Lower values mean more
        cache hits (and more false positives); higher values mean fewer
        hits but stricter matching.
      </p>
      <Code lang="bash">{`# Env var — affects every SEMANTIC_GET
NEUROCACHE_SEMANTIC_THRESHOLD=0.80

# Per-request override via HTTP
curl "http://localhost:8080/api/semantic?q=best+phone&threshold=0.85"`}</Code>

      <h2>Practical patterns</h2>
      <h3>Cache a knowledge-base answer</h3>
      <Code lang="bash">{`# Write once
SEMANTIC_SET "how do I reset my password" "Click the 'Forgot password' link on the login screen, then check your email for a reset link."

# All of these hit:
SEMANTIC_GET "I forgot my password"
SEMANTIC_GET "password reset steps"
SEMANTIC_GET "how to recover my account"`}</Code>

      <h3>Avoid regenerating expensive search results</h3>
      <Code lang="ts">{`const answer = await cache.semanticGet(query);
if (answer.hit) return answer.value;

const fresh = await runExpensiveSearch(query);
await cache.semanticSet(query, fresh);
return fresh;`}</Code>

      <h2>Inspecting cache state</h2>
      <p>
        The <Link to="/dashboard/semantic">Semantic page in the dashboard</Link>{" "}
        lets you add entries, run queries with a live threshold slider, and
        view each hit's similarity score. Use it to calibrate your
        threshold before deploying.
      </p>

      <h2>Gotchas</h2>
      <ul>
        <li><strong>Keys, not values, are indexed.</strong> The thing you want to "look up by meaning" goes in the <code>key</code> argument.</li>
        <li><strong>Semantic entries are separate from KV.</strong> <code>SET</code> / <code>GET</code> and <code>SEMANTIC_SET</code> / <code>SEMANTIC_GET</code> do not share a namespace.</li>
        <li><strong>Thresholds are embedding-dependent.</strong> If you swap in a different embedding model, retune.</li>
      </ul>
    </>
  );
}
