import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Build output lands in ../web, which the Go binary embeds via //go:embed web.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  build: {
    outDir: "../web",
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    // Dev proxy so `npm run dev` can talk to the Go backend on :8787.
    proxy: {
      "/api": "http://127.0.0.1:8787",
    },
  },
});
