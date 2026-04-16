import clsx from "clsx";
import type { LucideIcon } from "lucide-react";

export function Stat({
  label,
  value,
  hint,
  icon: Icon,
  accent,
}: {
  label: string;
  value: string | number;
  hint?: string;
  icon?: LucideIcon;
  accent?: "primary" | "accent" | "emerald" | "rose";
}) {
  return (
    <div className="card p-4">
      <div className="flex items-center gap-2 text-xs uppercase tracking-wider text-slate-500">
        {Icon ? <Icon size={14} /> : null}
        {label}
      </div>
      <div
        className={clsx(
          "mt-2 text-2xl font-semibold tabular-nums",
          accent === "primary" && "text-primary",
          accent === "accent" && "text-accent",
          accent === "emerald" && "text-emerald-400",
          accent === "rose" && "text-rose-400",
        )}
      >
        {value}
      </div>
      {hint ? <div className="mt-1 text-xs text-slate-500">{hint}</div> : null}
    </div>
  );
}

export function PageHeader({ title, subtitle }: { title: string; subtitle?: string }) {
  return (
    <div className="mb-6">
      <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
      {subtitle ? <p className="mt-1 text-sm text-slate-400">{subtitle}</p> : null}
    </div>
  );
}
