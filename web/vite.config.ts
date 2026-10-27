import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// The Go server listens here by default; override when it runs elsewhere:
//   LCM_DEV_API=http://127.0.0.1:9000 npm run dev
const api = process.env["LCM_DEV_API"] ?? "http://127.0.0.1:7800";

export default defineConfig({
  plugins: [react()],
  build: {
    // The Go binary embeds this directory (internal/webui).
    outDir: "../internal/webui/dist",
    emptyOutDir: true,
    target: "es2023",
    sourcemap: false,
    chunkSizeWarningLimit: 700,
  },
  server: {
    proxy: {
      "/api": { target: api, changeOrigin: false },
    },
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}"],
    setupFiles: ["src/test-setup.ts"],
  },
});
