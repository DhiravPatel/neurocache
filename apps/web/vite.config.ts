import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In dev, Vite serves the UI on :5173 and the Go engine runs on :8080.
// We proxy /api/* to the engine so relative fetches in the app work the
// same as in prod (where the engine serves the dashboard itself on :8080).
const API_TARGET =
  (typeof process !== "undefined" && process.env?.VITE_API_URL) ||
  "http://localhost:8080";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/api": {
        target: API_TARGET,
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: "dist",
    sourcemap: true,
  },
});
