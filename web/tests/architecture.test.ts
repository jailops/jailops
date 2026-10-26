// Architecture rules, enforced by reading the sources and web/apps.json (the registry).
// If one fails, the fix is in the code, not in the test.
import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs'
import { join, relative } from 'node:path'

const web = join(import.meta.dirname, '..')
const repo = join(web, '..')
interface App { title: string; binary: string; embedPackage: string; dist: string; role: string; serverRoles: string[]; prefixes: string[]; devPort: number; previewPort: number }
const apps = JSON.parse(readFileSync(join(web, 'apps.json'), 'utf8')).apps as Record<string, App>
const names = Object.keys(apps)

function walk(dir: string, out: string[] = []): string[] {
  if (!existsSync(dir)) return out
  for (const f of readdirSync(dir)) {
    if (f === 'node_modules' || f === '.results') continue
    const p = join(dir, f)
    if (statSync(p).isDirectory()) walk(p, out)
    else out.push(p)
  }
  return out
}
const isSrc = (f: string) => /\.(ts|vue|mjs)$/.test(f)
const isTest = (f: string) => /\.test\.ts$|\.spec\.ts$/.test(f)
const read = (f: string) => readFileSync(f, 'utf8')

/** API path literals in a call position: foo('/jails'), foo(`/jails/${x}`), get('') is the root. */
function apiPaths(text: string): string[] {
  return [...text.matchAll(/\(\s*[`'"]\/([a-z][a-z0-9-]*)/g)].map((m) => m[1])
}

// Segments the shared client and core may use, and the UI route that is not an API path.
const CORE = ['auth']
const SHARED_EXTRA = ['events', 'operations', 'login']

describe('registry', () => {
  it('is well formed', () => {
    expect(names.length).toBeGreaterThan(0)
    const ports = Object.values(apps).flatMap((a) => [a.devPort, a.previewPort])
    expect(new Set(ports).size, 'ports must be unique').toBe(ports.length)
    expect(new Set(Object.values(apps).map((a) => a.dist)).size, 'dist directories must be unique').toBe(names.length)
    expect(new Set(Object.values(apps).map((a) => a.title)).size, 'titles must be unique').toBe(names.length)
    expect(new Set(Object.values(apps).map((a) => a.binary)).size, 'binaries must be unique').toBe(names.length)
    for (const [n, a] of Object.entries(apps)) {
      expect(['host', 'hub', 'backup'], `${n}.role`).toContain(a.role)
      expect(a.embedPackage, `${n}.embedPackage`).toMatch(/^internal\/[a-z0-9]+$/)
      expect(a.dist, `${n}.dist`).toBe(`${a.embedPackage}/dist`)
      expect(existsSync(join(repo, a.dist, '.gitkeep')), `${a.dist}/.gitkeep must be tracked so go:embed compiles`).toBe(true)
      expect(existsSync(join(web, n)), `web/${n}`).toBe(true)
      expect(a.prefixes, `${n}.prefixes`).toContain('')
    }
  })
})

describe('app boundaries', () => {
  for (const n of names) {
    it(`${n} imports only itself and shared`, () => {
      const others = names.filter((o) => o !== n)
      for (const f of walk(join(web, n)).filter(isSrc)) {
        const text = read(f)
        for (const m of text.matchAll(/(?:from|import)\s*\(?\s*['"]([^'"]+)['"]/g)) {
          const spec = m[1]
          for (const o of others) {
            expect(spec.startsWith(`@jailops/${o}`), `${relative(web, f)} imports app ${o}`).toBe(false)
            expect(new RegExp(`(^|/)\\.\\./${o}(/|$)`).test(spec), `${relative(web, f)} imports app ${o} by path`).toBe(false)
          }
        }
      }
    })

    it(`${n}: its API module only calls registered prefixes`, () => {
      const allow = new Set(apps[n].prefixes.filter(Boolean))
      const files = walk(join(web, n, 'src', 'api')).filter((f) => isSrc(f) && !isTest(f))
      expect(files.length, `web/${n}/src/api must hold the endpoints module`).toBeGreaterThan(0)
      let usesCore = false
      for (const f of files) {
        const text = read(f)
        if (/api\/core/.test(text)) usesCore = true
        for (const seg of apiPaths(text)) expect(allow.has(seg), `${relative(web, f)}: path /${seg} is not in the "${n}" prefixes of web/apps.json`).toBe(true)
      }
      if (usesCore) for (const c of CORE) expect(allow.has(c), `${n} uses shared core: prefix "${c}" must be registered`).toBe(true)
    })

    it(`${n}: network access stays in its API module`, () => {
      for (const f of walk(join(web, n, 'src')).filter((x) => isSrc(x) && !isTest(x))) {
        const rel = relative(join(web, n, 'src'), f)
        if (rel.startsWith('api/') || rel.startsWith('demo/')) continue
        expect(/\bfetch\(|new EventSource|XMLHttpRequest|\bgetClient\(/.test(read(f)), `${rel} talks to the network outside src/api`).toBe(false)
        expect(apiPaths(read(f)).filter((p) => !['login'].includes(p) && !apps[n].prefixes.includes(p)), `${rel} has API-looking paths`).toEqual([])
      }
    })

    it(`${n}: index.html names its own app and role`, () => {
      const html = read(join(web, n, 'index.html'))
      expect(html).toContain(`<title>${apps[n].title}</title>`)
      expect(html).toContain(`<meta name="jailops-app" content="${n}"`)
      expect(html).toContain(`<meta name="jailops-role" content="${apps[n].serverRoles[0]}"`)
      expect(html, 'a visible header before the app mounts').toContain(`<div id="app"><p`)
      expect(html.match(new RegExp(apps[n].title, 'g'))!.length).toBeGreaterThanOrEqual(2)
    })

    it(`${n}: vite config builds only to its registered dist`, () => {
      const cfg = read(join(web, n, 'vite.config.ts'))
      expect(cfg).toMatch(new RegExp(`appConfig\\('${n}',\\s*import\\.meta\\.dirname\\)`))
      expect(cfg, 'outDir comes from the registry, never from the app').not.toMatch(/outDir/)
    })
  }
})

describe('shared code', () => {
  const shared = join(web, 'shared', 'src')
  const files = walk(shared).filter((f) => isSrc(f) && !isTest(f) && !f.endsWith('types.gen.ts'))
  it('has no feature views', () => {
    expect(existsSync(join(shared, 'views'))).toBe(false)
    expect(files.filter((f) => /View\.vue$/.test(f))).toEqual([])
  })
  it('imports no app', () => {
    for (const f of walk(join(web, 'shared')).filter(isSrc)) {
      for (const m of read(f).matchAll(/(?:from|import)\s*\(?\s*['"]([^'"]+)['"]/g)) {
        for (const o of names) expect(m[1].includes(`/${o}/`) || m[1].startsWith(`@jailops/${o}`), `${relative(web, f)} imports app ${o}`).toBe(false)
      }
    }
  })
  it('contains no app-specific endpoints', () => {
    const allow = new Set([...CORE, ...SHARED_EXTRA])
    for (const f of files) for (const seg of apiPaths(read(f))) expect(allow.has(seg), `${relative(web, f)}: /${seg} belongs in an app's API module`).toBe(true)
  })
})

describe('build outputs', () => {
  it('every dist directory is written only by its app', () => {
    // vite.app.ts is the only place that sets outDir, and it reads it from the registry.
    const shared = read(join(web, 'shared', 'vite.app.ts'))
    expect(shared.match(/outDir/g)!.length).toBeGreaterThan(0)
    expect(shared).toContain('e.dist')
    for (const f of walk(web).filter((x) => /vite\.config\.ts$/.test(x) && !x.includes('shared') && !x.includes('tests'))) expect(read(f)).not.toMatch(/outDir/)
  })
})
