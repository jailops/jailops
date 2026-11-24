// Canned jail logs for `?demo`: the files below /var/log of each demo jail. Logs get timestamps relative to now and grow
// while they are followed.
import type { JailLogFile, JailLogRead } from '@jailops/shared/api/types'

interface Line { text: string; ts: number }
/** A read of the demo with the time each line was written. */
export type DemoRead = Omit<JailLogRead, 'lines'> & { entries: Array<{ text: string; ts: number; n?: number }> }
interface File { lines: Line[]; size: number; grows: boolean; last: number; rotations: number; next: () => string }

const MON = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
const two = (n: number) => String(n).padStart(2, '0')
const sys = (t: number) => { const d = new Date(t); return `${MON[d.getMonth()]} ${String(d.getDate()).padStart(2, ' ')} ${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}` }
const web = (t: number) => { const d = new Date(t); return `${two(d.getUTCDate())}/${MON[d.getUTCMonth()]}/${d.getUTCFullYear()}:${two(d.getUTCHours())}:${two(d.getUTCMinutes())}:${two(d.getUTCSeconds())} +0000` }

const CLIENTS = ['192.0.2.41', '198.51.100.23', '203.0.113.9', '10.20.0.12', '2001:db8:20:1::77']
const PAGES: Array<[string, number, number]> = [['/', 200, 615], ['/index.html', 200, 615], ['/assets/app.css', 200, 18342], ['/assets/app.js', 200, 90211], ['/favicon.ico', 404, 153], ['/api/health', 200, 2], ['/login', 302, 0], ['/robots.txt', 200, 26], ['/.env', 404, 153]]
const AGENTS = ['Mozilla/5.0 (X11; FreeBSD amd64; rv:140.0) Gecko/20100101 Firefox/140.0', 'curl/8.16.0', 'Prometheus/3.15.0', 'Mozilla/5.0 (compatible; ExampleBot/2.1)']

function pick<T>(xs: T[], i: number): T { return xs[i % xs.length] }

function generator(kind: string, jail: string): (t: number, i: number) => string {
  const host = jail
  switch (kind) {
    case 'access':
      return (t, i) => { const [p, st, sz] = pick(PAGES, i * 7 + 3); return `${pick(CLIENTS, i * 3 + 1)} - - [${web(t)}] "GET ${p} HTTP/1.1" ${st} ${sz} "-" "${pick(AGENTS, i)}"` }
    case 'error':
      return (t, i) => { const d = new Date(t); return `${d.getFullYear()}/${two(d.getMonth() + 1)}/${two(d.getDate())} ${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())} [${i % 5 ? 'error' : 'warn'}] 4812#100231: *${900 + i} open() "/usr/local/www/nginx/${i % 2 ? 'favicon.ico' : '.env'}" failed (2: No such file or directory), client: ${pick(CLIENTS, i)}, server: localhost` }
    case 'auth':
      return (t, i) => `${sys(t)} ${host} sshd[${3100 + i}]: ${i % 3 ? `Accepted publickey for deploy from 203.0.113.5 port ${51000 + i} ssh2: ED25519 SHA256:q2Vd7nXbH1` : `Invalid user admin from 198.51.100.66 port ${40000 + i}`}`
    case 'cron':
      return (t, i) => `${sys(t)} ${host} /usr/sbin/cron[${5000 + i}]: (root) CMD (/usr/libexec/atrun)`
    case 'daemon':
      // One line carries terminal escape sequences, as a careless service would print them: the viewer must show them inert.
      return (t, i) => i % 9 === 4 ? `${sys(t)} ${host} prometheus[2231]: \x1b[31mlevel=WARN\x1b[0m msg="Error on ingesting samples" err="out of order sample"\x07` : `${sys(t)} ${host} prometheus[2231]: time=${new Date(t).toISOString()} level=INFO source=head.go msg="Head GC completed" duration=${(1.2 + (i % 7) * 0.4).toFixed(1)}ms`
    case 'pg':
      // ISO timestamps, and a DETAIL line now and then that has none of its own (it belongs to the line before).
      return (t, i) => i % 6 === 5 ? `\tDETAIL:  Key (id)=(${4100 + i}) already exists.` : `${new Date(t).toISOString()} [${2400 + (i % 5)}] ${i % 6 === 4 ? 'ERROR:  duplicate key value violates unique constraint "orders_pkey"' : `LOG:  checkpoint complete: wrote ${12 + (i % 40)} buffers`}`
    default:
      return (t, i) => {
        const m = [
          `kernel: pflog0: promiscuous mode enabled`,
          `syslogd: kernel boot file is /boot/kernel/kernel`,
          `postgres[${2400 + (i % 5)}]: [${(i % 3) + 1}-1] LOG:  checkpoint complete: wrote ${12 + (i % 40)} buffers (0.1%); write=${(0.4 + (i % 9) / 10).toFixed(1)} s`,
          `nginx: the configuration file /usr/local/etc/nginx/nginx.conf syntax is ok`,
          `ntpd[${880}]: leap second file expires soon`,
          `postgres[${2410 + (i % 3)}]: [1-1] LOG:  connection received: host=10.20.0.11 port=${52000 + i}`,
          `su[${7100 + i}]: deploy to root on /dev/pts/0`,
        ]
        return `${sys(t)} ${host} ${pick(m, i)}`
      }
  }
}

