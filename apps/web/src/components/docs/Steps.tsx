import { Children, isValidElement } from "react";

/**
 * Vertical numbered step list for tutorials (Quick Start, Installation).
 * Numbers are derived from child order — just nest <Step> elements.
 *
 *   <Steps>
 *     <Step title="Start the engine"> … </Step>
 *     <Step title="Set a key"> … </Step>
 *   </Steps>
 */
export function Steps({ children }: { children: React.ReactNode }) {
  const steps = Children.toArray(children).filter(isValidElement);
  return (
    <div className="not-prose my-6">
      <ol className="m-0 list-none space-y-0 p-0">
        {steps.map((child, i) => (
          <li key={i} className="relative flex gap-4 pb-7 last:pb-0">
            {/* connecting rail */}
            {i < steps.length - 1 && (
              <span
                aria-hidden
                className="absolute left-[15px] top-9 bottom-0 w-px bg-gradient-to-b from-border to-transparent"
              />
            )}
            <span className="relative z-10 flex h-8 w-8 shrink-0 items-center justify-center rounded-full border border-primary/30 bg-primary/10 text-[13px] font-semibold text-primary shadow-[0_0_0_4px_rgb(var(--bg))]">
              {i + 1}
            </span>
            <div className="min-w-0 flex-1 pt-0.5">{child}</div>
          </li>
        ))}
      </ol>
    </div>
  );
}

export function Step({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <>
      <h3 className="m-0 mb-2 text-[15px] font-semibold leading-snug text-slate-100">
        {title}
      </h3>
      <div className="text-sm leading-relaxed text-slate-300 [&_a]:font-medium [&_a]:text-accent [&_a:hover]:underline [&>p]:mt-0 [&>p]:mb-3 [&>p:last-child]:mb-0">
        {children}
      </div>
    </>
  );
}
