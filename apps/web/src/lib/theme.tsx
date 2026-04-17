import { createContext, useCallback, useContext, useEffect, useState } from "react";

type Mode = "light" | "dark" | "system";
type Resolved = "light" | "dark";

type Ctx = {
  mode: Mode;
  resolved: Resolved;
  setMode: (m: Mode) => void;
  toggle: () => void;
};

const ThemeContext = createContext<Ctx | null>(null);
const STORAGE_KEY = "neurocache-theme";

function systemPrefers(): Resolved {
  if (typeof window === "undefined") return "dark";
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function readStored(): Mode {
  if (typeof window === "undefined") return "system";
  const v = window.localStorage.getItem(STORAGE_KEY);
  return v === "light" || v === "dark" || v === "system" ? v : "system";
}

function applyClass(resolved: Resolved) {
  const root = document.documentElement;
  root.classList.toggle("dark", resolved === "dark");
}

export function ThemeProvider({ children }: { children: React.ReactNode }) {
  const [mode, setModeState] = useState<Mode>(() => readStored());
  const [resolved, setResolved] = useState<Resolved>(() =>
    readStored() === "system" ? systemPrefers() : (readStored() as Resolved),
  );

  const setMode = useCallback((m: Mode) => {
    setModeState(m);
    try {
      if (m === "system") window.localStorage.removeItem(STORAGE_KEY);
      else window.localStorage.setItem(STORAGE_KEY, m);
    } catch {
      /* ignore */
    }
    const next: Resolved = m === "system" ? systemPrefers() : m;
    setResolved(next);
    applyClass(next);
  }, []);

  const toggle = useCallback(() => {
    setMode(resolved === "dark" ? "light" : "dark");
  }, [resolved, setMode]);

  // React to system preference changes when in `system` mode.
  useEffect(() => {
    if (mode !== "system" || typeof window === "undefined") return;
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = () => {
      const next: Resolved = mq.matches ? "dark" : "light";
      setResolved(next);
      applyClass(next);
    };
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, [mode]);

  // Sync once on mount in case the pre-paint script set the wrong class.
  useEffect(() => {
    applyClass(resolved);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <ThemeContext.Provider value={{ mode, resolved, setMode, toggle }}>
      {children}
    </ThemeContext.Provider>
  );
}

export function useTheme(): Ctx {
  const ctx = useContext(ThemeContext);
  if (!ctx) throw new Error("useTheme must be used inside <ThemeProvider>");
  return ctx;
}
