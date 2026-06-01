import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// During development the Go API runs on :8080 (start it with `-dev`). Vite
// proxies REST and WebSocket traffic there so the SPA can use same-origin paths
// in both dev and the embedded production build.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8080",
        changeOrigin: true,
        ws: true,
      },
    },
  },
});
