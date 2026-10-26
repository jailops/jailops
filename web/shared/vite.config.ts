import { join } from 'node:path'
import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

// Shared code has no build of its own: apps compile it. This config only runs its unit tests.
export default defineConfig({
  root: import.meta.dirname,
  plugins: [vue()],
  resolve: { alias: { '@jailops/shared': join(import.meta.dirname, 'src') } },
  test: { name: 'shared', environment: 'jsdom', include: ['src/**/*.test.ts'] },
})
