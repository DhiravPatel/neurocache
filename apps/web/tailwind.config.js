import typography from "@tailwindcss/typography";

/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        bg: "#0b0d12",
        surface: "#11141c",
        border: "#1f2430",
        primary: "#7c5cff",
        accent: "#22d3ee",
      },
      fontFamily: {
        mono: ["ui-monospace", "SFMono-Regular", "Menlo", "monospace"],
      },
      typography: ({ theme }) => ({
        invert: {
          css: {
            "--tw-prose-body":        theme("colors.slate.300"),
            "--tw-prose-headings":    theme("colors.slate.100"),
            "--tw-prose-lead":        theme("colors.slate.300"),
            "--tw-prose-links":       theme("colors.accent"),
            "--tw-prose-bold":        theme("colors.slate.100"),
            "--tw-prose-counters":    theme("colors.slate.400"),
            "--tw-prose-bullets":     theme("colors.slate.600"),
            "--tw-prose-hr":          theme("colors.border"),
            "--tw-prose-quotes":      theme("colors.slate.200"),
            "--tw-prose-quote-borders": theme("colors.primary"),
            "--tw-prose-captions":    theme("colors.slate.500"),
            "--tw-prose-code":        theme("colors.primary"),
            "--tw-prose-pre-code":    theme("colors.slate.200"),
            "--tw-prose-pre-bg":      "#06080d",
            "--tw-prose-th-borders":  theme("colors.border"),
            "--tw-prose-td-borders":  theme("colors.border"),
          },
        },
      }),
    },
  },
  plugins: [typography],
};
