import typography from "@tailwindcss/typography";

/** @type {import('tailwindcss').Config} */
export default {
  darkMode: "class",
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        // Semantic tokens — driven by CSS variables defined in index.css.
        // The same Tailwind class (e.g. `bg-bg`) flips between light/dark
        // automatically when the `dark` class is added/removed on <html>.
        bg:      "rgb(var(--bg)      / <alpha-value>)",
        surface: "rgb(var(--surface) / <alpha-value>)",
        border:  "rgb(var(--border)  / <alpha-value>)",
        primary: "rgb(var(--primary) / <alpha-value>)",
        accent:  "rgb(var(--accent)  / <alpha-value>)",
        code: {
          DEFAULT: "rgb(var(--code-bg) / <alpha-value>)",
          border:  "rgb(var(--code-border) / <alpha-value>)",
          plain:   "rgb(var(--tok-plain) / <alpha-value>)",
        },
        // Slate scale is overridden so that existing `text-slate-400` etc.
        // stays semantic across themes (in light mode the values are darker
        // greys, so the visual hierarchy is preserved).
        slate: {
          100: "rgb(var(--slate-100) / <alpha-value>)",
          200: "rgb(var(--slate-200) / <alpha-value>)",
          300: "rgb(var(--slate-300) / <alpha-value>)",
          400: "rgb(var(--slate-400) / <alpha-value>)",
          500: "rgb(var(--slate-500) / <alpha-value>)",
          600: "rgb(var(--slate-600) / <alpha-value>)",
          700: "rgb(var(--slate-700) / <alpha-value>)",
        },
      },
      fontFamily: {
        mono: ["ui-monospace", "SFMono-Regular", "Menlo", "monospace"],
      },
      keyframes: {
        "fade-up": {
          "0%":   { opacity: "0", transform: "translateY(12px)" },
          "100%": { opacity: "1", transform: "translateY(0)" },
        },
        "fade-in": {
          "0%": { opacity: "0" }, "100%": { opacity: "1" },
        },
        "blob-1": {
          "0%, 100%": { transform: "translate(0, 0) scale(1)" },
          "33%":      { transform: "translate(40px, -30px) scale(1.1)" },
          "66%":      { transform: "translate(-30px, 30px) scale(0.95)" },
        },
        "blob-2": {
          "0%, 100%": { transform: "translate(0, 0) scale(1)" },
          "33%":      { transform: "translate(-50px, 20px) scale(1.05)" },
          "66%":      { transform: "translate(30px, -40px) scale(0.9)" },
        },
        "marquee": {
          "0%":   { transform: "translateX(0)" },
          "100%": { transform: "translateX(-50%)" },
        },
        "glow": {
          "0%, 100%": { opacity: "0.4" },
          "50%":      { opacity: "0.8" },
        },
        "pulse-ring": {
          "0%":   { transform: "scale(.8)", opacity: "0.7" },
          "100%": { transform: "scale(2)",  opacity: "0"   },
        },
        "shimmer": {
          "0%":   { backgroundPosition: "-200% 0" },
          "100%": { backgroundPosition: "200% 0" },
        },
      },
      animation: {
        "fade-up":    "fade-up 0.6s cubic-bezier(0.16,1,0.3,1) both",
        "fade-in":    "fade-in 0.5s ease-out both",
        "blob-1":     "blob-1 22s ease-in-out infinite",
        "blob-2":     "blob-2 26s ease-in-out infinite",
        "marquee":    "marquee 40s linear infinite",
        "glow":       "glow 4s ease-in-out infinite",
        "pulse-ring": "pulse-ring 2s cubic-bezier(0.4,0,0.6,1) infinite",
        "shimmer":    "shimmer 2.5s linear infinite",
      },
      typography: ({ theme }) => ({
        DEFAULT: {
          css: {
            "--tw-prose-body":          theme("colors.slate.300"),
            "--tw-prose-headings":      theme("colors.slate.100"),
            "--tw-prose-lead":          theme("colors.slate.300"),
            "--tw-prose-links":         theme("colors.accent"),
            "--tw-prose-bold":          theme("colors.slate.100"),
            "--tw-prose-counters":      theme("colors.slate.400"),
            "--tw-prose-bullets":       theme("colors.slate.600"),
            "--tw-prose-hr":            theme("colors.border"),
            "--tw-prose-quotes":        theme("colors.slate.200"),
            "--tw-prose-quote-borders": theme("colors.primary"),
            "--tw-prose-captions":      theme("colors.slate.500"),
            "--tw-prose-code":          theme("colors.primary"),
            "--tw-prose-pre-code":      theme("colors.slate.200"),
            "--tw-prose-pre-bg":        "rgb(var(--code-bg))",
            "--tw-prose-th-borders":    theme("colors.border"),
            "--tw-prose-td-borders":    theme("colors.border"),
          },
        },
      }),
    },
  },
  plugins: [typography],
};
