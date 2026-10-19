import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In development, API calls go to the Go server (`go run ./cmd/api`), so the page and the API share an
// origin and no CORS is needed.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/search": "http://localhost:8080",
    },
  },
});
