import { defineConfig } from 'vitest/config'

// Architecture tests: they read source files and web/apps.json, so no DOM is needed.
export default defineConfig({ root: import.meta.dirname, test: { name: 'tests', environment: 'node', include: ['*.test.ts'] } })
