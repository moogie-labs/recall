import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./tests",
  use: {
    baseURL: "http://127.0.0.1:4175",
    viewport: { width: 1140, height: 760 },
    trace: "retain-on-failure",
  },
  webServer: {
    command: "npm run dev -- --mode test-ui --port 4175 --strictPort",
    url: "http://127.0.0.1:4175/test-app.html",
    reuseExistingServer: false,
  },
  workers: 1,
});
