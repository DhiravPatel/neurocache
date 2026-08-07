import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function SchedulerDocs() {
  return (
    <>
      <h1>Scheduler</h1>
      <p className="lead">
        Run any command at a future time or after a delay — server-side, durable,
        and replicated. Schedule a cache warm, a TTL-less cleanup, a delayed
        retry, or a reminder without standing up an external cron or job runner.
      </p>

      <h2>Schedule, list, cancel</h2>
      <Code lang="bash">{`# Run a command after a delay (ms)
curl localhost:8080/api/schedule/in -d '{"delay_ms":30000,"cmd":"DEL","args":["session:42"]}'
# → {"id":1}

# …or at an absolute time (unix ms)
curl localhost:8080/api/schedule/at -d '{"unix_ms":1893456000000,"cmd":"SET","args":["k","v"]}'

curl localhost:8080/api/schedule          # pending tasks (id, fire_at, cmd, args)
curl -X DELETE localhost:8080/api/schedule/1   # cancel a task
curl localhost:8080/api/schedule/stats`}</Code>

      <h2>From the SDK</h2>
      <Code lang="ts">{`const s = cache.scheduler;

// Expire a draft in 10 minutes without relying on key TTL
const { id } = await s.in(10 * 60_000, "DEL", ["draft:42"]);

// Warm a cache at 3am
await s.at(Date.parse("2026-07-01T03:00:00Z"), "INFER.GENERATE", ["daily summary"]);

await s.list();        // { tasks: [{ id, fire_at, cmd, args, created_at }] }
await s.cancel(id);    // { cancelled: true }`}</Code>
      <Callout type="info" title="Durable, but command-scoped">
        Scheduled tasks survive restarts (they replay through the write log) and
        fire on the engine itself — the action is any NeuroCache command, run with
        the engine's own privileges. For arbitrary application work, schedule a
        command that enqueues a <Link to="/docs/queues">job</Link> and let your
        worker pool pick it up.
      </Callout>

      <h2>In the dashboard</h2>
      <p>
        The <Link to="/dashboard/scheduler">Scheduler page</Link> schedules a
        command, shows pending tasks with a live "fires in" countdown, and cancels
        them.
      </p>
    </>
  );
}
