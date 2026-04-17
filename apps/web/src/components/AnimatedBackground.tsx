/**
 * Decorative animated mesh gradient + grid backdrop. Placed behind hero
 * sections with negative z-index. Pure CSS — no canvas, no WebGL.
 */
export function AnimatedBackground() {
  return (
    <div aria-hidden className="pointer-events-none absolute inset-0 -z-10 overflow-hidden">
      {/* grid */}
      <div className="absolute inset-0 grid-bg opacity-60 dark:opacity-40" />

      {/* mesh blobs */}
      <div
        className="absolute -top-32 left-1/2 h-[520px] w-[520px] -translate-x-1/2 rounded-full
                   blur-3xl opacity-50 dark:opacity-60 animate-blob-1"
        style={{
          background:
            "radial-gradient(circle at center, rgb(var(--primary) / .55), transparent 60%)",
        }}
      />
      <div
        className="absolute right-[-120px] top-32 h-[420px] w-[420px] rounded-full
                   blur-3xl opacity-40 animate-blob-2"
        style={{
          background:
            "radial-gradient(circle at center, rgb(var(--accent) / .55), transparent 60%)",
        }}
      />
      <div
        className="absolute left-[-100px] top-64 h-[380px] w-[380px] rounded-full
                   blur-3xl opacity-30 animate-blob-1"
        style={{
          background:
            "radial-gradient(circle at center, rgb(244 114 182 / .45), transparent 60%)",
          animationDelay: "-8s",
        }}
      />
    </div>
  );
}

/** Marquee row that scrolls left forever. Children should be a small set
 *  of items that get duplicated for the seamless loop. */
export function Marquee({ children }: { children: React.ReactNode }) {
  return (
    <div className="group relative overflow-hidden">
      <div className="flex w-max animate-marquee gap-3 group-hover:[animation-play-state:paused]">
        {children}
        {children}
      </div>
      {/* fade edges */}
      <div className="pointer-events-none absolute inset-y-0 left-0 w-16 bg-gradient-to-r from-surface to-transparent" />
      <div className="pointer-events-none absolute inset-y-0 right-0 w-16 bg-gradient-to-l from-surface to-transparent" />
    </div>
  );
}
