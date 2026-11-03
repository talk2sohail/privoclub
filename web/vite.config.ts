import { defineConfig } from "vitest/config";

// The admin app (ADR 0019). The build goes to dist/, which is committed and
// embedded in the Go binary; Go serves the assets under /admin/app/.
export default defineConfig({
  base: "/admin/app/",
  oxc: { jsx: { runtime: "automatic", importSource: "preact" } },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    target: "es2022",
    modulePreload: { polyfill: false },
    reportCompressedSize: false,
    sourcemap: false,
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}"],
    setupFiles: ["src/test/setup.ts"],
  },
});
