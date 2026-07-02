import { useEffect, useRef, useState } from "react";
import { Link, NavLink, Outlet, useLocation } from "react-router-dom";
import {
  ChevronRight,
  ArrowLeft,
  ArrowRight,
  BookOpen,
  Download,
  Rocket,
  Terminal,
  Sparkles,
  Zap,
  Brain,
  DollarSign,
  Radio,
  Lock,
  Gauge,
  Trophy,
  ListChecks,
  Waves,
  MessagesSquare,
  FileText,
  FlaskConical,
  ShieldCheck,
  Flag,
  SlidersHorizontal,
  Boxes,
  Network,
  Cloud,
  Menu,
  X,
  PencilLine,
  type LucideIcon,
} from "lucide-react";
import clsx from "clsx";
import { SiteHeader, SiteFooter } from "../components/SiteHeader";
import { DocsToc } from "../components/docs/DocsToc";

type DocLink = { to: string; label: string; icon: LucideIcon; file: string };
type DocSection = { title: string; items: DocLink[] };

const GITHUB_BASE =
  "https://github.com/dhiravpatel/neurocache/blob/main/apps/web/src/pages/docs";

export const docsNav: DocSection[] = [
  {
    title: "Getting Started",
    items: [
      { to: "/docs",              label: "Introduction", icon: BookOpen, file: "Index.tsx" },
      { to: "/docs/installation", label: "Installation",  icon: Download, file: "Installation.tsx" },
      { to: "/docs/quickstart",   label: "Quick Start",   icon: Rocket,   file: "QuickStart.tsx" },
    ],
  },
  {
    title: "Core Concepts",
    items: [
      { to: "/docs/commands",       label: "Commands Reference", icon: Terminal, file: "Commands.tsx" },
      { to: "/docs/semantic-cache", label: "Semantic Cache",     icon: Sparkles, file: "SemanticCache.tsx" },
      { to: "/docs/llm-cache",      label: "LLM Response Cache",  icon: Zap,      file: "LLMCache.tsx" },
      { to: "/docs/memory",         label: "User Memory Store",   icon: Brain,    file: "Memory.tsx" },
      { to: "/docs/conversations",  label: "Conversations",       icon: MessagesSquare, file: "Conversations.tsx" },
      { to: "/docs/prompts",        label: "Prompt Templates",    icon: FileText, file: "Prompts.tsx" },
      { to: "/docs/experiments",    label: "A/B Experiments",     icon: FlaskConical, file: "Experiments.tsx" },
      { to: "/docs/costs",          label: "Cost & Budgets",      icon: DollarSign, file: "Costs.tsx" },
      { to: "/docs/pubsub",         label: "Pub/Sub",             icon: Radio,    file: "PubSub.tsx" },
      { to: "/docs/locks",          label: "Distributed Locks",   icon: Lock,     file: "Locks.tsx" },
    ],
  },
  {
    title: "Primitives",
    items: [
      { to: "/docs/rate-limiting", label: "Rate Limiting", icon: Gauge,      file: "RateLimiting.tsx" },
      { to: "/docs/leaderboards",  label: "Leaderboards",  icon: Trophy,     file: "Leaderboards.tsx" },
      { to: "/docs/queues",        label: "Queues",        icon: ListChecks, file: "Queues.tsx" },
      { to: "/docs/streams",       label: "Streams",       icon: Waves,      file: "Streams.tsx" },
      { to: "/docs/graph",         label: "Knowledge Graph", icon: Network,  file: "KnowledgeGraph.tsx" },
      { to: "/docs/moderation",    label: "Moderation",    icon: ShieldCheck, file: "Moderation.tsx" },
      { to: "/docs/feature-flags", label: "Feature Flags", icon: Flag,       file: "FeatureFlags.tsx" },
    ],
  },
  {
    title: "Reference",
    items: [
      { to: "/docs/pipelining",    label: "Pipelining & throughput", icon: Zap, file: "Pipelining.tsx" },
      { to: "/docs/configuration", label: "Configuration", icon: SlidersHorizontal, file: "Configuration.tsx" },
      { to: "/docs/sdks",          label: "SDKs & Clients", icon: Boxes,            file: "SDKs.tsx" },
      { to: "/docs/architecture",  label: "Architecture",   icon: Network,          file: "Architecture.tsx" },
      { to: "/docs/deployment",    label: "Deployment",     icon: Cloud,            file: "Deployment.tsx" },
    ],
  },
];

const FLAT: DocLink[] = docsNav.flatMap((s) => s.items);

/** The Commands reference manages its own full-width layout + TOC. */
const isWidePage = (pathname: string) => pathname === "/docs/commands";

