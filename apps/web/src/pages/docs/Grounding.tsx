import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function GroundingDocs() {
  return (
    <>
      <h1>Grounding &amp; Verification</h1>
      <p className="lead">
        Catch hallucinations before they reach a user. Grounding scores an LLM
        answer against the context it was supposed to use — sentence by sentence
        — and flags every claim that no source supports. It is RAG faithfulness
        and citation checking, computed server-side, with no extra model call.
      </p>

      <h2>How it works</h2>
      <p>
        An answer is split into sentences ("claims"). Each claim is embedded and
        compared against your retrieved context chunks; its <em>support</em> is
        the highest cosine similarity to any chunk, in <code>[0, 1]</code>. A
        claim counts as supported when its support clears <code>min_support</code>
        (default <code>0.5</code>). The <strong>doc score</strong> is the weakest
        claim — one unsupported sentence makes the whole answer ungrounded.
      </p>

      <h2>Verify an answer</h2>
      <Code lang="bash">{`curl localhost:8080/api/ground/verify -d '{
  "answer": "NeuroCache spreads data across CPU cores. It is written in Rust.",
  "context": [
    "NeuroCache shards the keyspace across all available CPU cores.",
    "Replies are encoded with zero heap allocations on the hot path."
  ],
  "min_support": 0.5
}'
# → {
#   "doc_score": 0.18, "mean_score": 0.55, "grounded": false,
#   "sentences": [
#     {"sentence":"NeuroCache spreads data across CPU cores.","support":0.92,"best_chunk":0,"supported":true},
#     {"sentence":"It is written in Rust.","support":0.18,"best_chunk":1,"supported":false}
#   ],
#   "unsupported": ["It is written in Rust."]
# }`}</Code>
      <p>
        The second sentence has no backing chunk, so it is returned in
        <code>unsupported</code> and the answer is flagged as not grounded.
      </p>

      <h2>From the SDK</h2>
      <Code lang="ts">{`const g = cache.grounding;

const report = await g.verify(answer, retrievedChunks);
if (!report.grounded) {
  // Refuse, regenerate, or surface the unsupported claims to the user.
  console.warn("Unsupported claims:", report.unsupported);
}`}</Code>

      <h2>Gate with a risk budget</h2>
      <p>
        <code>require</code> works like <code>verify</code> but also debits a
        per-session <em>risk budget</em> by how grounded the answer was. Sessions
        that keep producing shaky answers eventually trip{" "}
        <code>risk.enforce</code>, so you can route the next request through a
        stricter (slower, more expensive) path only when a user actually needs it:
      </p>
      <Code lang="ts">{`const { result, risk } = await cache.grounding.require(answer, chunks, {
  session: userId,
  minSupport: 0.6,
});
if (risk?.enforce) {
  // This session has burned its trust budget — escalate verification.
}`}</Code>

      <h2>Bring your own scorer</h2>
      <p>
        The built-in scorer uses cosine similarity over embeddings. To drive
        groundedness with a real entailment / NLI model instead, switch the
        scorer to <code>extern</code> and feed per-claim scores:
      </p>
      <Code lang="ts">{`await cache.grounding.setScorer("extern");
// idx is the 0-based sentence index of the answer
await cache.grounding.ingest(answer, 1, 0.97);`}</Code>

      <Callout type="tip" title="Pairs with risk budgets and moderation">
        Grounding answers the question "is this answer faithful to its sources?"
        — a different axis from{" "}
        <Link to="/docs/moderation">moderation</Link> ("is this text safe?"). Use
        both: moderate the input, ground the output.
      </Callout>

      <Callout type="warning" title="Similarity, not proof">
        Cosine grounding catches answers that drift from their context, but high
        similarity is not a logical entailment guarantee. For high-stakes flows,
        switch to an <code>extern</code> NLI scorer and treat the doc score as a
        signal, not a verdict.
      </Callout>

      <h2>In the dashboard</h2>
      <p>
        The <Link to="/dashboard/grounding">Grounding console</Link> lets you
        paste an answer and its sources, tune the threshold, and see each claim's
        support bar with unsupported sentences highlighted — plus live pass/fail
        counters.
      </p>
    </>
  );
}
