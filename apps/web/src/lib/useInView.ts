import { useEffect, useRef, useState } from "react";

/**
 * Returns a [ref, inView] pair. `inView` flips to true once the element
 * crosses into the viewport. Used for fade-in-on-scroll reveals.
 */
export function useInView<T extends HTMLElement>(opts: IntersectionObserverInit = {}) {
  const ref = useRef<T | null>(null);
  const [inView, setInView] = useState(false);

  useEffect(() => {
    const el = ref.current;
    if (!el || inView) return;
    const io = new IntersectionObserver(
      ([entry]) => {
        if (entry.isIntersecting) {
          setInView(true);
          io.disconnect();
        }
      },
      { rootMargin: "-10% 0px", threshold: 0.1, ...opts },
    );
    io.observe(el);
    return () => io.disconnect();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return [ref, inView] as const;
}
