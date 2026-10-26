#!/usr/bin/env node
// Scaffold a console:  npm run new-app -- <name> [--title "..."] [--label "..."] [--role host|hub|backup]
//                                              [--binary jailopsd] [--prefixes a,b,c]
// Creates web/<name> from web/templates/app, registers it in web/apps.json and package.json, and
// creates the Go embed package from web/templates/go when it does not exist.
import { readFileSync, writeFileSync, mkdirSync, existsSync, readdirSync, statSync, copyFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { webDir, repoDir, registryPath, loadRegistry } from './registry.mjs'

const args = process.argv.slice(2)
const name = args.shift()
const opt = {}
for (let i = 0; i < args.length; i += 2) opt[args[i].replace(/^--/, '')] = args[i + 1]
if (!name || !/^[a-z][a-z0-9-]*$/.test(name)) {
  console.error('usage: npm run new-app -- <name> [--title T] [--label L] [--role host|hub|backup] [--binary B] [--prefixes a,b]')
  process.exit(2)
}
const reg = JSON.parse(readFileSync(registryPath, 'utf8'))
if (reg.apps[name] || existsSync(join(webDir, name))) {
  console.error(`app ${name} already exists`)
  process.exit(1)
}
const role = opt.role ?? 'host'
const label = opt.label ?? `${name} console`
const title = opt.title ?? `JailOps ${label}`
const pkg = `${name.replace(/-/g, '')}ui`
const ports = Object.values(reg.apps).flatMap((a) => [a.devPort, a.previewPort])
const devPort = Math.max(...ports) + 1
const vars = { __APP__: name, __TITLE__: title, __ROLE__: role === 'hub' ? 'host' : role, __LABEL__: label }
const goVars = { '{{PKG}}': pkg, '{{APP}}': name, '{{TITLE}}': title }

function fill(text, map) {
  for (const [k, v] of Object.entries(map)) text = text.split(k).join(v)
  return text
}
function copyTree(from, to, map) {
  mkdirSync(to, { recursive: true })
  for (const f of readdirSync(from)) {
    const src = join(from, f)
    if (statSync(src).isDirectory()) copyTree(src, join(to, f), map)
    else if (/\.(png|ico|woff2?)$/.test(f)) copyFileSync(src, join(to, f))
    else writeFileSync(join(to, f), fill(readFileSync(src, 'utf8'), map))
  }
}

copyTree(join(webDir, 'templates', 'app'), join(webDir, name), vars)

reg.apps[name] = {
  title,
  binary: opt.binary ?? `jailops-${name}d`,
  embedPackage: `internal/${pkg}`,
  dist: `internal/${pkg}/dist`,
  role,
  serverRoles: role === 'backup' ? ['backup'] : ['host'],
  prefixes: ['', 'auth', 'doctor', 'tokens', ...(opt.prefixes ? opt.prefixes.split(',') : [])],
  devPort,
  previewPort: devPort + 1,
  proxy: { env: `JAILOPS_${name.toUpperCase().replace(/-/g, '_')}_API`, default: `http://127.0.0.1:${8100 + (devPort % 100)}` },
}
writeFileSync(registryPath, JSON.stringify(reg, null, 2) + '\n')

const pj = JSON.parse(readFileSync(join(webDir, 'package.json'), 'utf8'))
if (!pj.workspaces.includes(name)) pj.workspaces.push(name)
writeFileSync(join(webDir, 'package.json'), JSON.stringify(pj, null, 2) + '\n')

const goDir = join(repoDir, 'internal', pkg)
if (!existsSync(goDir)) {
  mkdirSync(join(goDir, 'dist'), { recursive: true })
  writeFileSync(join(goDir, 'dist', '.gitkeep'), '')
  const t = join(webDir, 'templates', 'go')
  writeFileSync(join(goDir, 'webui.go'), fill(readFileSync(join(t, 'webui.go.tmpl'), 'utf8'), goVars))
  writeFileSync(join(goDir, 'webui_test.go'), fill(readFileSync(join(t, 'webui_test.go.tmpl'), 'utf8'), goVars))
  writeFileSync(join(goDir, 'embed_test.go'), fill(readFileSync(join(t, 'embed_test.go.tmpl'), 'utf8'), goVars))
}

console.log(`created web/${name} and registered it in web/apps.json.
Next:
  cd web && npm install                       # links the new workspace
  npm run build -- --app ${name}              # output goes to ${reg.apps[name].dist}
  npm run dev -- --app ${name}                # port ${devPort}, proxy ${reg.apps[name].proxy.env}
  npm run test:all
Then: serve internal/${pkg}.Handler() from ${reg.apps[name].binary} and list the real URL prefixes under "prefixes" in web/apps.json.`)
