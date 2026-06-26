import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function KnowledgeGraphDocs() {
  return (
    <>
      <h1>Knowledge Graph</h1>
      <p className="lead">
        Store relationships as subject–predicate–object triples, then look up a
        node's neighbours or find the shortest path between two nodes. A
        lightweight graph for entity linking and grounded retrieval — no separate
        graph database to run.
      </p>

      <h2>Link &amp; traverse</h2>
      <Code lang="bash">{`# Add edges
curl localhost:8080/api/graph/link -d '{"subject":"alice","predicate":"knows","object":"bob"}'
curl localhost:8080/api/graph/link -d '{"subject":"bob","predicate":"knows","object":"carol"}'

# Outgoing edges (optionally filter by predicate)
curl "localhost:8080/api/graph/neighbors?subject=alice"
# → {"neighbors":[{"predicate":"knows","object":"bob"}]}

# Reverse: who points AT an object
curl "localhost:8080/api/graph/in?object=carol"

# Shortest path between two nodes — path is the chain of edges
curl "localhost:8080/api/graph/path?from=alice&to=carol&max_depth=6"
# → {"found":true,"path":[{"predicate":"knows","object":"bob"},
#                         {"predicate":"knows","object":"carol"}]}

curl localhost:8080/api/graph/subjects     # all subjects
curl localhost:8080/api/graph/stats`}</Code>

      <h2>From the SDK</h2>
      <Code lang="ts">{`const g = cache.graph;
await g.link("alice", "knows", "bob");
await g.link("bob", "authored", "doc:42");

const { neighbors } = await g.neighbors("alice");            // [{predicate, object}]
const { subjects }  = await g.in("doc:42", "authored");      // who authored it
const { found, path } = await g.path("alice", "doc:42", { maxDepth: 6 });`}</Code>
      <Callout type="tip" title="Ground retrieval with relationships">
        Pair the graph with <Link to="/docs/semantic-cache">semantic search</Link>:
        retrieve candidate entities by meaning, then expand or filter them by
        their graph relationships before handing context to the model.
      </Callout>

      <h2>In the dashboard</h2>
      <p>
        The <Link to="/dashboard/graph">Knowledge Graph page</Link> lets you add
        triples, click through a node's neighbours, and run shortest-path queries
        visually.
      </p>
    </>
  );
}
