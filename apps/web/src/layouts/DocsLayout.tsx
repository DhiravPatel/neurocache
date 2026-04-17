import { Link, NavLink, Outlet, useLocation } from "react-router-dom";
import { ChevronRight } from "lucide-react";
import clsx from "clsx";
import { SiteHeader, SiteFooter } from "../components/SiteHeader";

type DocLink = { to: string; label: string };
type DocSection = { title: string; items: DocLink[] };

export const docsNav: DocSection[] = [
  {
    title: "Getting Started",
    items: [
      { to: "/docs",              label: "Introduction" },
      { to: "/docs/installation", label: "Installation" },
      { to: "/docs/quickstart",   label: "Quick Start" },
    ],
  },
  {
    title: "Core Concepts",
    items: [
      { to: "/docs/commands",       label: "Commands Reference" },
      { to: "/docs/semantic-cache", label: "Semantic Cache" },
      { to: "/docs/llm-cache",      label: "LLM Response Cache" },
      { to: "/docs/memory",         label: "User Memory Store" },
    ],
  },
  {
    title: "Reference",
    items: [
      { to: "/docs/configuration", label: "Configuration" },
      { to: "/docs/sdks",          label: "SDKs & Clients" },
      { to: "/docs/architecture",  label: "Architecture" },
      { to: "/docs/deployment",    label: "Deployment" },
    ],
  },
];

export default function DocsLayout() {
  return (
    <div className="min-h-full">
      <SiteHeader />
      <div className="mx-auto grid max-w-6xl grid-cols-[200px_1fr] gap-8 px-6 py-8">
        <aside className="sticky top-16 h-[calc(100vh-5rem)] self-start overflow-y-auto">
          {docsNav.map((section) => (
            <div key={section.title} className="mb-5">
              <div className="mb-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-500">
                {section.title}
              </div>
              <nav className="space-y-0.5">
                {section.items.map((l) => (
                  <NavLink
                    key={l.to}
                    to={l.to}
                    end={l.to === "/docs"}
                    className={({ isActive }) =>
                      clsx(
                        "block rounded-md px-2 py-1 text-sm transition-colors",
                        isActive
                          ? "bg-primary/10 font-semibold text-primary"
                          : "text-slate-400 hover:bg-slate-100/60 hover:text-slate-100 dark:hover:bg-white/5",
                      )
                    }
                  >
                    {l.label}
                  </NavLink>
                ))}
              </nav>
            </div>
          ))}
        </aside>
        <main className="min-w-0">
          <div className="prose prose-invert max-w-none">
            <Outlet />
          </div>
          <DocsNextPrev />
        </main>
      </div>
      <SiteFooter />
    </div>
  );
}

/** Bottom-of-page "Previous / Next" navigation computed from docsNav order. */
function DocsNextPrev() {
  const { pathname } = useLocation();
  const flat: DocLink[] = docsNav.flatMap((s) => s.items);
  const idx = flat.findIndex((l) => l.to === pathname);
  if (idx === -1) return null;
  const prev = idx > 0 ? flat[idx - 1] : null;
  const next = idx < flat.length - 1 ? flat[idx + 1] : null;
  return (
    <div className="mt-12 flex items-center justify-between border-t border-border pt-6 text-sm">
      {prev ? (
        <Link to={prev.to} className="flex items-center gap-2 text-slate-400 hover:text-slate-100">
          <ChevronRight size={14} className="rotate-180" /> {prev.label}
        </Link>
      ) : (
        <span />
      )}
      {next ? (
        <Link to={next.to} className="ml-auto flex items-center gap-2 text-slate-400 hover:text-slate-100">
          {next.label} <ChevronRight size={14} />
        </Link>
      ) : null}
    </div>
  );
}
