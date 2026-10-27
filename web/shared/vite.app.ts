// The one Vite configuration every console uses. An app's vite.config.ts is a single call:
//   export default appConfig('host', import.meta.dirname)
// Everything (output directory, dev proxy, port) comes from web/apps.json.
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

interface AppEntry { dist: string; devPort: number; previewPort: number; proxy: { env: string; default: string } }

export function loadEntry(name: string): AppEntry & { repo: string } {
  const repo = resolve(import.meta.dirname, '..', '..')
  const reg = JSON.parse(readFileSync(join(repo, 'web', 'apps.json'), 'utf8')).apps as Record<string, AppEntry>
  if (!reg[name]) throw new Error(`app ${name} is not in web/apps.json`)
  return { ...reg[name], repo }
}

export function appConfig(name: string, appDir: string) {
  const e = loadEntry(name)
  const outDir = join(e.repo, e.dist)
  const target = process.env[e.proxy.env] || e.proxy.default
  // emptyOutDir removes the tracked placeholder that keeps go:embed compiling before the first build.
  const keepPlaceholder = () => ({
    name: 'keep-gitkeep',
    closeBundle() {
      mkdirSync(outDir, { recursive: true })
      writeFileSync(join(outDir, '.gitkeep'), '')
    },
  })
  return defineConfig({
    root: appDir,
    plugins: [vue(), keepPlaceholder()],
    resolve: {
      alias: {
        '@jailops/shared': join(appDir, '..', 'shared', 'src'),
        '@': join(appDir, 'src'),
      },
    },
    server: {
      port: e.devPort,
      proxy: {
        '/api/v1': {
          target,
          secure: false, // a development server often has a self-signed certificate
          // The daemon rejects a state-changing request whose Origin host differs from its Host
          // (CSRF defence). The browser sends the dev server's origin, so rewrite it to the target.
          changeOrigin: true,
          configure: (proxy) => {
            proxy.on('proxyReq', (req) => {
              if (req.getHeader('origin')) req.setHeader('origin', target)
            })
          },
        },
      },
    },
    preview: { port: e.previewPort, strictPort: true },
    build: { outDir, emptyOutDir: true, target: 'es2022', assetsInlineLimit: 0, chunkSizeWarningLimit: 400 },
    test: { name, environment: 'jsdom', include: ['src/**/*.test.ts'] },
  })
}

export function e2eConfig(name: string) {
  return { port: loadEntry(name).previewPort }
}
