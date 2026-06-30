import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function ExperimentsDocs() {
  return (
    <>
      <h1>A/B Experiments</h1>
      <p className="lead">
        Split-test prompts, models, or thresholds with deterministic variant
        assignment and server-side exposure + conversion tracking. The same user
        always gets the same variant, and win-rate picks the leader once there's
        enough data.
      </p>

      <h2>Define, assign, measure</h2>
      <Code lang="bash">{`# Define an experiment (optionally weighted)
curl localhost:8080/api/ab -d '{"name":"prompt.greeting","variants":["control","friendly"]}'

# Assign a user — deterministic, sticky per user
curl "localhost:8080/api/ab/prompt.greeting/assign?user=u42"   # → {"variant":"friendly"}

# Record the exposure (denominator) and the outcome (e.g. 1 = converted)
curl localhost:8080/api/ab/prompt.greeting/expose -d '{"variant":"friendly"}'
curl localhost:8080/api/ab/prompt.greeting/record -d '{"variant":"friendly","value":1}'

# Read results: per-variant exposures, wins, win-rate, avg value, and the leader
curl localhost:8080/api/ab/prompt.greeting`}</Code>

      <h2>From the SDK</h2>
      <Code lang="ts">{`const ab = cache.experiments;
await ab.define("prompt.greeting", ["control", "friendly"]);

async function greet(userId: string) {
  const { variant } = await ab.assign("prompt.greeting", userId);
  await ab.expose("prompt.greeting", variant!);
  const reply = await callLLM(promptFor(variant!));   // use the assigned variant
  await ab.record("prompt.greeting", variant!, userConverted ? 1 : 0);
  return reply;
}

const stats = await ab.stats("prompt.greeting"); // variants[], winner`}</Code>
      <Callout type="info" title="Deterministic + sticky">
        Assignment hashes the user id, so a user keeps the same variant across
        requests and processes without you storing anything. A winner is only
        declared once a variant clears a minimum exposure count, to avoid calling
        it from noise.
      </Callout>

      <h2>In the dashboard</h2>
      <p>
        The <Link to="/dashboard/experiments">Experiments page</Link> defines
        experiments, simulates traffic, and shows a live per-variant results table
        with win-rate bars and the current leader.
      </p>
    </>
  );
}
