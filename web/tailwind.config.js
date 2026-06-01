/** @type {import('tailwindcss').Config} */
export default {
  darkMode: "class",
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        // Enterprise slate palette with a docker-blue accent.
        bg: "#0b0f17",
        panel: "#121826",
        panel2: "#1a2233",
        border: "#243047",
        muted: "#8b97ad",
        text: "#e6ebf4",
        accent: "#2496ed",
        accent2: "#1d7fd1",
        ok: "#2dd4a7",
        warn: "#f5b14c",
        danger: "#f0616d",
      },
      fontFamily: {
        sans: ["Inter", "system-ui", "Segoe UI", "Roboto", "sans-serif"],
        mono: ["JetBrains Mono", "ui-monospace", "SFMono-Regular", "Menlo", "monospace"],
      },
    },
  },
  plugins: [],
};
