import clsx from "clsx";
import { useInView } from "../lib/useInView";

/**
 * Wraps children in a fade-up reveal that triggers when scrolled into view.
 * Stagger delays via the `delay` prop (in ms).
 */
export function Reveal({
  children,
  delay = 0,
  className,
  as = "div",
}: {
  children: React.ReactNode;
  delay?: number;
  className?: string;
  as?: keyof JSX.IntrinsicElements;
}) {
  const [ref, inView] = useInView<HTMLElement>();
  const Tag = as as any;
  return (
    <Tag
      ref={ref as any}
      style={{ transitionDelay: `${delay}ms` }}
      className={clsx("reveal", inView && "reveal-on", className)}
    >
      {children}
    </Tag>
  );
}
