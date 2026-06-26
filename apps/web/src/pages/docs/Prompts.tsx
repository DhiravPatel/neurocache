import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function PromptsDocs() {
  return (
    <>
      <h1>Prompt Templates</h1>
      <p className="lead">
        Versioned, server-side prompt templates with <code>{`{variable}`}</code>{" "}
        rendering. Change a prompt without shipping code, keep every service on
        the same canonical version, and roll back instantly when a new prompt
        regresses.
      </p>

      <h2>Store, version, render</h2>
      <Code lang="bash">{`# Save a template — version auto-increments each time
curl localhost:8080/api/prompts/support.greeting \\
  -d '{"body":"Hi {name}, how can I help with {topic}?"}'
# → {"version":1}

# Render it with variables (latest version unless you pass ?version)
curl localhost:8080/api/prompts/support.greeting/render \\
  -d '{"vars":{"name":"Ada","topic":"billing"}}'
# → {"rendered":"Hi Ada, how can I help with billing?"}

curl localhost:8080/api/prompts                          # list templates
curl localhost:8080/api/prompts/support.greeting/versions # all versions
curl localhost:8080/api/prompts/support.greeting?version=1 # a specific version`}</Code>

      <h2>From the SDK</h2>
      <Code lang="ts">{`const p = cache.prompts;

const { version } = await p.set("support.greeting", "Hi {name}!"); // v1
await p.set("support.greeting", "Hey {name} 👋");                  // v2

const { rendered } = await p.render("support.greeting", { name: "Ada" });
// → "Hey Ada 👋"

await p.versions("support.greeting");   // history
await p.get("support.greeting", 1);     // pin an old version
await p.delete("support.greeting", 2);  // roll back v2`}</Code>
      <Callout type="tip" title="Decouple prompts from deploys">
        Because templates live in the store, a prompt change is a single API call
        — no rebuild, no redeploy, and every service picks up the new version on
        its next <code>render</code>. Pin a <code>version</code> in production and
        promote deliberately.
      </Callout>

      <h2>In the dashboard</h2>
      <p>
        The <Link to="/dashboard/prompts">Prompt Templates page</Link> lists your
        templates and versions, edits bodies, and renders them with sample
        variables live.
      </p>
    </>
  );
}
