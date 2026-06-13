import {
  Info,
  Lightbulb,
  TriangleAlert,
  OctagonAlert,
  Sparkles,
  type LucideIcon,
} from "lucide-react";
import clsx from "clsx";

type Variant = "note" | "tip" | "info" | "warning" | "danger";

const VARIANTS: Record<
  Variant,
  { icon: LucideIcon; label: string; ring: string; iconWrap: string; bar: string }
> = {
  note: {
    icon: Info,
    label: "Note",
    ring: "border-slate-300/60 bg-slate-500/[0.06] dark:border-white/10 dark:bg-white/[0.03]",
    iconWrap: "bg-slate-500/15 text-slate-500 dark:text-slate-300",
    bar: "bg-slate-400/70",
  },
  tip: {
    icon: Lightbulb,
    label: "Tip",
    ring: "border-emerald-400/30 bg-emerald-500/[0.07]",
    iconWrap: "bg-emerald-500/15 text-emerald-500 dark:text-emerald-400",
    bar: "bg-emerald-500/80",
  },
  info: {
    icon: Sparkles,
    label: "Good to know",
    ring: "border-sky-400/30 bg-sky-500/[0.07]",
    iconWrap: "bg-sky-500/15 text-sky-500 dark:text-sky-400",
    bar: "bg-sky-500/80",
  },
  warning: {
    icon: TriangleAlert,
    label: "Warning",
    ring: "border-amber-400/30 bg-amber-500/[0.08]",
    iconWrap: "bg-amber-500/15 text-amber-600 dark:text-amber-400",
    bar: "bg-amber-500/80",
  },
  danger: {
    icon: OctagonAlert,
    label: "Careful",
    ring: "border-rose-400/30 bg-rose-500/[0.07]",
    iconWrap: "bg-rose-500/15 text-rose-500 dark:text-rose-400",
    bar: "bg-rose-500/80",
  },
};

/**
 * Admonition / callout block for docs prose. Drop inside a page; uses
 * `not-prose` internally so the surrounding typography styles don't fight
 * its layout.
 *
 *   <Callout type="tip" title="Pro tip">…</Callout>
 */
export function Callout({
  type = "note",
  title,
  children,
}: {
  type?: Variant;
  title?: string;
  children: React.ReactNode;
}) {
  const v = VARIANTS[type];
  const Icon = v.icon;
  return (
    <div
      className={clsx(
        "not-prose group relative my-6 overflow-hidden rounded-xl border pl-4 pr-4 py-4 backdrop-blur-sm",
        v.ring,
      )}
    >
      <span className={clsx("absolute inset-y-0 left-0 w-1", v.bar)} aria-hidden />
      <div className="flex gap-3">
        <span
          className={clsx(
            "mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-lg",
            v.iconWrap,
          )}
        >
          <Icon size={15} strokeWidth={2.25} />
        </span>
        <div className="min-w-0 flex-1">
          <div className="text-[13px] font-semibold tracking-wide text-slate-100">
            {title ?? v.label}
          </div>
          <div className="mt-1 text-sm leading-relaxed text-slate-300 [&_a]:font-medium [&_a]:text-accent [&_a:hover]:underline [&_code]:rounded [&_code]:bg-black/20 [&_code]:px-1.5 [&_code]:py-0.5 [&_code]:font-mono [&_code]:text-[0.85em] dark:[&_code]:bg-white/10">
            {children}
          </div>
        </div>
      </div>
    </div>
  );
}
