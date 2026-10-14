import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["test/**/*.test.ts"],
    // One HomeCloud per Docker host: never run test files in parallel.
    fileParallelism: false,
    testTimeout: 60_000,
    hookTimeout: 300_000,
  },
});
