import { useEffect, useRef, useState } from "react";
import {
  Radio, Send, Antenna, Hash, Asterisk, Trash2, Wifi, WifiOff,
} from "lucide-react";
import { api } from "../lib/api";
import { usePolling } from "../lib/usePolling";
import { PageHeader, Stat } from "../components/Stat";

type Mode = "channel" | "pattern";
type LiveMsg = { channel: string; pattern: string; payload: string; at: number };

const MAX_LOG = 200;

function timeOf(ms: number) {
  const d = new Date(ms);
  return d.toLocaleTimeString([], { hour12: false }) + "." +
    String(d.getMilliseconds()).padStart(3, "0");
}

export default function PubSubPage() {
  const [mode, setMode] = useState<Mode>("channel");
  const [target, setTarget] = useState("news");
  const [connected, setConnected] = useState(false);
  const [subscribedTo, setSubscribedTo] = useState<string[]>([]);
  const [messages, setMessages] = useState<LiveMsg[]>([]);
  const esRef = useRef<EventSource | null>(null);

  const { data: channelData } = usePolling(() => api.pubsubChannels("*"), 2000);

  // Publish form
  const [pubChannel, setPubChannel] = useState("news");
  const [pubMessage, setPubMessage] = useState("");
  const [pubResult, setPubResult] = useState<string | null>(null);

  const stop = () => {
    esRef.current?.close();
    esRef.current = null;
    setConnected(false);
    setSubscribedTo([]);
  };

  // Tear down the stream when the page unmounts.
  useEffect(() => () => esRef.current?.close(), []);

  const start = () => {
    stop();
    const items = target.split(",").map((s) => s.trim()).filter(Boolean);
    if (items.length === 0) return;
    const url =
      mode === "channel"
        ? api.subscribeUrl({ channels: items })
        : api.subscribeUrl({ patterns: items });
    const es = new EventSource(url);
    es.onopen = () => { setConnected(true); setSubscribedTo(items); };
    es.onmessage = (e) => {
      try {
        const m = JSON.parse(e.data) as Omit<LiveMsg, "at">;
        setMessages((prev) => [{ ...m, at: Date.now() }, ...prev].slice(0, MAX_LOG));
      } catch { /* ignore */ }
    };
    es.onerror = () => setConnected(false); // EventSource auto-reconnects
    esRef.current = es;
  };

  const publish = async () => {
    if (!pubChannel.trim()) return;
    try {
      const r = await api.publish(pubChannel.trim(), pubMessage);
      setPubResult(`Delivered to ${r.receivers} subscriber${r.receivers === 1 ? "" : "s"}.`);
      setPubMessage("");
    } catch (e) {
      setPubResult((e as Error).message);
    }
  };

  const channels = channelData?.channels ?? [];

  return (
    <>
      <PageHeader
        title="Pub/Sub"
        subtitle="Publish and subscribe in real time over Server-Sent Events. Messages bridge the RESP port and HTTP — a SUBSCRIBE here receives anything PUBLISHed from redis-cli too."
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Active channels" icon={Hash} value={channels.length} accent="primary" />
        <Stat label="Active patterns" icon={Asterisk} value={channelData?.num_patterns ?? 0} accent="accent" />
        <Stat
          label="Stream"
          icon={connected ? Wifi : WifiOff}
          value={connected ? "live" : "idle"}
          accent={connected ? "emerald" : "rose"}
          hint={subscribedTo.length ? subscribedTo.join(", ") : "not subscribed"}
        />
        <Stat label="Messages (session)" icon={Radio} value={messages.length} accent="accent" />
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-[360px_1fr]">
        {/* ── Subscribe + publish controls ── */}
        <div className="space-y-4">
          <div className="card p-4">
            <div className="mb-3 flex items-center gap-2 text-sm font-medium">
              <Antenna size={15} /> Subscribe
            </div>
            <div className="mb-2 inline-flex rounded-md border border-border p-0.5 text-xs">
              {(["channel", "pattern"] as Mode[]).map((m) => (
                <button
                  key={m}
                  onClick={() => setMode(m)}
                  className={
                    "rounded px-2.5 py-1 capitalize transition-colors " +
                    (mode === m ? "bg-primary text-white" : "text-slate-400 hover:text-slate-200")
                  }
                >
                  {m}
                </button>
              ))}
            </div>
            <input
              className="input"
              placeholder={mode === "channel" ? "channel(s), comma-separated" : "glob pattern, e.g. room.*"}
              value={target}
              onChange={(e) => setTarget(e.target.value)}
            />
            <div className="mt-3 flex gap-2">
              {connected ? (
                <button className="btn-ghost flex-1" onClick={stop}>
                  <WifiOff size={14} /> Unsubscribe
                </button>
              ) : (
                <button className="btn-primary flex-1" onClick={start}>
                  <Antenna size={14} /> Subscribe
                </button>
              )}
            </div>
          </div>

          <div className="card p-4">
            <div className="mb-3 flex items-center gap-2 text-sm font-medium">
              <Send size={15} /> Publish
            </div>
            <input
              className="input mb-2"
              placeholder="channel"
              value={pubChannel}
              onChange={(e) => setPubChannel(e.target.value)}
            />
            <textarea
              className="input mb-2 min-h-20"
              placeholder="message payload"
              value={pubMessage}
              onChange={(e) => setPubMessage(e.target.value)}
            />
            <button className="btn-primary w-full" onClick={publish}>
              <Send size={14} /> Publish
            </button>
            {pubResult ? <div className="mt-2 text-xs text-slate-400">{pubResult}</div> : null}
          </div>

          <div className="card p-4">
            <div className="mb-2 text-[11px] font-semibold uppercase tracking-wider text-slate-500">
              Active channels
            </div>
            {channels.length === 0 ? (
              <div className="text-sm text-slate-500">No active subscriptions.</div>
            ) : (
              <ul className="space-y-1 text-sm">
                {channels.map((c) => (
                  <li key={c} className="flex items-center justify-between">
                    <span className="font-mono text-[13px] text-slate-300">{c}</span>
                    <span className="pill">{channelData?.num_subs?.[c] ?? 0} sub</span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>

        {/* ── Live message stream ── */}
        <div className="card flex flex-col p-0">
          <div className="flex items-center justify-between border-b border-border px-4 py-2.5">
            <div className="flex items-center gap-2 text-sm font-medium">
              <Radio size={15} className={connected ? "text-emerald-400" : "text-slate-500"} /> Live stream
            </div>
            <button
              onClick={() => setMessages([])}
              className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-slate-500 hover:bg-white/5 hover:text-slate-200"
            >
              <Trash2 size={13} /> Clear
            </button>
          </div>
          <div className="max-h-[60vh] min-h-[16rem] overflow-y-auto p-3 font-mono text-[13px]">
            {messages.length === 0 ? (
              <div className="flex h-40 items-center justify-center text-sm text-slate-500">
                {connected ? "Listening… publish a message to see it here." : "Subscribe to start streaming."}
              </div>
            ) : (
              <ul className="space-y-1">
                {messages.map((m, i) => (
                  <li key={i} className="flex items-start gap-3 rounded px-2 py-1 hover:bg-white/5">
                    <span className="shrink-0 text-slate-600">{timeOf(m.at)}</span>
                    <span className="shrink-0 rounded bg-primary/15 px-1.5 text-primary">{m.channel}</span>
                    {m.pattern ? (
                      <span className="shrink-0 rounded bg-accent/15 px-1.5 text-accent">{m.pattern}</span>
                    ) : null}
                    <span className="min-w-0 break-words text-slate-200">{m.payload}</span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>
      </div>
    </>
  );
}
