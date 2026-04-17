import { Link, NavLink } from "react-router-dom";
import { Github, Book, LayoutDashboard } from "lucide-react";
import clsx from "clsx";
import { ThemeToggle } from "./ThemeToggle";

export function SiteHeader() {
  return (
    <header className="sticky top-0 z-30 border-b border-border/60 bg-bg/80 backdrop-blur">
      <div className="mx-auto flex h-14 max-w-6xl items-center gap-6 px-6">
        <Link to="/" className="flex items-center gap-2">
          <div className="h-6 w-6 rounded-md bg-gradient-to-br from-primary to-accent" />
          <span className="text-sm font-semibold tracking-wide">NeuroCache</span>
        </Link>
        <nav className="hidden items-center gap-1 md:flex">
          <HeaderLink to="/docs">Docs</HeaderLink>
          <HeaderLink to="/dashboard">Dashboard</HeaderLink>
        </nav>
        <div className="ml-auto flex items-center gap-2">
          <ThemeToggle />
          <a
            href="https://github.com/dhiravpatel/neurocache"
            target="_blank"
            rel="noreferrer"
            className="btn-ghost hidden md:inline-flex"
          >
            <Github size={14} /> GitHub
          </a>
          <Link to="/docs" className="btn-ghost md:hidden">
            <Book size={14} />
          </Link>
          <Link to="/dashboard" className="btn-primary">
            <LayoutDashboard size={14} /> Open Dashboard
          </Link>
        </div>
      </div>
    </header>
  );
}

function HeaderLink({ to, children }: { to: string; children: React.ReactNode }) {
  return (
    <NavLink
      to={to}
      className={({ isActive }) =>
        clsx(
          "rounded-md px-3 py-1.5 text-sm transition-colors",
          isActive ? "bg-white/5 text-slate-100" : "text-slate-400 hover:text-slate-100",
        )
      }
    >
      {children}
    </NavLink>
  );
}

export function SiteFooter() {
  return (
    <footer className="mt-24 border-t border-border bg-surface/30">
      <div className="mx-auto grid max-w-6xl grid-cols-2 gap-8 px-6 py-10 md:grid-cols-4">
        <div>
          <div className="flex items-center gap-2">
            <div className="h-5 w-5 rounded-md bg-gradient-to-br from-primary to-accent" />
            <span className="text-sm font-semibold">NeuroCache</span>
          </div>
          <p className="mt-2 text-xs text-slate-500">
            The memory layer for AI applications. MIT licensed, self-hostable.
          </p>
        </div>
        <FooterCol
          title="Product"
          links={[
            { href: "/dashboard", label: "Dashboard" },
            { href: "/docs/quickstart", label: "Quick Start" },
            { href: "/docs/commands", label: "Commands" },
          ]}
        />
        <FooterCol
          title="Docs"
          links={[
            { href: "/docs/installation", label: "Installation" },
            { href: "/docs/semantic-cache", label: "Semantic Cache" },
            { href: "/docs/memory", label: "Memory Store" },
            { href: "/docs/deployment", label: "Deployment" },
          ]}
        />
        <FooterCol
          title="Community"
          links={[
            { href: "https://github.com/dhiravpatel/neurocache", label: "GitHub", external: true },
            { href: "https://github.com/dhiravpatel/neurocache/issues", label: "Issues", external: true },
            { href: "/docs/architecture", label: "Architecture" },
          ]}
        />
      </div>
      <div className="border-t border-border py-4 text-center text-xs text-slate-500">
        © {new Date().getFullYear()} NeuroCache · MIT License
      </div>
    </footer>
  );
}

function FooterCol({
  title,
  links,
}: {
  title: string;
  links: { href: string; label: string; external?: boolean }[];
}) {
  return (
    <div>
      <div className="mb-2 text-xs font-semibold uppercase tracking-wider text-slate-400">{title}</div>
      <ul className="space-y-1.5 text-sm">
        {links.map((l) =>
          l.external ? (
            <li key={l.href}>
              <a
                href={l.href}
                target="_blank"
                rel="noreferrer"
                className="text-slate-400 hover:text-slate-100"
              >
                {l.label}
              </a>
            </li>
          ) : (
            <li key={l.href}>
              <Link to={l.href} className="text-slate-400 hover:text-slate-100">
                {l.label}
              </Link>
            </li>
          ),
        )}
      </ul>
    </div>
  );
}
