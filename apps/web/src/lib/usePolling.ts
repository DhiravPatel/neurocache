import { useEffect, useRef, useState } from "react";

export function usePolling<T>(fn: () => Promise<T>, intervalMs = 2000) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<Error | null>(null);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    let timer: ReturnType<typeof setTimeout>;
    const tick = async () => {
      try {
        const d = await fn();
        if (mounted.current) {
          setData(d);
          setError(null);
        }
      } catch (e) {
        if (mounted.current) setError(e as Error);
      } finally {
        if (mounted.current) timer = setTimeout(tick, intervalMs);
      }
    };
    tick();
    return () => {
      mounted.current = false;
      clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [intervalMs]);

  return { data, error };
}
