import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// During development the Go API runs on :8470 (start it with `-dev`). Vite
// proxies REST and WebSocket traffic there so the SPA can use same-origin paths
// in both dev and the embedded production build.
export default defineConfig({
  plugins: [react()],
  // Relative asset URLs: the server writes a <base href> naming the path
  // prefix the UI is served under (DC_BASE_PATH / X-Forwarded-Prefix), and
  // the assets resolve against it at any route depth.
  base: "./",
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8470",
        changeOrigin: true,
        ws: true,
      },
    },
  },
});
