// Reads web/apps.json: the single source of truth for every console.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join, resolve } from 'node:path'

export const webDir = resolve(dirname(fileURLToPath(import.meta.url)), '..')
export const repoDir = resolve(webDir, '..')
export const registryPath = join(webDir, 'apps.json')

export function loadRegistry() {
  return JSON.parse(readFileSync(registryPath, 'utf8')).apps
}

export function loadApp(name) {
  const app = loadRegistry()[name]
  if (!app) throw new Error(`unknown app "${name}"; registered: ${Object.keys(loadRegistry()).join(', ')}`)
  return { name, ...app, dir: join(webDir, name), distAbs: join(repoDir, app.dist) }
}
