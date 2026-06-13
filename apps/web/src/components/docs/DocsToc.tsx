import { useEffect, useState, type RefObject } from "react";
import { List } from "lucide-react";
import clsx from "clsx";

type Heading = { id: string; text: string; level: 2 | 3 };

function slugify(text: string): string {
  return text
    .toLowerCase()
    .replace(/[^\w\s-]/g, "")
    .trim()
    .replace(/\s+/g, "-")
    .replace(/-+/g, "-");
}

/**
 * Reads the rendered <h2>/<h3> elements inside `contentRef`, assigns stable
 * ids + hover anchor links, and renders a sticky "On this page" rail with
 * scroll-spy highlighting. Re-runs whenever `pathname` changes so each doc
 * page gets a fresh table of contents.
 */
export function DocsToc({
  contentRef,
  pathname,
}: {
  contentRef: RefObject<HTMLElement>;
  pathname: string;
}) {
  const [headings, setHeadings] = useState<Heading[]>([]);
  const [activeId, setActiveId] = useState<string>("");

  useEffect(() => {
    const root = contentRef.current;
    if (!root) return;

    const els = Array.from(
      root.querySelectorAll<HTMLHeadingElement>("h2, h3"),
    );
    const seen = new Map<string, number>();
    const collected: Heading[] = [];

    for (const el of els) {
      const text = (el.textContent ?? "").replace(/#$/, "").trim();
      if (!text) continue;
      let id = el.id || slugify(text);
      // de-duplicate slugs (e.g. two "Examples" headings)
      const n = seen.get(id) ?? 0;
      seen.set(id, n + 1);
      if (n > 0) id = `${id}-${n}`;
      el.id = id;

      // inject a hover "#" anchor once
      if (!el.querySelector(".heading-anchor")) {
        const a = document.createElement("a");
        a.href = `#${id}`;
        a.className = "heading-anchor";
        a.textContent = "#";
        a.setAttribute("aria-label", "Direct link to section");
        el.appendChild(a);
      }

      collected.push({ id, text, level: el.tagName === "H3" ? 3 : 2 });
    }

    setHeadings(collected);
    setActiveId(collected[0]?.id ?? "");

    if (collected.length === 0) return;

    const visible = new Set<string>();
    const observer = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (e.isIntersecting) visible.add(e.target.id);
          else visible.delete(e.target.id);
        }
        // pick the first heading (document order) currently in the band
        const firstVisible = collected.find((h) => visible.has(h.id));
        if (firstVisible) setActiveId(firstVisible.id);
      },
      // band sits just below the sticky header; nothing counts as "active"
      // until it crosses into the top third of the viewport
      { rootMargin: "-96px 0px -66% 0px", threshold: 0 },
    );

    els.forEach((el) => observer.observe(el));
    return () => observer.disconnect();
  }, [contentRef, pathname]);

  if (headings.length < 2) return null;

  return (
    <nav aria-label="On this page" className="text-sm">
      <div className="mb-3 flex items-center gap-2 text-[11px] font-semibold uppercase tracking-wider text-slate-400">
        <List size={13} /> On this page
      </div>
      <ul className="space-y-0.5 border-l border-border">
        {headings.map((h) => {
          const active = h.id === activeId;
          return (
            <li key={h.id}>
              <a
                href={`#${h.id}`}
                className={clsx(
                  "-ml-px block border-l py-1 text-[13px] leading-snug transition-colors",
                  h.level === 3 ? "pl-7" : "pl-4",
                  active
                    ? "border-primary-strong font-medium text-primary-strong"
                    : "border-transparent text-slate-400 hover:border-slate-500 hover:text-slate-200",
                )}
              >
                {h.text}
              </a>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