/** The files below /var/log of a demo jail, by its name. */
interface Layout { name: string; kind: string; count: number; grows: boolean; rotated?: number; gz?: number }
function layout(jail: string): Layout[] {
  const base: Layout[] = [
    { name: 'messages', kind: 'messages', count: 260, grows: true, rotated: 1, gz: 2 },
    { name: 'auth.log', kind: 'auth', count: 60, grows: false, gz: 1 },
    { name: 'cron', kind: 'cron', count: 120, grows: false },
    { name: 'daemon.log', kind: 'daemon', count: 90, grows: false },
  ]
  if (jail === 'web' || jail === 'proxy') base.push({ name: 'nginx/access.log', kind: 'access', count: 400, grows: true, rotated: 1 }, { name: 'nginx/error.log', kind: 'error', count: 40, grows: false })
  if (jail === 'db') base.push({ name: 'postgresql/postgresql.log', kind: 'pg', count: 80, grows: true })
  return base
}

/** Log state of every jail of the demo, created on first use. */
export function demoLogs() {
  const jails = new Map<string, Map<string, File & { modified: number }>>()

  function filesOf(jail: string) {
    let m = jails.get(jail)
    if (m) return m
    m = new Map()
    const now = Date.now()
    for (const f of layout(jail)) {
      const gen = generator(f.kind, jail)
      const mk = (count: number, endAgo: number, step: number, seed: number) => {
        const lines: Line[] = []
        for (let i = 0; i < count; i++) { const ts = now - endAgo - (count - i) * step; lines.push({ text: gen(ts, seed + i), ts }) }
        return lines
      }
      let i = f.count
      const step = Math.round((26 * 3600_000) / f.count) // a day and a bit, so "since" has something to cut
      const lines = mk(f.count, 4_000, step, 0)
      const file = { lines, size: 0, grows: f.grows, last: now, rotations: 0, modified: now - 4_000, next: () => gen(Date.now(), i++) }
      file.size = lines.reduce((n, l) => n + l.text.length + 1, 0)
      m.set(f.name, file)
      for (let r = 0; r < (f.rotated ?? 0); r++) {
        const old = mk(f.count, 86400_000 * (r + 1), step, 5000 + r * 1000)
        m.set(`${f.name}.${r}`, { lines: old, size: old.reduce((n, l) => n + l.text.length + 1, 0), grows: false, last: now, rotations: 0, modified: now - 86400_000 * (r + 1), next: () => '' })
      }
      for (let r = 0; r < (f.gz ?? 0); r++) {
        const at = (f.rotated ?? 0) + r
        m.set(`${f.name}.${at}.gz`, { lines: [], size: 18_000 + r * 2311, grows: false, last: now, rotations: 0, modified: now - 86400_000 * (at + 1), next: () => '' })
      }
    }
    jails.set(jail, m)
    return m
  }

  /** A file that grows gets about one line a second while someone reads it; a long one is rotated now and then. */
  function grow(f: File & { modified: number }): void {
    if (!f.grows) return
    const now = Date.now()
    const n = Math.min(5, Math.floor((now - f.last) / 900))
    if (n < 1) return
    f.last = now
    if (f.lines.length > 1200) {
      f.lines = []
      f.size = 0
      f.rotations++
    }
    for (let i = 0; i < n; i++) {
      const text = f.next()
      f.lines.push({ text, ts: now })
      f.size += text.length + 1
    }
    f.modified = now
  }

  return {
    list(jail: string): JailLogFile[] {
      return [...filesOf(jail).entries()].map(([name, f]) => {
        const compressed = /\.(gz|bz2|xz)$/.test(name)
        return { name, size_bytes: f.size, modified_at: new Date(f.modified).toISOString(), kind: compressed || /\.\d+$/.test(name) ? 'rotated' : 'active', ...(compressed ? { compressed: true } : {}) }
      })
    },
    read(jail: string, file: string, q: URLSearchParams): JailLogRead | 'missing' | 'compressed' {
      const r = this.entries(jail, file, q)
      if (typeof r === 'string') return r
      const { entries, ...out } = r
      return { ...out, lines: entries.map((l) => ({ text: l.text, ...(l.n ? { n: l.n } : {}) })) }
    },
    /** A read as the server makes it, with the time each line was written (the unified view merges by it). */
    entries(jail: string, file: string, q: URLSearchParams): DemoRead | 'missing' | 'compressed' {
      const f = filesOf(jail).get(file)
      if (!f) return 'missing'
      if (/\.(gz|bz2|xz)$/.test(file)) return 'compressed'
      let offset = Number(q.get('offset') ?? 0)
      if (offset > 0) grow(f)
      const out: DemoRead = { jail, file, entries: [], next_offset: f.size }
      if (offset > f.size) { out.rotated = true; offset = 0 }
      const sinceRaw = q.get('since') ?? ''
      const m = /^(\d+)([mhd])$/.exec(sinceRaw)
      const since = m ? Date.now() - Number(m[1]) * { m: 60_000, h: 3600_000, d: 86400_000 }[m[2] as 'm' | 'h' | 'd'] : 0
      const grep = (q.get('grep') ?? '').toLowerCase()
      const tail = Number(q.get('limit') ?? 200)
      let pos = 0
      f.lines.forEach((l, i) => {
        const start = pos
        pos += l.text.length + 1
        if (start < offset) return
        if (since && l.ts < since) return
        if (grep && !l.text.toLowerCase().includes(grep)) return
        out.entries.push({ text: l.text, ts: l.ts, ...(offset === 0 ? { n: i + 1 } : {}) })
      })
      if (out.entries.length > tail) { out.entries = out.entries.slice(-tail); out.truncated = true }
      return out
    },
  }
}