export default function DocsLayout() {
  const { pathname } = useLocation();
  const contentRef = useRef<HTMLElement>(null);
  const openBtnRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const wide = isWidePage(pathname);

  // Close the mobile drawer + jump to top whenever the route changes.
  useEffect(() => {
    setMobileNavOpen(false);
    window.scrollTo(0, 0);
  }, [pathname]);

  // While the mobile drawer is open: lock body scroll, move focus into the
  // panel, trap Tab within it, close on Escape, and restore focus to the
  // trigger button on close.
  useEffect(() => {
    if (!mobileNavOpen) return;
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    const focusables = () =>
      panelRef.current
        ? Array.from(
            panelRef.current.querySelectorAll<HTMLElement>(
              'a[href], button:not([disabled])',
            ),
          )
        : [];
    focusables()[0]?.focus();

    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setMobileNavOpen(false);
        return;
      }
      if (e.key !== "Tab") return;
      const f = focusables();
      if (f.length === 0) return;
      const first = f[0];
      const last = f[f.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => {
      document.body.style.overflow = prevOverflow;
      document.removeEventListener("keydown", onKey);
      openBtnRef.current?.focus();
    };
  }, [mobileNavOpen]);

  const current = FLAT.find((l) => l.to === pathname);
  const section = docsNav.find((s) => s.items.some((i) => i.to === pathname));

  return (
    <div className="relative min-h-full">
      {/* decorative backdrop — soft primary glow + faint grid fading downward */}
      <div
        aria-hidden
        className="pointer-events-none absolute inset-x-0 top-0 -z-10 h-[420px] grid-bg opacity-50 dark:opacity-30"
      />
      <div
        aria-hidden
        className="pointer-events-none absolute inset-x-0 top-0 -z-10 h-[420px]"
        style={{
          background:
            "radial-gradient(65% 100% at 50% -10%, rgb(var(--primary) / 0.12), transparent 70%)",
        }}
      />

      <SiteHeader />

      {/* mobile sub-bar: hamburger + breadcrumb trail (fixed 48px height so
          pages with their own sticky chrome can offset below it) */}
      <div className="sticky top-14 z-30 flex h-12 items-center gap-3 border-b border-border/60 bg-bg/85 px-5 backdrop-blur sm:px-6 lg:hidden">
        <button
          ref={openBtnRef}
          onClick={() => setMobileNavOpen(true)}
          className="btn-ghost -ml-1 px-2"
          aria-label="Open documentation menu"
        >
          <Menu size={16} /> Menu
        </button>
        <span className="truncate text-sm text-slate-400">
          {section ? `${section.title} · ` : ""}
          <span className="text-slate-200">{current?.label ?? "Docs"}</span>
        </span>
      </div>

      <div className="mx-auto grid max-w-7xl gap-0 px-0 sm:px-6 lg:grid-cols-[244px_minmax(0,1fr)] lg:gap-10 xl:max-w-[1400px] 2xl:max-w-[1520px]">
        {/* ── Desktop sidebar ─────────────────────────────────────── */}
        <aside className="hidden lg:block">
          <div className="sticky top-14 max-h-[calc(100vh-3.5rem)] overflow-y-auto py-8 pr-2">
            <SidebarNav pathname={pathname} />
          </div>
        </aside>

        {/* ── Content + right rail ────────────────────────────────── */}
        <div
          className={clsx(
            "min-w-0 px-5 py-8 sm:px-0",
            !wide && "xl:grid xl:grid-cols-[minmax(0,1fr)_208px] xl:gap-10",
          )}
        >
          <main ref={contentRef} className="min-w-0">
            {current && <Breadcrumb section={section?.title} label={current.label} />}

            <div className={clsx("prose dark:prose-invert break-words", wide ? "max-w-none" : "max-w-3xl")}>
              <Outlet />
            </div>

            <PageFooter pathname={pathname} file={current?.file} />
          </main>

          {!wide && (
            <div className="hidden xl:block">
              <div className="sticky top-20 max-h-[calc(100vh-6rem)] overflow-y-auto py-8">
                <DocsToc contentRef={contentRef} pathname={pathname} />
              </div>
            </div>
          )}
        </div>
      </div>

      {/* ── Mobile drawer ─────────────────────────────────────────── */}
      {mobileNavOpen && (
        <div className="fixed inset-0 z-40 lg:hidden">
          <div
            aria-hidden
            onClick={() => setMobileNavOpen(false)}
            className="absolute inset-0 animate-fade-in bg-black/50 backdrop-blur-sm"
          />
          <div
            ref={panelRef}
            role="dialog"
            aria-modal="true"
            aria-labelledby="docs-drawer-title"
            className="absolute inset-y-0 left-0 w-[280px] max-w-[82vw] overflow-y-auto border-r border-border bg-bg p-5 shadow-2xl"
          >
            <div className="mb-4 flex items-center justify-between">
              <span id="docs-drawer-title" className="text-sm font-semibold">
                Documentation
              </span>
              <button
                onClick={() => setMobileNavOpen(false)}
                className="btn-ghost px-2"
                aria-label="Close menu"
              >
                <X size={16} />
              </button>
            </div>
            <SidebarNav pathname={pathname} />
          </div>
        </div>
      )}

      <SiteFooter />
    </div>
  );
}

