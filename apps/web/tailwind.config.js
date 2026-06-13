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
        "primary-strong": "rgb(var(--primary-strong) / <alpha-value>)",
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
      typography: () => ({
        DEFAULT: {
          css: {
            // Use raw rgb(var(…)) — NOT theme() — because the typography
            // plugin injects values as literal CSS. The Tailwind theme()
            // helper returns `rgb(var(--x) / <alpha-value>)` which only
            // works inside utility classes, not raw CSS properties.
            "--tw-prose-body":          "rgb(var(--slate-300))",
            "--tw-prose-headings":      "rgb(var(--slate-100))",
            "--tw-prose-lead":          "rgb(var(--slate-300))",
            "--tw-prose-links":         "rgb(var(--accent))",
            "--tw-prose-bold":          "rgb(var(--slate-100))",
            "--tw-prose-counters":      "rgb(var(--slate-400))",
            "--tw-prose-bullets":       "rgb(var(--slate-600))",
            "--tw-prose-hr":            "rgb(var(--border))",
            "--tw-prose-quotes":        "rgb(var(--slate-200))",
            "--tw-prose-quote-borders": "rgb(var(--primary))",
            "--tw-prose-captions":      "rgb(var(--slate-500))",
            "--tw-prose-code":          "rgb(var(--inline-code-text))",
            "--tw-prose-pre-code":      "rgb(var(--tok-plain))",
            "--tw-prose-pre-bg":        "rgb(var(--code-bg))",
            "--tw-prose-th-borders":    "rgb(var(--border))",
            "--tw-prose-td-borders":    "rgb(var(--border))",

            maxWidth: "none",
            fontSize: "0.95rem",
            lineHeight: "1.75",

            // ── Headings — tighter, more deliberate scale ──────────
            h1: {
              fontSize: "2.15rem",
              fontWeight: "700",
              letterSpacing: "-0.022em",
              lineHeight: "1.12",
              marginTop: "0",
              marginBottom: "0.55em",
            },
            h2: {
              fontSize: "1.45rem",
              fontWeight: "600",
              letterSpacing: "-0.012em",
              lineHeight: "1.3",
              marginTop: "2.75rem",
              marginBottom: "1rem",
              scrollMarginTop: "6rem",
            },
            h3: {
              fontSize: "1.15rem",
              fontWeight: "600",
              lineHeight: "1.4",
              marginTop: "2rem",
              marginBottom: "0.6rem",
              scrollMarginTop: "6rem",
            },
            h4: {
              fontSize: "1rem",
              fontWeight: "600",
              marginTop: "1.5rem",
              marginBottom: "0.5rem",
              scrollMarginTop: "6rem",
            },

            // ── Lead paragraph under the <h1> ─────────────────────
            '[class~="lead"]': {
              fontSize: "1.2rem",
              lineHeight: "1.6",
              color: "rgb(var(--slate-300))",
              marginTop: "0.4rem",
              marginBottom: "2rem",
            },

            // ── Links — underline only on hover, with offset ──────
            a: {
              fontWeight: "500",
              textDecoration: "none",
              transition: "color .15s ease",
            },
            "a:hover": {
              textDecoration: "underline",
              textUnderlineOffset: "3px",
            },

            // ── Lists ─────────────────────────────────────────────
            "ul > li::marker": { color: "rgb(var(--primary))" },
            "ol > li::marker": { color: "rgb(var(--slate-400))", fontWeight: "600" },
            li: { marginTop: "0.4em", marginBottom: "0.4em" },
            strong: { color: "rgb(var(--slate-100))", fontWeight: "600" },
            hr: { marginTop: "2.5rem", marginBottom: "2.5rem" },

            // ── Blockquote — de-italicized, primary accent rail ───
            blockquote: {
              fontStyle: "normal",
              fontWeight: "400",
              borderLeftWidth: "3px",
              paddingLeft: "1.1em",
            },
            "blockquote p:first-of-type::before": { content: "none" },
            "blockquote p:last-of-type::after": { content: "none" },

            // ── Tables — header fill, generous cell padding ───────
            table: { fontSize: "0.875rem", lineHeight: "1.6" },
            "thead th": {
              backgroundColor: "rgb(var(--surface))",
              color: "rgb(var(--slate-200))",
              fontWeight: "600",
              padding: "0.6em 0.9em",
              verticalAlign: "bottom",
            },
            "tbody td": { padding: "0.55em 0.9em", verticalAlign: "top" },

            // Inline <code> — pill-shaped, high contrast in both themes.
            "code": {
              backgroundColor: "rgb(var(--inline-code-bg))",
              color: "rgb(var(--inline-code-text))",
              padding: "3px 6px",
              borderRadius: "5px",
              border: "1px solid rgb(var(--inline-code-border))",
              fontWeight: "500",
              fontSize: "0.875em",
            },
            // Strip the default backtick quotes around inline <code>.
            "code::before": { content: '""' },
            "code::after": { content: '""' },
            // Don't double-style <pre><code> (already handled by Code.tsx).
            "pre code": {
              backgroundColor: "transparent",
              padding: "0",
              border: "none",
              fontWeight: "normal",
            },
          },
        },
      }),
    },
  },
  plugins: [typography],
};
