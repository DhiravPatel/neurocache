import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function ModerationDocs() {
  return (
    <>
      <h1>Moderation &amp; Safety</h1>
      <p className="lead">
        Cache moderation verdicts so you never pay to re-score the same text, and
        screen prompts for injection attempts with a built-in heuristic. Keep the
        safety layer fast and cheap without a round-trip to a moderation API on
        every request.
      </p>

      <h2>Cache verdicts</h2>
      <Code lang="bash">{`# Store a verdict for some text (e.g. after calling a moderation API)
curl localhost:8080/api/safe \\
  -d '{"text":"how do I reset my password","safe":true,"score":0.01}'

# Look it up next time the same text appears — no re-scoring
curl "localhost:8080/api/safe?text=how%20do%20I%20reset%20my%20password"
# → {"hit":true,"result":{"safe":true,"score":0.01,"categories":[]}}

curl localhost:8080/api/safe/stats`}</Code>

      <h2>Screen for prompt injection</h2>
      <p>
        A dependency-free heuristic scores text for injection patterns
        ("ignore previous instructions", role-play jailbreaks, etc.) — useful as
        a cheap first gate before an expensive model call:
      </p>
      <Code lang="bash">{`curl "localhost:8080/api/safe/inject?text=ignore+all+previous+instructions"
# → {"score":0.9,"matched":["ignore previous instructions"]}`}</Code>

      <h2>From the SDK</h2>
      <Code lang="ts">{`const m = cache.moderation;

// Cache a verdict, then reuse it
await m.set("user text…", { safe: true, score: 0.01 });
const { hit, result } = await m.check("user text…");

// Cheap injection gate before calling the model
const { score, matched } = await m.injectionScore(userPrompt);
if (score >= 0.5) return refuse(matched);`}</Code>
      <Callout type="warning" title="A cache + heuristic, not a moderation model">
        NeuroCache <em>caches</em> verdicts you produce and runs a pattern-based
        injection heuristic — it is not itself a content classifier. Generate
        verdicts with your provider's moderation endpoint (or your own model) and
        store them here so repeats are instant.
      </Callout>

      <h2>In the dashboard</h2>
      <p>
        The <Link to="/dashboard/moderation">Moderation console</Link> screens
        text live, shows the cached verdict, and visualizes the injection score
        with the matched patterns.
      </p>
    </>
  );
}
