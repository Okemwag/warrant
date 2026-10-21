import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In dev, the Console runs on :5173 and proxies the API on :8080.
// In production the Go server serves console/dist itself.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/v1": "http://localhost:8080",
      "/paypal": "http://localhost:8080",
      "/healthz": "http://localhost:8080",
    },
  },
  build: { outDir: "dist", sourcemap: false },
});
