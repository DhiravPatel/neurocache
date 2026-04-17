import { Code } from "../../components/Code";

export default function Installation() {
  return (
    <>
      <h1>Installation</h1>
      <p className="lead">
        NeuroCache ships as a single Docker image. Pick your install method
        below — the outcome is the same: the dashboard at{" "}
        <code>http://localhost:8080</code> and the RESP protocol at{" "}
        <code>:6379</code>.
      </p>

      <h2>One-line installer</h2>
      <p>
        The <code>install.sh</code> script checks Docker, pulls the image,
        starts the container with a persistent volume, and waits for the
        health check:
      </p>
      <Code lang="bash">{`curl -fsSL https://neurocache.dev/install.sh | sh`}</Code>
      <p>Flags:</p>
      <Code lang="bash">{`curl -fsSL https://neurocache.dev/install.sh | sh -s -- \\
  --http-port 9090 \\
  --resp-port 16379 \\
  --max-memory 1gb \\
  --eviction ai-smart`}</Code>

      <h2>Docker</h2>
      <Code lang="bash">{`docker run -d \\
  --name neurocache \\
  -p 8080:8080 \\
  -p 6379:6379 \\
  -v neurocache-data:/data \\
  neurocache/engine:latest`}</Code>

      <h2>Docker Compose</h2>
      <p>Drop this into <code>docker-compose.yml</code>:</p>
      <Code lang="yaml">{`services:
  neurocache:
    image: neurocache/engine:latest
    container_name: neurocache
    ports:
      - "8080:8080"
      - "6379:6379"
    environment:
      NEUROCACHE_MAX_MEMORY: 512mb
      NEUROCACHE_EVICTION_POLICY: ai-smart
      NEUROCACHE_SEMANTIC_THRESHOLD: "0.75"
    volumes:
      - neurocache-data:/data
    restart: unless-stopped

volumes:
  neurocache-data:`}</Code>
      <p>Then:</p>
      <Code lang="bash">{`docker compose up -d`}</Code>

      <h2>From source</h2>
      <p>
        Requirements: <strong>Go 1.22+</strong>, <strong>Node 18+</strong>,{" "}
        <strong>pnpm 8+</strong>.
      </p>
      <Code lang="bash">{`git clone https://github.com/dhiravpatel/neurocache.git
cd neurocache
pnpm install
make build               # builds the React dashboard + Go binary
./bin/neurocache         # → http://localhost:8080`}</Code>
      <p>
        The <code>make build</code> target runs the Vite production build,
        copies the output into <code>apps/api/internal/webui/dist</code>,
        and then builds the Go binary with <code>//go:embed</code> so the
        dashboard is inside the single executable.
      </p>

      <h2>Verify the install</h2>
      <Code lang="bash">{`curl -s http://localhost:8080/api/health
# → {"status":"ok","uptime":12.3}

redis-cli -p 6379 PING
# → PONG

docker exec -it neurocache neurocache-cli
neurocache> SET hello world
neurocache> GET hello
# → world`}</Code>

      <h2>Uninstall</h2>
      <Code lang="bash">{`docker rm -f neurocache
docker volume rm neurocache-data     # also deletes persisted data
docker image rm neurocache/engine:latest`}</Code>
    </>
  );
}
