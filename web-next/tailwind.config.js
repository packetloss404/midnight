/** @type {import('tailwindcss').Config} */
// Tailwind theme that mirrors the OKLch design tokens defined in
// `src/styles/tokens.css`. The CSS variables are the canonical source of
// truth; this config exposes them to Tailwind utilities so we can use
// `text-text`, `bg-surface`, `border-line`, etc. directly.
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        bg: "var(--bg)",
        "bg-2": "var(--bg-2)",
        surface: "var(--surface)",
        "surface-2": "var(--surface-2)",
        "surface-3": "var(--surface-3)",
        "surface-raised": "var(--surface-raised)",
        line: "var(--line)",
        "line-soft": "var(--line-soft)",
        "line-strong": "var(--line-strong)",
        text: "var(--text)",
        muted: "var(--muted)",
        subtle: "var(--subtle)",
        accent: "var(--accent)",
        "accent-soft": "var(--accent-soft)",
        "accent-ink": "var(--accent-ink)",
        warn: "var(--warn)",
        "warn-soft": "var(--warn-soft)",
        info: "var(--info)",
        "info-soft": "var(--info-soft)",
        branch: "var(--branch)",
        "branch-soft": "var(--branch-soft)",
        danger: "var(--danger)",
        "danger-soft": "var(--danger-soft)",
        staged: "var(--staged)",
        "staged-soft": "var(--staged-soft)",
        "chart-1": "var(--chart-1)",
        "chart-2": "var(--chart-2)",
        "chart-3": "var(--chart-3)",
        "chart-4": "var(--chart-4)",
      },
      spacing: {
        rail: "var(--rail-width)",
      },
      borderRadius: {
        sm: "var(--r-sm)",
        DEFAULT: "var(--r)",
        lg: "var(--r-lg)",
      },
      fontFamily: {
        ui: "var(--font-ui)",
        mono: "var(--font-mono)",
      },
      boxShadow: {
        sm: "var(--shadow-sm)",
        DEFAULT: "var(--shadow)",
        lg: "var(--shadow-lg)",
      },
    },
  },
  plugins: [],
};
