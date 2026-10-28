import { defineConfig, devices } from '@playwright/test'
import { e2eConfig } from '../shared/vite.app.ts'

// The specs run against the built console in demo mode (`?demo`), so no server is needed.
// Build first: `npm run build -- --app host`.
const { port } = e2eConfig('host')
export default defineConfig({
  testDir: 'e2e',
  outputDir: 'e2e/.results',
  fullyParallel: true,
  workers: process.env.CI ? 4 : undefined, // a hosted runner has 4 cores; the default is half of them
  retries: 0,
  reporter: [['list']],
  use: { baseURL: `http://127.0.0.1:${port}`, testIdAttribute: 'data-test', trace: 'retain-on-failure' },
  webServer: { command: `npx vite preview --host 127.0.0.1`, url: `http://127.0.0.1:${port}`, reuseExistingServer: true },
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 800 } } },
    { name: 'phone', use: { ...devices['Pixel 5'] } },
  ],
})
