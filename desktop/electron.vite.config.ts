// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { resolve } from 'node:path'
import { defineConfig } from 'electron-vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  main: { build: { outDir: 'out/main', rollupOptions: { input: resolve('src/main/index.ts') } } },
  preload: {
    // 预加载脚本跑在沙箱里，只能是 CommonJS。
    build: { outDir: 'out/preload', rollupOptions: { input: resolve('src/preload/index.ts'), output: { format: 'cjs', entryFileNames: '[name].cjs' } } },
  },
  renderer: {
    root: 'src/renderer',
    build: { outDir: 'out/renderer', rollupOptions: { input: resolve('src/renderer/index.html') } },
    plugins: [vue()],
  },
})
