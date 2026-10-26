#!/usr/bin/env node
// Generic tooling, driven by web/apps.json only.
//   node scripts/app.mjs <build|dev|typecheck|e2e> (--app <name> | --all) [-- extra args]
import { spawnSync } from 'node:child_process'
import { join } from 'node:path'
import { loadRegistry, loadApp, webDir } from './registry.mjs'

const [cmd, ...rest] = process.argv.slice(2)
const flags = { app: '', all: false, extra: [] }
for (let i = 0; i < rest.length; i++) {
  if (rest[i] === '--app') flags.app = rest[++i] ?? ''
  else if (rest[i] === '--all') flags.all = true
  else flags.extra.push(rest[i])
}
const names = flags.all ? Object.keys(loadRegistry()) : flags.app ? [flags.app] : []
if (!cmd || !names.length) {
  console.error(`usage: app.mjs <build|dev|typecheck|e2e> --app <name> | --all\nregistered apps: ${Object.keys(loadRegistry()).join(', ')}`)
  process.exit(2)
}

function run(bin, args, cwd) {
  const r = spawnSync(join(webDir, 'node_modules', '.bin', bin), args, { cwd, stdio: 'inherit', env: process.env })
  if (r.status !== 0) process.exit(r.status ?? 1)
}

for (const n of names) {
  const app = loadApp(n)
  console.log(`\n== ${cmd} ${n} (${app.title}) ==`)
  switch (cmd) {
    case 'build': run('vite', ['build', ...flags.extra], app.dir); break
    case 'dev': run('vite', ['--port', String(app.devPort), ...flags.extra], app.dir); break
    case 'typecheck': run('vue-tsc', ['--noEmit', '-p', 'tsconfig.app.json'], app.dir); break
    case 'e2e': run('playwright', ['test', ...flags.extra], app.dir); break
    default: console.error(`unknown command ${cmd}`); process.exit(2)
  }
}
