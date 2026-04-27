import { useEffect, useState } from "react";
import { Boxes, RefreshCw, Power, PowerOff } from "lucide-react";
import { api } from "../lib/api";

// Loaded mirrors what `MODULE LIST` returns over /api/exec, but the
// HTTP wrapper in apps/api/internal/http/modules.go serialises it as
// JSON objects rather than the bare RESP array — easier to render
// without re-parsing positional pairs in the browser.
type Loaded = {
  name: string;
  version: string;
  description: string;
  commands: string[];
  types: string[];
};

type Available = {
  name: string;
  version: string;
  description: string;
};

export default function ModulesPage() {
  const [loaded, setLoaded] = useState<Loaded[] | null>(null);
  const [available, setAvailable] = useState<Available[]>([]);
  const [busy, setBusy] = useState<string>(""); // module currently being mutated
  const [err, setErr] = useState<string>("");

  // refresh both lists in parallel — the available pool is static for
  // the process lifetime, but re-fetching keeps the panel honest if
  // we ever add hot-loadable .so plugins.
  async function refresh() {
    setErr("");
    try {
      const [list, avail] = await Promise.all([
        api.exec("MODULE", ["LIST"]),
        api.exec("MODULE", ["AVAILABLE"]),
      ]);
      setLoaded((list.result as Loaded[]) ?? []);
      setAvailable((avail.result as Available[]) ?? []);
    } catch (e: any) {
      setErr(e?.message ?? String(e));
    }
  }

  useEffect(() => {
    refresh();
  }, []);

  async function load(name: string) {
    setBusy(name);
    try {
      const r = await api.exec("MODULE", ["LOAD", name]);
      if (!r.ok) throw new Error(r.error ?? "load failed");
      await refresh();
    } catch (e: any) {
      setErr(e?.message ?? String(e));
    } finally {
      setBusy("");
    }
  }

  async function unload(name: string) {
    setBusy(name);
    try {
      const r = await api.exec("MODULE", ["UNLOAD", name]);
      if (!r.ok) throw new Error(r.error ?? "unload failed");
      await refresh();
    } catch (e: any) {
      setErr(e?.message ?? String(e));
    } finally {
      setBusy("");
    }
  }

  // Available modules that aren't currently loaded — what the operator
  // can MODULE LOAD into the running engine.
  const loadedSet = new Set((loaded ?? []).map((m) => m.name.toLowerCase()));
  const installable = available.filter((m) => !loadedSet.has(m.name.toLowerCase()));

  return (
    <div className="space-y-6">
      <header className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Boxes size={22} className="text-primary" />
          <div>
            <h1 className="text-xl font-semibold">Modules</h1>
            <p className="text-sm text-slate-400">
              Manage compile-time-linked Stack modules. Activate to add
              commands and custom data types to the running engine.
            </p>
          </div>
        </div>
        <button
          onClick={refresh}
          className="flex items-center gap-2 rounded-lg border border-border px-3 py-1.5 text-sm hover:border-primary/40"
        >
          <RefreshCw size={14} /> Refresh
        </button>
      </header>

      {err && (
        <div className="rounded-md border border-red-500/40 bg-red-500/10 px-4 py-2 text-sm text-red-300">
          {err}
        </div>
      )}

      <section>
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
          Loaded ({loaded?.length ?? 0})
        </h2>
        <div className="grid gap-3 md:grid-cols-2">
          {loaded?.length === 0 && (
            <div className="text-sm text-slate-500">
              No modules loaded. Activate one from the available list below.
            </div>
          )}
          {loaded?.map((m) => (
            <div
              key={m.name}
              className="rounded-lg border border-border bg-white/5 p-4"
            >
              <div className="flex items-start justify-between">
                <div>
                  <div className="flex items-baseline gap-2">
                    <span className="font-semibold">{m.name}</span>
                    <span className="text-xs text-slate-500">v{m.version}</span>
                  </div>
                  <p className="mt-1 text-sm text-slate-400">{m.description}</p>
                </div>
                <button
                  disabled={busy === m.name}
                  onClick={() => unload(m.name)}
                  className="flex items-center gap-1 rounded-md border border-red-500/40 px-2 py-1 text-xs text-red-300 hover:bg-red-500/10 disabled:opacity-50"
                >
                  <PowerOff size={12} />
                  {busy === m.name ? "…" : "Unload"}
                </button>
              </div>
              <div className="mt-3 flex flex-wrap gap-1">
                {m.commands.slice(0, 16).map((c) => (
                  <code
                    key={c}
                    className="rounded bg-black/40 px-1.5 py-0.5 text-[11px] text-slate-300"
                  >
                    {c}
                  </code>
                ))}
                {m.commands.length > 16 && (
                  <span className="text-[11px] text-slate-500">
                    + {m.commands.length - 16} more
                  </span>
                )}
              </div>
              {m.types.length > 0 && (
                <div className="mt-2 text-xs text-slate-500">
                  Custom types: {m.types.join(", ")}
                </div>
              )}
            </div>
          ))}
        </div>
      </section>

      <section>
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wider text-slate-400">
          Available ({installable.length})
        </h2>
        <div className="grid gap-3 md:grid-cols-2">
          {installable.length === 0 && (
            <div className="text-sm text-slate-500">
              Every linked module is already loaded.
            </div>
          )}
          {installable.map((m) => (
            <div
              key={m.name}
              className="rounded-lg border border-border bg-white/5 p-4"
            >
              <div className="flex items-start justify-between">
                <div>
                  <div className="flex items-baseline gap-2">
                    <span className="font-semibold">{m.name}</span>
                    <span className="text-xs text-slate-500">v{m.version}</span>
                  </div>
                  <p className="mt-1 text-sm text-slate-400">{m.description}</p>
                </div>
                <button
                  disabled={busy === m.name}
                  onClick={() => load(m.name)}
                  className="flex items-center gap-1 rounded-md border border-primary/40 px-2 py-1 text-xs text-primary hover:bg-primary/10 disabled:opacity-50"
                >
                  <Power size={12} />
                  {busy === m.name ? "…" : "Load"}
                </button>
              </div>
            </div>
          ))}
        </div>
      </section>

      <section className="rounded-lg border border-border bg-white/5 p-4 text-sm text-slate-400">
        <p>
          To pre-load modules at boot, set{" "}
          <code className="rounded bg-black/40 px-1.5 py-0.5 text-[12px] text-slate-200">
            NEUROCACHE_MODULES_LOAD=json,probabilistic,timeseries,search
          </code>
          . See the{" "}
          <a className="text-primary underline" href="/docs/commands#modules">
            Modules
          </a>{" "}
          docs for the ABI and per-module command surface.
        </p>
      </section>
    </div>
  );
}
