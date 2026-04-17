import { Moon, Sun } from "lucide-react";
import clsx from "clsx";
import { useTheme } from "../lib/theme";

export function ThemeToggle({ className }: { className?: string }) {
  const { resolved, toggle } = useTheme();
  return (
    <button
      onClick={toggle}
      aria-label={`Switch to ${resolved === "dark" ? "light" : "dark"} theme`}
      title={`Switch to ${resolved === "dark" ? "light" : "dark"} theme`}
      className={clsx(
        "relative inline-flex h-8 w-8 items-center justify-center rounded-md",
        "border border-border bg-bg/50 text-slate-300 transition-colors",
        "hover:bg-slate-100/40 dark:hover:bg-white/10",
        className,
      )}
    >
      <Sun
        size={15}
        className={clsx(
          "absolute transition-all duration-300",
          resolved === "dark" ? "scale-0 -rotate-90 opacity-0" : "scale-100 rotate-0 opacity-100",
        )}
      />
      <Moon
        size={15}
        className={clsx(
          "absolute transition-all duration-300",
          resolved === "dark" ? "scale-100 rotate-0 opacity-100" : "scale-0 rotate-90 opacity-0",
        )}
      />
    </button>
  );
}
