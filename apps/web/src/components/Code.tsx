import { useMemo, useState } from "react";
import { Check, Copy } from "lucide-react";
import clsx from "clsx";
import { tokenize } from "../lib/highlight";

/**
 * Styled code block with VS Code-inspired syntax highlighting and a copy
 * button. Token colors come from CSS variables that flip between Light+
 * and Dark+ palettes based on the active theme.
 */
export function Code({
  children,
  lang,
  className,
}: {
  children: string;
  lang?: string;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);
  const tokens = useMemo(() => tokenize(children, lang), [children, lang]);

  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(children);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* ignore */
    }
  };

  return (
    <div
      className={clsx(
        "group relative overflow-hidden rounded-lg border bg-code",
        "border-code-border",
        className,
      )}
    >
      {lang ? (
        <div className="flex items-center justify-between border-b border-white/10 bg-white/5 px-4 py-2 text-[11px] uppercase tracking-wider text-white/40">
          <span>{lang}</span>
        </div>
      ) : null}
      <pre className="overflow-x-auto px-4 py-3 font-mono text-[13px] leading-relaxed text-code-plain">
        <code>
          {tokens.map((t, i) =>
            t.type === "plain" ? (
              <span key={i}>{t.text}</span>
            ) : (
              <span key={i} className={`tok-${t.type}`}>
                {t.text}
              </span>
            ),
          )}
        </code>
      </pre>
      <button
        onClick={onCopy}
        className="absolute right-2 top-2 rounded-md border border-white/10 bg-white/10 p-1.5 text-white/40 opacity-0 transition-all hover:bg-white/20 hover:text-white group-hover:opacity-100"
        aria-label="Copy to clipboard"
      >
        {copied ? <Check size={13} className="text-emerald-500" /> : <Copy size={13} />}
      </button>
    </div>
  );
}

/** Inline code tag used outside of prose context (e.g. feature cards). */
export function C({ children }: { children: React.ReactNode }) {
  return (
    <code
      className="rounded-[5px] px-1.5 py-0.5 font-mono text-[0.85em] font-medium"
      style={{
        backgroundColor: "rgb(var(--inline-code-bg))",
        color: "rgb(var(--inline-code-text))",
        border: "1px solid rgb(var(--inline-code-border))",
      }}
    >
      {children}
    </code>
  );
}
