import { defineConfig } from 'vitest/config'
import { readFileSync } from 'node:fs'

// One project per registered app, plus shared and the architecture tests.
const apps = Object.keys(JSON.parse(readFileSync(new URL('./apps.json', import.meta.url), 'utf8')).apps)
export default defineConfig({ test: { projects: [...apps.map((a) => `./${a}`), './shared', './tests'] } })
