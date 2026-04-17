import { Link, NavLink, Outlet } from "react-router-dom";
import {
  Activity, Database, Brain, Zap, Sparkles, Terminal, BarChart3,
  BookOpen, ExternalLink,
} from "lucide-react";
import clsx from "clsx";
import { usePolling } from "../lib/usePolling";
import { api } from "../lib/api";
import { ThemeToggle } from "./ThemeToggle";

const navItems = [
  { to: "/dashboard",            label: "Dashboard",  icon: Activity,  end: true },
  { to: "/dashboard/analytics",  label: "Analytics",  icon: BarChart3 },
  { to: "/dashboard/kv",         label: "Key-Value",  icon: Database  },
  { to: "/dashboard/semantic",   label: "Semantic",   icon: Sparkles  },
  { to: "/dashboard/llm",        label: "LLM Cache",  icon: Zap       },
  { to: "/dashboard/memory",     label: "Memory",     icon: Brain     },
  { to: "/dashboard/playground", label: "Playground", icon: Terminal  },
];

export default function Layout() {
  const { data: health } = usePolling(api.health, 5000);
  const online = !!health;

  return (
    <div className="flex min-h-full">
      <aside className="flex w-60 shrink-0 flex-col border-r border-border bg-surface/40 backdrop-blur-sm">
        <Link
          to="/"
          className="flex h-14 items-center gap-2 border-b border-border px-4 hover:bg-white/5"
        >
          <div className="h-6 w-6 rounded-md bg-gradient-to-br from-primary to-accent" />
          <div className="text-sm font-semibold tracking-wide">NeuroCache</div>
          <span
            className={clsx(
              "ml-auto h-2 w-2 rounded-full",
              online ? "bg-emerald-400" : "bg-rose-500",
            )}
            title={online ? "engine online" : "engine offline"}
          />
        </Link>

        <nav className="flex-1 overflow-y-auto px-2 py-3">
          <div className="mb-1.5 px-2 text-[11px] font-semibold uppercase tracking-wider text-slate-500">
            Engine
          </div>
          <div className="space-y-0.5">
            {navItems.map(({ to, label, icon: Icon, end }) => (
              <NavLink
                key={to}
                to={to}
                end={end}
                className={({ isActive }) =>
                  clsx(
                    "flex items-center gap-3 rounded-md px-3 py-2 text-sm transition-all",
                    isActive
                      ? "bg-primary font-semibold text-white shadow-sm shadow-primary/30 ring-1 ring-primary/40"
                      : "text-slate-400 hover:bg-slate-100/60 hover:text-slate-100 dark:hover:bg-white/5",
                  )
                }
              >
                <Icon size={16} />
                {label}
              </NavLink>
            ))}
          </div>

          <div className="mt-5 mb-1.5 px-2 text-[11px] font-semibold uppercase tracking-wider text-slate-500">
            Resources
          </div>
          <div className="space-y-0.5">
            <Link
              to="/docs"
              className="flex items-center gap-3 rounded-md px-3 py-2 text-sm text-slate-400 transition-colors hover:bg-slate-100/60 hover:text-slate-100 dark:hover:bg-white/5"
            >
              <BookOpen size={16} /> Docs
            </Link>
            <Link
              to="/"
              className="flex items-center gap-3 rounded-md px-3 py-2 text-sm text-slate-400 transition-colors hover:bg-slate-100/60 hover:text-slate-100 dark:hover:bg-white/5"
            >
              <ExternalLink size={16} /> Landing
            </Link>
          </div>
        </nav>

        <div className="flex items-center justify-between gap-2 border-t border-border p-4 text-[11px] text-slate-500">
          <span>v0.1.0 · {import.meta.env.VITE_API_URL || "same origin"}</span>
          <ThemeToggle />
        </div>
      </aside>
      <main className="flex-1 overflow-y-auto">
        <div className="mx-auto max-w-6xl px-6 py-8">
          <Outlet />
        </div>
      </main>
    </div>
  );
}
