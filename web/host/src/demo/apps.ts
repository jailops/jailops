// Canned catalog and jail logs for `?demo`. The five definitions follow packaging/apps/*.yaml; a sixth
// one is invalid so the catalog shows that state. Logs get timestamps relative to now and
// grow while they are followed.
import type { App, AppDetail, AppSummary, JailLogFile, JailLogRead } from '@jailops/shared/api/types'

const hash = (name: string) => 'sha256:' + Array.from({ length: 64 }, (_, i) => '0123456789abcdef'[(name.charCodeAt(i % name.length) * (i + 7)) % 16]).join('')

// A plain geometric mark (not a product logo), so the catalog shows the icon path as well as the monogram.
const svg = (body: string) => 'data:image/svg+xml;base64,' + btoa(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">${body}</svg>`)
const ICONS: Record<string, string> = {
  nginx: svg('<path d="M32 4 56 18v28L32 60 8 46V18z" fill="#1f8f4e"/><path d="M23 44V20l18 24V20" fill="none" stroke="#fff" stroke-width="5" stroke-linejoin="round"/>'),
  prometheus: svg('<circle cx="32" cy="32" r="28" fill="#d8572a"/><path d="M20 40h24M22 46h20M32 14c6 8 10 12 6 22h-12c-4-10 0-14 6-22z" fill="none" stroke="#fff" stroke-width="4" stroke-linecap="round"/>'),
}

const IMAGE = { os: 'freebsd', release_min: '15.1' }

export const definitions: App[] = [
  {
    version: 1, name: 'nginx', title: 'nginx', summary: 'Web server and reverse proxy, serving /usr/local/www.',
    description: 'nginx is a small, fast web server and reverse proxy. The package installs the\nserver with its dynamic modules and a default site under /usr/local/www/nginx-dist,\nwhich this definition copies to /usr/local/www/nginx, the root of the default\nconfiguration; the configuration lives in /usr/local/etc/nginx/nginx.conf and is checked\n(nginx -t) before every start and reload.\n',
    category: 'web', homepage: 'https://nginx.org/', license: 'BSD-2-Clause', icon: 'nginx.svg', image: IMAGE,
    packages: [{ name: 'nginx', repo: 'ports', version: '1.30.6,3' }],
    services: [{ name: 'nginx', start: true }],
    ports: [
      { name: 'http', port: 80, proto: 'tcp', description: 'HTTP, the default site.', expose: true },
      { name: 'https', port: 443, proto: 'tcp', description: 'HTTPS; not configured until a certificate and a server block are added.', expose: false },
    ],
    data: [{ path: '/usr/local/www', description: 'The sites; the default site is nginx, copied from nginx-dist.' }],
    config: [{ path: '/usr/local/etc/nginx/nginx.conf', description: "The server configuration; 'service nginx reload' applies most changes without a restart.", restart: 'nginx' }],
    logs: [
      { name: 'access', path: '/var/log/nginx/access.log', description: 'One line per request.' },
      { name: 'error', path: '/var/log/nginx/error.log', description: 'Errors and warnings of the server.' },
    ],
    setup: [['/bin/mkdir', '-p', '/usr/local/www/nginx'], ['/bin/cp', '-Rn', '/usr/local/www/nginx-dist/.', '/usr/local/www/nginx/']],
    health: { http: { port: 80, path: '/', status: 200 }, wait_seconds: 30 },
    resources: { memory: '256M', disk: '2G', cpu: '1' },
    notes: "Put the site under /usr/local/www and its server block in\n/usr/local/etc/nginx/nginx.conf; run 'service nginx reload' inside the jail to\napply a change. Add a certificate before enabling the https port.\n",
  },
  {
    version: 1, name: 'postgresql', title: 'PostgreSQL', summary: 'Relational database server, PostgreSQL 18 from the FreeBSD packages.',
    description: 'PostgreSQL is a relational database with transactions, replication and a large\nextension ecosystem. This definition installs the server package of the current\nmajor release, initialises a cluster under /var/db/postgres/data18 with UTF-8 and\nthe C collation (the port\'s defaults) and starts it.\n\nThe cluster listens on localhost only until listen_addresses in postgresql.conf\nand the client rules in pg_hba.conf are changed. The port logs to syslog.\n',
    category: 'database', homepage: 'https://www.postgresql.org/', license: 'PostgreSQL', image: IMAGE,
    packages: [{ name: 'postgresql18-server', repo: 'ports', version: '18.6' }],
    services: [{ name: 'postgresql', start: true }],
    ports: [{ name: 'postgres', port: 5432, proto: 'tcp', description: 'The SQL port; listens on localhost until listen_addresses is set.', expose: false }],
    data: [{ path: '/var/db/postgres', description: 'The cluster (data18) and the home of the postgres user.' }],
    config: [
      { path: '/var/db/postgres/data18/postgresql.conf', description: 'Server settings, including listen_addresses and logging.', restart: 'postgresql' },
      { path: '/var/db/postgres/data18/pg_hba.conf', description: 'Who may connect from where, and how they authenticate.', restart: 'postgresql' },
    ],
    logs: [{ name: 'server', path: '/var/log/messages', description: 'The port logs to syslog by default (log_destination in postgresql.conf).' }],
    setup: [['/usr/sbin/chown', 'postgres:postgres', '/var/db/postgres'], ['/usr/sbin/service', 'postgresql', 'initdb']],
    health: { exec: ['/usr/local/bin/pg_isready', '-U', 'postgres'], wait_seconds: 60 },
    resources: { memory: '1G', disk: '10G', cpu: '1' },
    notes: "Connect from inside the jail with: jailops exec NAME -- su -l postgres -c psql\nTo accept connections from the network, set listen_addresses = '*' in\npostgresql.conf, add a host rule to pg_hba.conf and restart the service.\n",
  },
  {
    version: 1, name: 'valkey', title: 'Valkey', summary: 'In-memory key-value store, the BSD-licensed successor of Redis.',
    description: 'Valkey is a high-performance data structure server that serves key/value\nworkloads, compatible with the Redis protocol and clients. The package ships\nthe server, the command line client and a sentinel; this definition runs the\nserver with the configuration the package installs.\n\nThe server binds 127.0.0.1 and ::1 with protected mode on until\n/usr/local/etc/valkey.conf is changed.\n',
    category: 'database', homepage: 'https://valkey.io/', license: 'BSD-3-Clause', image: IMAGE,
    packages: [{ name: 'valkey', repo: 'ports', version: '9.1.2' }],
    services: [{ name: 'valkey', start: true }],
    ports: [{ name: 'valkey', port: 6379, proto: 'tcp', description: 'The protocol port; bound to localhost until bind is changed.', expose: false }],
    data: [{ path: '/var/db/valkey', description: 'Snapshots and the append-only file.' }],
    config: [{ path: '/usr/local/etc/valkey.conf', description: 'The server configuration (bind, protected-mode, persistence).', restart: 'valkey' }],
    logs: [{ name: 'server', path: '/var/log/valkey/valkey.log', description: 'The server log.' }],
    setup: [['/usr/sbin/chown', 'valkey:valkey', '/var/db/valkey']],
    health: { exec: ['/usr/local/bin/valkey-cli', 'ping'], wait_seconds: 30 },
    resources: { memory: '512M', disk: '2G', cpu: '1' },
    notes: 'Talk to it from inside the jail with: jailops exec NAME -- valkey-cli\nTo reach it from other jails, set bind and requirepass in valkey.conf and\nrestart the service.\n',
  },
  {
    version: 1, name: 'forgejo', title: 'Forgejo', summary: 'Self-hosted git forge with issues, pull requests and CI hooks.',
    description: 'Forgejo is a compact, self-hosted software forge: git hosting, issues, pull\nrequests, wikis, packages and a web interface. The package runs it as the git\nuser under /var/db/forgejo with the configuration in\n/usr/local/etc/forgejo/conf/app.ini.\n\nThe installation leaves the configuration to the web installer: the first\nvisit to port 3000 asks for the database (SQLite under /var/db/forgejo works\nout of the box) and the first account, which becomes the administrator.\n',
    category: 'devops', homepage: 'https://forgejo.org/', license: 'MIT', image: IMAGE,
    packages: [{ name: 'forgejo16', repo: 'ports', version: '16.0.6_1' }],
    services: [{ name: 'forgejo', rc: { forgejo_configcheck_enable: 'NO' }, start: true }],
    ports: [{ name: 'http', port: 3000, proto: 'tcp', description: 'The web interface and the HTTP git endpoint.', expose: true }],
    data: [{ path: '/var/db/forgejo', description: 'Repositories, the SQLite database, attachments, indexes and sessions.' }],
    config: [{ path: '/usr/local/etc/forgejo/conf/app.ini', description: 'The configuration the web installer writes; secrets included, keep it private.', restart: 'forgejo' }],
    logs: [
      { name: 'app', path: '/var/log/forgejo', description: 'Log files, when the [log] section writes files.' },
      { name: 'syslog', path: '/var/log/daemon.log', description: 'What the service prints; daemon(8) sends it to syslog as daemon.info.' },
    ],
    setup: [['/bin/rm', '-f', '/usr/local/etc/forgejo/conf/app.ini'], ['/usr/sbin/chown', '-R', 'git:git', '/usr/local/etc/forgejo'], ['/usr/sbin/chown', '-R', 'git:git', '/var/db/forgejo', '/var/log/forgejo']],
    health: { tcp: 3000, wait_seconds: 60 },
    resources: { memory: '1G', disk: '10G', cpu: '1' },
    notes: "Open http://ADDRESS:3000/ to finish the setup in the browser: choose SQLite3\nwith /var/db/forgejo/forgejo.db, keep the repository root\n/var/db/forgejo/forgejo-repositories, set the server domain and base URL to\nthe address you reach the jail by, and register the first user, who becomes\nthe administrator. The installer writes app.ini; later changes to it need\n'service forgejo restart' inside the jail.\n",
  },
  {
    version: 1, name: 'prometheus', title: 'Prometheus', summary: 'Metrics collection, storage and alerting with PromQL.',
    description: 'Prometheus scrapes metrics from HTTP endpoints, stores them as time series\nand evaluates alerting rules. The package installs the current major release\nwith a configuration that scrapes Prometheus itself; scrape targets and rule\nfiles are added to /usr/local/etc/prometheus.yml.\n\nThe time series database lives under /var/db/prometheus. The web interface\nand the API are on port 9090.\n',
    category: 'monitoring', homepage: 'https://prometheus.io/', license: 'Apache-2.0', icon: 'prometheus.svg', image: IMAGE,
    packages: [{ name: 'prometheus3', repo: 'ports', version: '3.15.0_2' }],
    services: [{ name: 'prometheus', start: true }],
    ports: [{ name: 'http', port: 9090, proto: 'tcp', description: 'The web interface, the query API and /metrics.', expose: false }],
    data: [{ path: '/var/db/prometheus', description: 'The time series database (storage.tsdb.path).' }],
    config: [{ path: '/usr/local/etc/prometheus.yml', description: "Scrape targets, rule files and alertmanagers; 'service prometheus reload' applies a change.", restart: 'prometheus' }],
    logs: [{ name: 'syslog', path: '/var/log/daemon.log', description: 'The server log; daemon(8) sends it to syslog as daemon.info.' }],
    setup: [['/usr/sbin/chown', 'prometheus:prometheus', '/var/db/prometheus']],
    health: { http: { port: 9090, path: '/-/healthy', status: 200 }, wait_seconds: 60 },
    resources: { memory: '1G', disk: '20G', cpu: '1' },
    notes: 'Add scrape targets under scrape_configs in /usr/local/etc/prometheus.yml and\nrun \'service prometheus reload\' inside the jail. Retention defaults to 15\ndays; set prometheus_args="--storage.tsdb.retention.time=90d" in the jail\'s\nrc.conf to change it.\n',
  },
]

/** An operator's own definition that fails validation: listed with the reason, never installable. */
const broken: AppSummary = {
  name: 'wiki', title: 'Team wiki', summary: 'A definition dropped into the catalog directory by hand.', category: 'tools', valid: false,
  error: 'wiki.yaml: data[0].path "/etc/wiki" lies under the base system; data paths lie below /var, /usr/local or /home',
}

export function catalog(): AppSummary[] {
  return [
    ...definitions.map((a) => ({ name: a.name, title: a.title, summary: a.summary, category: a.category, version: a.packages[0]?.version, valid: true })),
    broken,
  ]
}

export function detail(name: string): AppDetail | 'invalid' | undefined {
  if (name === broken.name) return 'invalid'
  const a = definitions.find((x) => x.name === name)
  if (!a) return undefined
  return { definition: structuredClone(a), hash: hash(a.name), file: `/usr/local/share/jailops/apps/${a.name}.yaml`, ...(ICONS[a.name] ? { icon: ICONS[a.name] } : {}) }
}
export const appHash = hash
export const LABEL = 'jailops.app'
export const invalidApp = broken

/** The steps the server describes for an installation, in its "NN name: effect" form. */
export function installSteps(a: App, jail: string, project: string, image: string): string[] {
  const root = `/jailops/${project}/${jail}/root`
  const out: Array<[string, string]> = [
    ['create-jail', `create thin jail ${jail} from ${image} with ${a.data?.length ?? 0} data volume(s) and the labels jailops.app, jailops.app.hash`],
    ['start-jail', `start ${jail}`],
    ['install-packages-ports', `pkg -r ${root} install -y ${a.packages.map((p) => p.name).join(' ')}`],
    ['refresh-library-cache', 'in the jail: /sbin/ldconfig -m /usr/local/lib'],
    ['record-version', `pkg -r ${root} query %v ${a.packages[0].name}, then label jailops.app.version`],
    ['write-rc-conf', `set ${(a.services ?? []).flatMap((s) => [`${s.name}_enable`, ...Object.keys(s.rc ?? {})]).join(', ')} in ${root}/etc/rc.conf`],
    ...(a.setup ?? []).map((cmd, i): [string, string] => [`setup-${i + 1}`, `in the jail: ${cmd.join(' ')}`]),
    ...(a.services ?? []).filter((s) => s.start !== false).map((s): [string, string] => [`start-service-${s.name}`, `in the jail: /usr/sbin/service ${s.name} start`]),
  ]
  const h = a.health
  if (h) out.push(['health-check', h.http ? `GET http://<first address>:${h.http.port}${h.http.path} until it answers ${h.http.status ?? 200}, at most ${h.wait_seconds ?? 60} attempts` : h.tcp ? `connect to <first address>:${h.tcp} until it accepts, at most ${h.wait_seconds ?? 60} attempts` : `in the jail: ${h.exec?.join(' ')} until it exits 0, at most ${h.wait_seconds ?? 60} attempts`])
  out.push(['snapshot', `snapshot every dataset of ${jail} as app-installed`])
  return out.map(([n, d], i) => `${String(i + 1).padStart(2, '0')} ${n}: ${d}`)
}

// ---- jail logs ---------------------------------------------------------------------------------------

interface Line { text: string; ts: number }
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
    case 'valkey':
      return (t, i) => `2231:M ${new Date(t).getDate()} ${MON[new Date(t).getMonth()]} ${new Date(t).getFullYear()} ${new Date(t).toTimeString().slice(0, 8)}.${String(i % 1000).padStart(3, '0')} * ${i % 4 ? '100 changes in 300 seconds. Saving...' : 'Background saving terminated with success'}`
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

/** The files below /var/log of a demo jail, by what it runs. */
interface Layout { name: string; kind: string; count: number; grows: boolean; rotated?: number; gz?: number }
function layout(jail: string, app: string): Layout[] {
  const base: Layout[] = [
    { name: 'messages', kind: 'messages', count: 260, grows: true, rotated: 1, gz: 2 },
    { name: 'auth.log', kind: 'auth', count: 60, grows: false, gz: 1 },
    { name: 'cron', kind: 'cron', count: 120, grows: false },
    { name: 'daemon.log', kind: 'daemon', count: 90, grows: app === 'prometheus' || app === 'forgejo' },
  ]
  if (app === 'nginx' || jail === 'web' || jail === 'proxy') base.push({ name: 'nginx/access.log', kind: 'access', count: 400, grows: true, rotated: 1 }, { name: 'nginx/error.log', kind: 'error', count: 40, grows: false })
  if (app === 'valkey') base.push({ name: 'valkey/valkey.log', kind: 'valkey', count: 80, grows: true })
  if (app === 'forgejo') base.push({ name: 'forgejo/forgejo.log', kind: 'messages', count: 50, grows: true })
  return base
}

/** Log state of every jail of the demo, created on first use. */
export function demoLogs() {
  const jails = new Map<string, Map<string, File & { modified: number }>>()

  function filesOf(jail: string, app: string) {
    let m = jails.get(jail)
    if (m) return m
    m = new Map()
    const now = Date.now()
    for (const f of layout(jail, app)) {
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
    list(jail: string, app: string): JailLogFile[] {
      return [...filesOf(jail, app).entries()].map(([name, f]) => {
        const compressed = /\.(gz|bz2|xz)$/.test(name)
        return { name, size: f.size, modified_at: new Date(f.modified).toISOString(), kind: compressed || /\.\d+$/.test(name) ? 'rotated' : 'active', ...(compressed ? { compressed: true } : {}) }
      })
    },
    read(jail: string, app: string, file: string, q: URLSearchParams): JailLogRead | 'missing' | 'compressed' {
      const f = filesOf(jail, app).get(file)
      if (!f) return 'missing'
      if (/\.(gz|bz2|xz)$/.test(file)) return 'compressed'
      let offset = Number(q.get('offset') ?? 0)
      if (offset > 0) grow(f)
      const out: JailLogRead = { jail, file, lines: [], next_offset: f.size }
      if (offset > f.size) { out.rotated = true; offset = 0 }
      const sinceRaw = q.get('since') ?? ''
      const m = /^(\d+)([mhd])$/.exec(sinceRaw)
      const since = m ? Date.now() - Number(m[1]) * { m: 60_000, h: 3600_000, d: 86400_000 }[m[2] as 'm' | 'h' | 'd'] : 0
      const grep = (q.get('grep') ?? '').toLowerCase()
      const tail = Number(q.get('tail') ?? 200)
      let pos = 0
      f.lines.forEach((l, i) => {
        const start = pos
        pos += l.text.length + 1
        if (start < offset) return
        if (since && l.ts < since) return
        if (grep && !l.text.toLowerCase().includes(grep)) return
        out.lines.push({ text: l.text, ...(offset === 0 ? { n: i + 1 } : {}) })
      })
      if (out.lines.length > tail) { out.lines = out.lines.slice(-tail); out.truncated = true }
      return out
    },
  }
}
