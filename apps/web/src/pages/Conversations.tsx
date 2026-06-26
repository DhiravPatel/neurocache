import { useEffect, useState } from "react";
import { MessagesSquare, Send, Trash2, Plus, Scissors } from "lucide-react";
import { api, type ConvTurn } from "../lib/api";
import { usePolling } from "../lib/usePolling";
import { PageHeader, Stat } from "../components/Stat";

const ROLES = ["user", "assistant", "system", "tool"];
const roleStyle: Record<string, string> = {
  user: "bg-primary/15 text-primary",
  assistant: "bg-emerald-500/15 text-emerald-300",
  system: "bg-slate-500/15 text-slate-300",
  tool: "bg-accent/15 text-accent",
};

export default function ConversationsPage() {
  const { data: list } = usePolling(api.convList, 2000);
  const keys = list?.conversations ?? [];

  const [selected, setSelected] = useState("");
  const [turns, setTurns] = useState<ConvTurn[]>([]);
  const [role, setRole] = useState("user");
  const [content, setContent] = useState("");
  const [newKey, setNewKey] = useState("session:demo");

  const active = selected || keys[0] || "";

  const loadWindow = async (key: string) => {
    if (!key) { setTurns([]); return; }
    try { setTurns((await api.convWindow(key)).turns ?? []); } catch { setTurns([]); }
  };

  useEffect(() => { loadWindow(active); /* eslint-disable-next-line */ }, [active, list?.count]);

  const append = async () => {
    const key = (selected || newKey).trim();
    if (!key || !content.trim()) return;
    await api.convAppend(key, role, content.trim());
    setContent("");
    setSelected(key);
    loadWindow(key);
  };
  const reset = async (key: string) => {
    await api.convReset(key);
    if (key === active) setTurns([]);
  };
  const summarize = async () => {
    if (!active) return;
    await api.convSummarize(active, "(summary of earlier turns)", 200);
    loadWindow(active);
  };

  const totalTokens = turns.reduce((s, t) => s + (t.tokens || 0), 0);

  return (
    <>
      <PageHeader
        title="Conversations"
        subtitle="Server-side multi-turn context: append turns, read a token-bounded window, and compress old history with a running summary — so every client shares one session state."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Conversations" icon={MessagesSquare} value={list?.count ?? 0} accent="primary" />
        <Stat label="Active key" icon={Send} value={active || "—"} accent="accent" />
        <Stat label="Turns shown" icon={MessagesSquare} value={turns.length} accent="emerald" />
        <Stat label="Tokens (window)" icon={Scissors} value={totalTokens} accent="primary" />
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-[260px_1fr]">
        {/* sessions list */}
        <div className="card flex flex-col p-0">
          <div className="border-b border-border px-4 py-2.5 text-sm font-medium">Sessions</div>
          <div className="max-h-[60vh] overflow-y-auto p-2">
            {keys.length === 0 ? (
              <div className="px-2 py-6 text-center text-sm text-slate-500">No sessions yet.</div>
            ) : keys.map((k) => (
              <button key={k} onClick={() => setSelected(k)}
                className={"flex w-full items-center justify-between rounded-md px-2 py-1.5 text-left text-sm " +
                  (k === active ? "bg-primary/10 text-primary" : "text-slate-300 hover:bg-white/5")}>
                <span className="truncate font-mono text-[13px]">{k}</span>
                <Trash2 size={13} className="shrink-0 text-slate-500 hover:text-rose-400"
                  onClick={(e) => { e.stopPropagation(); reset(k); }} />
              </button>
            ))}
          </div>
        </div>

        {/* transcript + composer */}
        <div className="card flex flex-col p-0">
          <div className="flex items-center gap-2 border-b border-border px-4 py-2.5 text-sm font-medium">
            <MessagesSquare size={15} /> {active || "new session"}
            <button onClick={summarize} disabled={!active}
              className="ml-auto inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-slate-400 hover:bg-white/5 hover:text-slate-100 disabled:opacity-40">
              <Scissors size={13} /> Summarize
            </button>
          </div>
          <div className="max-h-[48vh] min-h-[14rem] space-y-2 overflow-y-auto p-4">
            {turns.length === 0 ? (
              <div className="flex h-40 items-center justify-center text-sm text-slate-500">
                No turns — append one below.
              </div>
            ) : turns.map((t, i) => (
              <div key={i} className="flex gap-2">
                <span className={"shrink-0 self-start rounded px-1.5 py-0.5 text-[11px] font-medium " + (roleStyle[t.role] || roleStyle.system)}>
                  {t.role}
                </span>
                <span className="text-sm text-slate-200">{t.content}
                  <span className="ml-2 text-[11px] text-slate-600">{t.tokens} tok</span>
                </span>
              </div>
            ))}
          </div>
          <div className="border-t border-border p-3">
            {keys.length === 0 && !selected ? (
              <input className="input mb-2" value={newKey} onChange={(e) => setNewKey(e.target.value)}
                placeholder="new session key" />
            ) : null}
            <div className="flex gap-2">
              <select className="input w-32" value={role} onChange={(e) => setRole(e.target.value)}>
                {ROLES.map((r) => <option key={r} value={r}>{r}</option>)}
              </select>
              <input className="input flex-1" value={content} onChange={(e) => setContent(e.target.value)}
                placeholder="message content…" onKeyDown={(e) => e.key === "Enter" && append()} />
              <button className="btn-primary" onClick={append}><Plus size={14} /> Append</button>
            </div>
          </div>
        </div>
      </div>
    </>
  );
}
