// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

// 后端端口可通过 VITE_BACKEND_TARGET 覆盖；前端代码只使用相对 URL
//（/api/...、/onebot/...）。
const backendTarget = process.env.VITE_BACKEND_TARGET || "http://127.0.0.1:18080";
const basePath = process.env.VITE_BASE_PATH || "/";

export default defineConfig({
  base: basePath,
  plugins: [vue()],
  server: {
    port: 5174,
    strictPort: false,
    proxy: {
      "/api": backendTarget,
      "/onebot": {
        target: backendTarget,
        ws: true
      }
    }
  },
  build: {
    outDir: "dist",
    emptyOutDir: true
  }
});
