import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  timeout: 60_000,
  workers: 1,
  use: {
    baseURL: process.env.CONTROL_PLANE_API_URL ?? "http://127.0.0.1:8000",
    trace: "retain-on-failure",
  },
});
