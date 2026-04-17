import { Code } from "../../components/Code";

export default function Deployment() {
  return (
    <>
      <h1>Deployment</h1>
      <p className="lead">
        NeuroCache is a single container. Anywhere that runs Docker runs
        NeuroCache. Below are setup notes for the most common hosts.
      </p>

      <h2>Checklist</h2>
      <ul>
        <li><strong>Persistent volume</strong> mounted at <code>/data</code> — at least 1 GB for small workloads.</li>
        <li><strong>Expose port 8080</strong> (dashboard + HTTP API) publicly. Expose <code>6379</code> only if RESP access is needed — otherwise keep it internal.</li>
        <li><strong>Set <code>NEUROCACHE_CORS_ORIGINS</code></strong> to your app's domain (or keep <code>*</code> for internal-only services).</li>
        <li><strong>Health check</strong> on <code>GET /api/health</code>.</li>
        <li><strong>Auth</strong>: NeuroCache does not ship with auth — deploy behind a reverse proxy (Caddy, Cloudflare Zero Trust, Tailscale, OAuth2 Proxy) if it's internet-facing.</li>
      </ul>

      <h2>Render</h2>
      <p>
        The repo ships a <code>render.yaml</code> Blueprint that defines a
        single Docker web service with a 1 GB disk. Either:
      </p>
      <ul>
        <li><strong>Blueprint</strong>: push the repo, click <em>New Blueprint</em>, select <code>render.yaml</code>.</li>
        <li>
          <strong>Manual</strong>: new <em>Web Service</em>, runtime
          <em> Docker</em>, health check path <code>/api/health</code>,
          attach a disk at <code>/data</code>.
        </li>
      </ul>
      <Code lang="yaml">{`services:
  - type: web
    name: neurocache
    runtime: docker
    dockerfilePath: ./Dockerfile
    plan: starter
    healthCheckPath: /api/health
    disk:
      name: neurocache-data
      mountPath: /data
      sizeGB: 1
    envVars:
      - key: NEUROCACHE_MAX_MEMORY
        value: 512mb
      - key: NEUROCACHE_EVICTION_POLICY
        value: ai-smart
      - key: NEUROCACHE_LOG_FORMAT
        value: json`}</Code>
      <p>
        Render only exposes one public HTTP port per service — the dashboard and
        JSON API both work. For public RESP access, add a separate TCP service
        or use Render's private networking.
      </p>

      <h2>Fly.io</h2>
      <Code lang="toml">{`# fly.toml
app = "neurocache"
primary_region = "ord"

[build]
  dockerfile = "Dockerfile"

[[services]]
  internal_port = 8080
  protocol = "tcp"
  [[services.ports]]
    handlers = ["http"]
    port = 80
  [[services.ports]]
    handlers = ["tls", "http"]
    port = 443
  [services.http_checks]
    interval = "10s"
    method = "get"
    path = "/api/health"

[[mounts]]
  source = "neurocache_data"
  destination = "/data"

[env]
  NEUROCACHE_MAX_MEMORY = "1gb"
  NEUROCACHE_LOG_FORMAT = "json"`}</Code>
      <Code lang="bash">{`fly launch --copy-config --no-deploy
fly volumes create neurocache_data --size 1
fly deploy`}</Code>

      <h2>Railway</h2>
      <p>
        Create a new service → <em>Deploy from Repo</em>. Railway picks up
        the <code>Dockerfile</code> automatically. Add a volume and set env
        vars in the dashboard.
      </p>

      <h2>Kubernetes</h2>
      <Code lang="yaml">{`apiVersion: apps/v1
kind: Deployment
metadata: { name: neurocache }
spec:
  replicas: 1
  selector: { matchLabels: { app: neurocache } }
  template:
    metadata: { labels: { app: neurocache } }
    spec:
      containers:
        - name: neurocache
          image: neurocache/engine:latest
          ports:
            - containerPort: 8080
            - containerPort: 6379
          env:
            - { name: NEUROCACHE_MAX_MEMORY,    value: "1gb" }
            - { name: NEUROCACHE_LOG_FORMAT,    value: "json" }
          volumeMounts:
            - { name: data, mountPath: /data }
          readinessProbe:
            httpGet: { path: /api/health, port: 8080 }
            periodSeconds: 5
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: neurocache-data`}</Code>

      <h2>Behind a reverse proxy (Caddy)</h2>
      <Code lang="caddy">{`neurocache.example.com {
  reverse_proxy localhost:8080
  basic_auth {
    admin JDJhJDE0JGJkZzlwVmJETHo5eGNFWGkua3pwYy5LWk0xRzVPeEdxNHhyWmhXV3FIbUl4MmpYajVsczNT
  }
}`}</Code>
      <p>
        Caddy terminates TLS and adds basic auth. Swap for your SSO / OAuth
        proxy of choice in production.
      </p>

      <h2>Scaling notes</h2>
      <ul>
        <li><strong>Today: single node.</strong> NeuroCache is an in-memory engine. One replica per volume.</li>
        <li><strong>Vertical scaling</strong> is straightforward — raise the memory cap and bump <code>NEUROCACHE_MAX_MEMORY</code>.</li>
        <li><strong>Horizontal scaling</strong> (replication + sharding) is on the V3 roadmap. For multi-region today, run independent nodes per region and let your app key into the nearest one.</li>
      </ul>
    </>
  );
}