/* ─── Sidebar navigation (shared between desktop + mobile drawer) ──── */
function SidebarNav({ pathname }: { pathname: string }) {
  return (
    <nav aria-label="Docs sections" className="space-y-6">
      {docsNav.map((sec) => (
        <div key={sec.title}>
          <div className="mb-2 px-2 text-[11px] font-semibold uppercase tracking-wider text-slate-400">
            {sec.title}
          </div>
          <div className="space-y-0.5">
            {sec.items.map((l) => {
              const Icon = l.icon;
              const active = pathname === l.to;
              return (
                <NavLink
                  key={l.to}
                  to={l.to}
                  end={l.to === "/docs"}
                  className={clsx(
                    "group flex items-center gap-2.5 rounded-lg px-2 py-1.5 text-sm transition-colors",
                    active
                      ? "bg-primary/10 font-medium text-primary-strong"
                      : "text-slate-400 hover:bg-slate-500/10 hover:text-slate-100 dark:hover:bg-white/5",
                  )}
                >
                  <Icon
                    size={15}
                    className={clsx(
                      "shrink-0 transition-colors",
                      active ? "text-primary-strong" : "text-slate-500 group-hover:text-slate-300",
                    )}
                  />
                  {l.label}
                </NavLink>
              );
            })}
          </div>
        </div>
      ))}
    </nav>
  );
}

/* ─── Breadcrumb shown above each page's <h1> ──────────────────────── */
function Breadcrumb({ section, label }: { section?: string; label: string }) {
  return (
    <div className="mb-5 flex items-center gap-1.5 text-[13px] text-slate-500">
      <Link to="/docs" className="transition-colors hover:text-slate-300">
        Docs
      </Link>
      {section && (
        <>
          <ChevronRight size={13} className="text-slate-600" />
          <span className="text-slate-400">{section}</span>
        </>
      )}
      <ChevronRight size={13} className="text-slate-600" />
      <span className="font-medium text-slate-300">{label}</span>
    </div>
  );
}

/* ─── Bottom-of-page: edit link + prev / next cards ────────────────── */
function PageFooter({ pathname, file }: { pathname: string; file?: string }) {
  const idx = FLAT.findIndex((l) => l.to === pathname);
  const prev = idx > 0 ? FLAT[idx - 1] : null;
  const next = idx >= 0 && idx < FLAT.length - 1 ? FLAT[idx + 1] : null;

  return (
    <footer className="mt-14 border-t border-border pt-6">
      {file && (
        <a
          href={`${GITHUB_BASE}/${file}`}
          target="_blank"
          rel="noreferrer"
          className="mb-6 inline-flex items-center gap-1.5 text-[13px] text-slate-500 transition-colors hover:text-slate-300"
        >
          <PencilLine size={13} /> Edit this page on GitHub
        </a>
      )}
      <div className="grid gap-3 sm:grid-cols-2">
        {prev ? (
          <Link
            to={prev.to}
            className="card group flex flex-col gap-1 p-4 no-underline transition-colors hover:border-primary/40"
          >
            <span className="flex items-center gap-1 text-xs text-slate-500">
              <ArrowLeft size={13} /> Previous
            </span>
            <span className="text-sm font-medium text-slate-200 group-hover:text-primary">
              {prev.label}
            </span>
          </Link>
        ) : (
          <span className="hidden sm:block" />
        )}
        {next ? (
          <Link
            to={next.to}
            className="card group flex flex-col items-end gap-1 p-4 text-right no-underline transition-colors hover:border-primary/40"
          >
            <span className="flex items-center gap-1 text-xs text-slate-500">
              Next <ArrowRight size={13} />
            </span>
            <span className="text-sm font-medium text-slate-200 group-hover:text-primary">
              {next.label}
            </span>
          </Link>
        ) : null}
      </div>
    </footer>
  );
}
