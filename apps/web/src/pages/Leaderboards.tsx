import { useState } from "react";
import { Trophy, Plus, ChevronUp, Trash2, Crown } from "lucide-react";
import { api } from "../lib/api";
import { usePolling } from "../lib/usePolling";
import { PageHeader, Stat } from "../components/Stat";

export default function LeaderboardsPage() {
  const [board, setBoard] = useState("game:highscores");
  const [member, setMember] = useState("");
  const [score, setScore] = useState("");
  const [err, setErr] = useState<string | null>(null);

  const { data } = usePolling(() => api.lbTop(board.trim() || "game:highscores", 20), 1500);
  const entries = data?.entries ?? [];

  const set = async () => {
    if (!member.trim() || score === "") { setErr("member and score required"); return; }
    setErr(null);
    try { await api.lbSet(board.trim(), member.trim(), Number(score)); setScore(""); }
    catch (e) { setErr((e as Error).message); }
  };
  const incr = async () => {
    if (!member.trim()) { setErr("member required"); return; }
    setErr(null);
    try { await api.lbIncr(board.trim(), member.trim(), Number(score) || 1); }
    catch (e) { setErr((e as Error).message); }
  };
  const remove = async (m: string) => { try { await api.lbRemove(board.trim(), m); } catch { /* poll */ } };

  const medal = (rank: number) =>
    rank === 1 ? "text-amber-400" : rank === 2 ? "text-slate-300" : rank === 3 ? "text-orange-400" : "text-slate-500";

  return (
    <>
      <PageHeader
        title="Leaderboards"
        subtitle="Sorted-set leaderboards read highest-first — O(log n) score updates and rank lookups. Set or increment a score and the board re-ranks instantly."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Members" icon={Trophy} value={data?.count ?? 0} accent="primary" />
        <Stat label="Leader" icon={Crown}
          value={entries[0]?.member ?? "—"} accent="emerald" hint={entries[0] ? `${entries[0].score} pts` : undefined} />
        <Stat label="Top score" icon={ChevronUp} value={entries[0]?.score ?? "—"} accent="accent" />
        <Stat label="Board" icon={Trophy} value={entries.length ? "live" : "empty"} accent="primary" />
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-[340px_1fr]">
        <div className="card p-4">
          <div className="mb-3 flex items-center gap-2 text-sm font-medium">
            <Plus size={15} /> Update scores
          </div>
          <div className="space-y-3">
            <label className="block">
              <span className="text-xs text-slate-400">Board</span>
              <input className="input mt-1" value={board} onChange={(e) => setBoard(e.target.value)} />
            </label>
            <label className="block">
              <span className="text-xs text-slate-400">Member</span>
              <input className="input mt-1" value={member} onChange={(e) => setMember(e.target.value)}
                placeholder="player-1" />
            </label>
            <label className="block">
              <span className="text-xs text-slate-400">Score (or amount to add)</span>
              <input className="input mt-1" type="number" value={score} onChange={(e) => setScore(e.target.value)}
                placeholder="100" />
            </label>
            <div className="grid grid-cols-2 gap-2">
              <button className="btn-primary" onClick={set}>Set score</button>
              <button className="btn-ghost" onClick={incr}><ChevronUp size={14} /> Add</button>
            </div>
            {err ? <div className="text-xs text-rose-400">{err}</div> : null}
          </div>
        </div>

        <div className="card flex flex-col p-0">
          <div className="flex items-center gap-2 border-b border-border px-4 py-2.5 text-sm font-medium">
            <Trophy size={15} /> {board || "leaderboard"}
            <span className="ml-auto text-xs text-slate-500">live · top 20</span>
          </div>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="bg-surface text-left text-xs uppercase tracking-wider text-slate-500">
                <tr>
                  <th className="px-4 py-2 w-16 text-right">Rank</th>
                  <th className="px-4 py-2">Member</th>
                  <th className="px-4 py-2 text-right">Score</th>
                  <th className="px-4 py-2 text-right">Action</th>
                </tr>
              </thead>
              <tbody>
                {entries.length === 0 ? (
                  <tr><td colSpan={4} className="px-4 py-10 text-center text-slate-500">
                    No entries yet — add a score on the left.
                  </td></tr>
                ) : (
                  entries.map((e) => (
                    <tr key={e.member} className="border-t border-border">
                      <td className={"px-4 py-2 text-right font-semibold tabular-nums " + medal(e.rank)}>
                        {e.rank <= 3 ? <Crown size={14} className="ml-auto inline" /> : null} {e.rank}
                      </td>
                      <td className="px-4 py-2 font-mono text-[13px] text-slate-200">{e.member}</td>
                      <td className="px-4 py-2 text-right tabular-nums text-accent">{e.score}</td>
                      <td className="px-4 py-2 text-right">
                        <button onClick={() => remove(e.member)}
                          className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-slate-500 hover:bg-white/5 hover:text-rose-300">
                          <Trash2 size={13} />
                        </button>
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </>
  );
}
