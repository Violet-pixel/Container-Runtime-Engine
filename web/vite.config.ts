import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: { outDir: "../internal/ui/dist", emptyOutDir: true },
  server: {
    port: 5173,
    proxy: {
      "/api": { target: "http://127.0.0.1:7070", ws: true },
      "/healthz": "http://127.0.0.1:7070",
    },
  },
});
