import { useState } from "react";
import { Check, Copy } from "lucide-react";
import clsx from "clsx";

/**
 * Styled code block with copy button. Used on the landing page and
 * throughout the docs.
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
    <div className={clsx("group relative rounded-lg border border-border bg-[#06080d]", className)}>
      {lang ? (
        <div className="flex items-center justify-between border-b border-border px-3 py-1.5 text-[11px] uppercase tracking-wider text-slate-500">
          <span>{lang}</span>
        </div>
      ) : null}
      <pre className="overflow-x-auto px-4 py-3 font-mono text-[13px] leading-relaxed text-slate-200">
        <code>{children}</code>
      </pre>
      <button
        onClick={onCopy}
        className="absolute right-2 top-2 rounded-md border border-border bg-bg/80 p-1.5 text-slate-400 opacity-0 transition-opacity hover:text-slate-100 group-hover:opacity-100"
        aria-label="Copy to clipboard"
      >
        {copied ? <Check size={13} className="text-emerald-400" /> : <Copy size={13} />}
      </button>
    </div>
  );
}

/** Inline code tag. */
export function C({ children }: { children: React.ReactNode }) {
  return (
    <code className="rounded bg-bg px-1.5 py-0.5 font-mono text-[0.85em] text-primary">
      {children}
    </code>
  );
}
