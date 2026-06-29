#!/usr/bin/env node
// Pipelining throughput benchmark: N individual /api/exec round-trips vs the
// same N commands sent in ONE /api/pipeline request. Requires Node 18+ (fetch).
//
//   node scripts/bench-pipeline.mjs            # 500 ops vs http://localhost:8080
//   N=2000 NC_URL=http://host:8080 node scripts/bench-pipeline.mjs

const BASE = process.env.NC_URL || "http://localhost:8080";
const N = Number(process.env.N || 500);

const post = (path, body) =>
  fetch(`${BASE}${path}`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  }).then((r) => r.json());

const fmt = (n) => n.toLocaleString("en-US", { maximumFractionDigits: 1 });

async function main() {
  await post("/api/exec", { command: "PING", args: [] }); // warm up

  // 1) N individual requests (one round-trip each)
  let t0 = performance.now();
  for (let i = 0; i < N; i++) {
    await post("/api/exec", { command: "SET", args: [`bench:i:${i}`, "v"] });
  }
  const indivMs = performance.now() - t0;

  // 2) the same N commands in a single pipelined request
  const commands = Array.from({ length: N }, (_, i) => ["SET", `bench:p:${i}`, "v"]);
  t0 = performance.now();
  const res = await post("/api/pipeline", { commands });
  const pipeMs = performance.now() - t0;

  const ok = (res.results || []).filter((r) => r.ok).length;
  const indivOps = (N / indivMs) * 1000;
  const pipeOps = (N / pipeMs) * 1000;

  console.log(`\n  NeuroCache pipelining benchmark — ${N} SET ops → ${BASE}\n`);
  console.log(`  individual /api/exec : ${fmt(indivMs)} ms   (${fmt(indivOps)} ops/sec)`);
  console.log(`  single /api/pipeline : ${fmt(pipeMs)} ms   (${fmt(pipeOps)} ops/sec)   [${ok}/${N} ok]`);
  console.log(`\n  → ${fmt(indivMs / pipeMs)}× faster wall-clock, ${fmt(pipeOps / indivOps)}× the throughput\n`);
}

main().catch((e) => {
  console.error("benchmark failed:", e.message);
  process.exit(1);
});
