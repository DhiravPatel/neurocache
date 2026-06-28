import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function ChurnDocs() {
  return (
    <>
      <h1>Tag Invalidation</h1>
      <p className="lead">
        Tag cache keys with logical groups, then bust an entire group in a
        single call. The classic "invalidate everything for user X" or "drop the
        whole product:42 page cache" pattern — server-side, with no need to track
        which keys belong to which entity in your application.
      </p>

      <h2>Tag, then invalidate</h2>
      <Code lang="bash">{`# Tag cache keys as you write them
curl localhost:8080/api/churn/user:1:profile -d '{"tags":["user:1","profiles"]}'
curl localhost:8080/api/churn/user:1:feed    -d '{"tags":["user:1","feeds"]}'

# Later, blow away everything for user:1 in one call — returns the dropped keys
curl localhost:8080/api/churn/invalidate -d '{"tags":["user:1"]}'
# → {"dropped":["user:1:profile","user:1:feed"]}

curl "localhost:8080/api/churn/keys?tag=profiles"   # keys for a tag
curl localhost:8080/api/churn/user:1:profile         # tags on a key
curl localhost:8080/api/churn/tags                   # all tags`}</Code>

      <h2>From the SDK</h2>
      <Code lang="ts">{`const c = cache.churn;

// When you cache something, tag it
await cache.set("user:1:profile", json);
await c.tag("user:1:profile", "user:1", "profiles");

// When the user changes, invalidate every key carrying their tag
const { dropped } = await c.invalidate("user:1");   // → string[] of dropped keys

await c.keysFor("profiles");   // what's tagged "profiles"
await c.tagsOf("user:1:feed"); // tags on a key`}</Code>
      <Callout type="tip" title="Stop tracking key sets in your app">
        The usual cache-busting headache is remembering which keys to delete when
        an entity changes. Tag at write-time and invalidate by tag instead — the
        store keeps the tag→keys index, so one call drops the whole group
        atomically and tells you exactly what it removed.
      </Callout>

      <h2>In the dashboard</h2>
      <p>
        The <Link to="/dashboard/churn">Tag Invalidation page</Link> lets you tag
        keys, browse the keys under each tag, and invalidate a tag — showing the
        keys that were dropped.
      </p>
    </>
  );
}
