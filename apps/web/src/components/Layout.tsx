import { NavLink, Outlet } from "react-router-dom";
import { Activity, Database, Brain, Zap, Sparkles, Terminal, BarChart3 } from "lucide-react";
import clsx from "clsx";
import { usePolling } from "../lib/usePolling";
import { api } from "../lib/api";

const navItems = [
  { to: "/",            label: "Dashboard",  icon: Activity  },
  { to: "/analytics",   label: "Analytics",  icon: BarChart3 },
  { to: "/kv",          label: "Key-Value",  icon: Database  },
  { to: "/semantic",    label: "Semantic",   icon: Sparkles  },
  { to: "/llm",         label: "LLM Cache",  icon: Zap       },
  { to: "/memory",      label: "Memory",     icon: Brain     },
  { to: "/playground",  label: "Playground", icon: Terminal  },
];

export default function Layout() {
  const { data: health } = usePolling(api.health, 5000);
  const online = !!health;

  return (
    <div className="flex min-h-full">
      <aside className="w-60 shrink-0 border-r border-border bg-surface/40 backdrop-blur-sm">
        <div className="flex h-14 items-center gap-2 border-b border-border px-4">
          <div className="h-6 w-6 rounded-md bg-gradient-to-br from-primary to-accent" />
          <div className="text-sm font-semibold tracking-wide">NeuroCache</div>
          <span className={clsx("ml-auto h-2 w-2 rounded-full", online ? "bg-emerald-400" : "bg-rose-500")} />
        </div>
        <nav className="px-2 py-3 space-y-0.5">
          {navItems.map(({ to, label, icon: Icon }) => (
            <NavLink
              key={to}
              to={to}
              end={to === "/"}
              className={({ isActive }) =>
                clsx(
                  "flex items-center gap-3 rounded-md px-3 py-2 text-sm transition-colors",
                  isActive
                    ? "bg-primary/15 text-white"
                    : "text-slate-400 hover:text-slate-100 hover:bg-white/5",
                )
              }
            >
              <Icon size={16} />
              {label}
            </NavLink>
          ))}
        </nav>
        <div className="mt-auto border-t border-border p-4 text-[11px] text-slate-500">
          v0.1.0 · {import.meta.env.VITE_API_URL || "same origin"}
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
