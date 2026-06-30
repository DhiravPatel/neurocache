import { useEffect, useState } from "react";
import { FileText, Save, Play, Trash2, GitBranch, Plus } from "lucide-react";
import { api, type PromptListing, type PromptVersion } from "../lib/api";
import { usePolling } from "../lib/usePolling";
import { PageHeader, Stat } from "../components/Stat";

export default function PromptsPage() {
  const { data: list } = usePolling(api.promptList, 2000);
  const templates: PromptListing[] = list ?? [];

  const [selected, setSelected] = useState("");
  const [creating, setCreating] = useState(false);
  const [versions, setVersions] = useState<PromptVersion[]>([]);
  const [body, setBody] = useState("");
  const [newName, setNewName] = useState("");
  const [varsText, setVarsText] = useState('{\n  "name": "Ada"\n}');
  const [rendered, setRendered] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);

  // In "creating" mode there's no active template — show the name input.
  const active = creating ? "" : (selected || templates[0]?.name || "");

  const load = async (name: string) => {
    if (!name) { setVersions([]); setBody(""); return; }
    try {
      const vs = await api.promptVersions(name);
      setVersions(vs);
      setBody(vs[vs.length - 1]?.body ?? "");
    } catch { setVersions([]); }
  };
  useEffect(() => { load(active); /* eslint-disable-next-line */ }, [active, templates.length]);

  const save = async () => {
    const name = (creating ? newName : active).trim();
    if (!name || !body.trim()) { setMsg("name and body required"); return; }
    const r = await api.promptSet(name, body);
    setMsg(`Saved version ${r.version}`);
    setCreating(false);
    setSelected(name);
    load(name);
  };
  const render = async () => {
    if (!active) return;
    let vars: Record<string, string> = {};
    try { vars = JSON.parse(varsText || "{}"); } catch { setMsg("vars must be valid JSON"); return; }
    try { setRendered((await api.promptRender(active, vars)).rendered); setMsg(null); }
    catch (e) { setMsg((e as Error).message); }
  };
  const del = async (name: string) => {
    await api.promptDelete(name);
    if (name === active) { setSelected(""); setVersions([]); setBody(""); }
  };

  return (
    <>
      <PageHeader
        title="Prompt Templates"
        subtitle="Versioned, server-side prompt templates with {variable} rendering — edit prompts without shipping code, roll back versions, and keep every app on the same canonical prompt."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Templates" icon={FileText} value={templates.length} accent="primary" />
        <Stat label="Selected" icon={GitBranch} value={active || "—"} accent="accent" />
        <Stat label="Versions" icon={GitBranch} value={versions.length} accent="emerald" hint={versions.length ? `latest v${versions[versions.length - 1]?.version}` : undefined} />
        <Stat label="Total versions" icon={FileText} value={templates.reduce((s, t) => s + t.versions, 0)} accent="primary" />
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-[240px_1fr]">
        {/* template list */}
        <div className="card flex flex-col p-0">
          <div className="flex items-center justify-between border-b border-border px-4 py-2.5 text-sm font-medium">
            Templates
            <button onClick={() => { setCreating(true); setSelected(""); setNewName(""); setBody(""); setVersions([]); setRendered(null); }}
              className="text-slate-400 hover:text-primary" title="New template"><Plus size={15} /></button>
          </div>
          <div className="max-h-[60vh] overflow-y-auto p-2">
            {templates.length === 0 ? (
              <div className="px-2 py-6 text-center text-sm text-slate-500">No templates yet.</div>
            ) : templates.map((t) => (
              <button key={t.name} onClick={() => { setCreating(false); setSelected(t.name); }}
                className={"flex w-full items-center justify-between rounded-md px-2 py-1.5 text-left text-sm " +
                  (t.name === active ? "bg-primary/10 text-primary" : "text-slate-300 hover:bg-white/5")}>
                <span className="truncate font-mono text-[13px]">{t.name}</span>
                <span className="flex items-center gap-2">
                  <span className="pill">v{t.latest_version}</span>
                  <Trash2 size={13} className="text-slate-500 hover:text-rose-400"
                    onClick={(e) => { e.stopPropagation(); del(t.name); }} />
                </span>
              </button>
            ))}
          </div>
        </div>

        {/* editor + render */}
        <div className="space-y-4">
          <div className="card p-4">
            <div className="mb-2 flex items-center gap-2 text-sm font-medium"><FileText size={15} /> Template body</div>
            {(creating || !active) && (
              <input className="input mb-2" value={newName} onChange={(e) => setNewName(e.target.value)}
                placeholder="template name e.g. support.greeting" autoFocus />
            )}
            <textarea className="input min-h-32 font-mono text-[13px]" value={body}
              onChange={(e) => setBody(e.target.value)} placeholder="Hello {name}, how can I help with {topic}?" />
            <div className="mt-3 flex items-center gap-2">
              <button className="btn-primary" onClick={save}><Save size={14} /> Save version</button>
              {versions.length > 0 && (
                <span className="text-xs text-slate-500">{versions.length} version{versions.length === 1 ? "" : "s"} · latest v{versions[versions.length - 1]?.version}</span>
              )}
            </div>
          </div>

          <div className="card p-4">
            <div className="mb-2 flex items-center gap-2 text-sm font-medium"><Play size={15} /> Render with variables</div>
            <textarea className="input min-h-20 font-mono text-[13px]" value={varsText}
              onChange={(e) => setVarsText(e.target.value)} />
            <button className="btn-ghost mt-2" onClick={render}><Play size={14} /> Render</button>
            {rendered !== null && (
              <pre className="mt-3 whitespace-pre-wrap rounded-md border border-border bg-bg/40 p-3 text-[13px] text-emerald-300">{rendered}</pre>
            )}
            {msg && <div className="mt-2 text-xs text-slate-400">{msg}</div>}
          </div>
        </div>
      </div>
    </>
  );
}
