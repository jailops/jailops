// An in-process Transport that answers like the real API, so the whole UI (including async
// operations, ETags, events and API errors) can be exercised without a server. Only loaded
// for `?demo`.
import { storedToken } from '@jailops/shared/api/client'
import type { EventStream, RawRequest, RawResponse, StreamHandlers, Transport } from '@jailops/shared/api/client'
import type { ApiEvent, ApiErrorBody, Operation, RestorePreflight, FirewallPolicy, AddressSet, Token, Backup, Jail, JailRollback, JailUpgrade, PowerState } from '@jailops/shared/api/types'
import * as d from './data'
import * as d2 from './data2'
import * as da from './apps'
import * as dh from './host'
import { validDNSName } from '@/lib/host'
import * as dw from './wireguard'
import { demoAccess } from './access'
import { volumeName } from '@/lib/apps'
import { lookupKey, setValue, expandedSources, flatten } from '@/lib/configKeys'
import { loadChoice, saveChoice } from '@/lib/projectScope'

interface DemoOp extends Operation {
  startedAt: number
  duration: number
  onDone?: () => void
  done?: boolean
  metadataFinal?: unknown
  /** Ends the operation with this error instead of success (the demo's failed installation). */
  failure?: ApiErrorBody
}

const GiB = 1024 ** 3
export interface DemoOptions {
  /** Start signed out so the login view can be shown. */
  signedOut?: boolean
  /** Speed factor for operation durations (tests use a large one). */
  speed?: number
}

const sleep0 = () => Promise.resolve()

export function demoTransport(opts: DemoOptions = {}): Transport {
  // Like a user who has already chosen a project (`project switch web`); `?demo=fresh` is a first visit with none.
  if (typeof location !== 'undefined' && !location.search.includes('fresh') && !location.search.includes('empty')) { if (!loadChoice()) saveChoice('web') }
  const speed = opts.speed ?? 1
  let authed = !opts.signedOut
  const db = {
    jails: [...d.jails(), ...d2.stagingJails()], snaps: d.snapshots(), images: d.images(), networks: d.networks(), leases: d.leases(),
    policies: d.policies(), sets: d.addressSets(), targets: d.targets(), bpol: d.backupPolicies(), backups: d.backups(),
    remotes: d.remotes(), audit: d.audit(), tokens: d.tokens(), users: d.users(), certs: d.certificates(),
    ops: [] as DemoOp[],
    storage: d2.storages(), sessions: d2.sessions(), certs2: d2.certificates(), profiles: d2.profiles(), updates: [] as import('@jailops/shared/api/types').UpdateReport[], history: {} as Record<string, import('@jailops/shared/api/types').UpdateRecord[]>, projects: [] as ReturnType<typeof d2.projects>,
    host: dh.hostState(),
    wireguard: dw.tunnels(),
    key: { status: 'present' as 'none' | 'present' | 'escrowed', domain: 'kd-7f3a91', path: '/var/db/jailops/backup.key' },
  }
  for (const j of db.jails) {
    j.spec.provision = d2.provisionSpec(j.metadata.name)
    j.status.provisioned_at = d2.provisionedAt(j.metadata.name)
    if (j.metadata.name === 'web') j.spec.update = { mode: 'notify', schedule: '0 4 * * *', packages: true, os: true, reboot: 'manual' }
    if (j.metadata.name === 'db') j.spec.update = { mode: 'managed', schedule: '0 3 * * 0', packages: true, os: true, reboot: 'manual' }
    j.spec.profiles = j.metadata.name === 'web' ? ['web-limits'] : j.spec.profiles
  }
  db.updates = d2.updateReports(db.jails)
  for (const j of db.jails) db.history[j.metadata.name] = d2.updateHistory(j.metadata.name)
  if (typeof location !== 'undefined' && location.search.includes('empty')) {
    // A host that was just installed: nothing is configured yet (`?demo=empty`).
    db.jails = []; db.snaps = {} as typeof db.snaps; db.images = []; db.networks = []; db.leases = {} as typeof db.leases
    db.storage = []; db.profiles = { project: [], host: [] } as typeof db.profiles; db.updates = []; db.policies = []; db.sets = []
    db.targets = []; db.bpol = []; db.backups = []; db.remotes = []; db.wireguard = []
  }
  const countByProject = () => { const c: Record<string, number> = {}; for (const j of db.jails) c[j.metadata.project ?? ''] = (c[j.metadata.project ?? ''] ?? 0) + 1; return c }
  db.projects = d2.projects(countByProject())
  if (typeof location !== 'undefined' && location.search.includes('empty')) db.projects = []
  const shells = new Map<string, { h: StreamHandlers; line: string; jail: string }>()
  const logs = da.demoLogs()
  const access = demoAccess(() => db.jails, () => db.sets, (app) => { const x = da.detail(app); return x && x !== 'invalid' ? x.definition : undefined })
  for (const o of d.operations()) db.ops.push({ ...o, startedAt: Date.parse(o.created_at), duration: o.status === 'running' ? 600_000 : 0, done: o.status !== 'running' })
  const subs = new Set<StreamHandlers>()
  const pending: Array<{ jail: string; to: PowerState; at: number }> = []
  let seq = 2000

  // A real server answers with fresh JSON every time; clone so in-place demo mutations are seen as changes.
  const ok = (metadata: unknown, etag?: string): RawResponse => ({ status: 200, etag, envelope: { type: 'sync', status: 'Success', status_code: 200, metadata: metadata === undefined || metadata === null ? metadata : structuredClone(metadata) } })
  const fail = (status: number, e: ApiErrorBody): RawResponse => ({ status, envelope: { type: 'error', status: 'Failure', status_code: status, error: e } })
  const etagOf = (generation: number | undefined) => `"${generation ?? 0}"`

  function emit(action: string, source: string, who = 'demo'): void {
    const e: ApiEvent = { type: 'lifecycle', timestamp: new Date().toISOString(), metadata: { action, source, requestor: who } }
    subs.forEach((h) => h.onEvent(e))
  }
  function startOp(desc: string, resources: Operation['resources'], duration: number, apply: () => void, metadata?: unknown, may_cancel = true): RawResponse {
    const op: DemoOp = {
      id: `op-${++seq}`, description: desc, status: 'pending', resources, may_cancel, created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
      startedAt: Date.now(), duration: duration / speed, onDone: apply, metadataFinal: metadata,
    }
    db.ops.unshift(op)
    return { status: 202, envelope: { type: 'async', status: 'Operation created', status_code: 100, operation: `/api/v1/operations/${op.id}`, metadata: view(op) } }
  }
  function view(o: DemoOp): Operation {
    if (!o.done && o.status !== 'cancelled') {
      const t = Date.now() - o.startedAt
      if (t >= o.duration && o.failure) {
        o.done = true
        o.status = 'failure'
        o.error = o.failure
        o.onDone?.()
      } else if (t >= o.duration) {
        o.done = true
        o.status = 'success'
        o.progress = 100
        o.metadata = o.metadataFinal as never
        o.onDone?.()
      } else {
        o.status = t < 150 / speed ? 'pending' : 'running'
        o.progress = Math.min(95, Math.round((t / o.duration) * 100))
      }
      o.updated_at = new Date().toISOString()
    }
    const { startedAt: _s, duration: _d, onDone: _o, done: _x, metadataFinal: _m, failure: _f, ...pub } = o
    return pub
  }
  function settleJails(): void {
    for (let i = pending.length - 1; i >= 0; i--) {
      const p = pending[i]
      if (Date.now() >= p.at) {
        const j = db.jails.find((x) => x.metadata.name === p.jail)
        if (j) {
          j.status.power = p.to
          if (p.to === 'running') {
            j.status.jid = 100 + Math.floor(Math.random() * 50)
            j.status.started_at = new Date().toISOString()
            j.status.addresses = [(db.leases[j.spec.network?.[0]?.network ?? 'lan']?.find((l) => l.jail === j.metadata.name)?.address ?? '10.20.0.50') + '/24']
          } else {
            j.status.jid = undefined
            j.status.addresses = []
            j.status.started_at = undefined
          }
        }
        pending.splice(i, 1)
      }
    }
  }
  const jailOr404 = (n: string): Jail | undefined => db.jails.find((j) => j.metadata.name === n)

  async function route(req: RawRequest): Promise<RawResponse> {
    await sleep0()
    if (!authed && storedToken()) authed = true // any bearer token is accepted in the demo
    settleJails()
    const url = new URL(req.path, 'http://x')
    const path = url.pathname.replace(/\/$/, '')
    const seg = path.split('/').filter(Boolean).map(decodeURIComponent)
    const m = req.method
    const body = (req.body === undefined ? {} : JSON.parse(JSON.stringify(req.body))) as Record<string, any> // the wire is JSON: never keep the caller's reactive objects

    if (path === '' && m === 'GET') return ok({ ...d.server(), auth: authed ? 'trusted' : 'untrusted' })
    if (seg[0] === 'auth') {
      if (seg[1] === 'session') return authed ? ok({ ...d2.identity(), csrf: 'demo-csrf' }) : fail(401, { code: 'unauthorized', message: 'No session.' })
      if (seg[1] === 'login') {
        if (!body.username) return fail(400, { code: 'invalid', message: 'username is required' })
        if (body.username === 'alice' && !body.totp) return fail(401, { code: 'unauthorized', message: 'A one-time code is required.', hint: 'Enter the six digits from your authenticator.', detail: { totp: 'required' } })
        authed = true
        return ok({ user: body.username, permission: 'admin', csrf: 'demo-csrf' })
      }
      authed = false
      return ok({})
    }
    if (!authed) return fail(401, { code: 'unauthorized', message: 'Authentication is required.', hint: 'Sign in with a token or a user.' })
    if (m === 'GET' && seg[0] === 'resources') return ok(d.pool())
    // There is no default project: what works inside one needs it named, like the real server.
    const project = url.searchParams.get('project') ?? ''
    if (!project && ['jails', 'firewall', 'profiles', 'updates', 'restores'].includes(seg[0])) return fail(400, { code: 'invalid', message: 'a project is required', hint: "name one with --project NAME (list them with 'jailops project list')" })
    const inProject = (j: Jail) => (j.metadata.project ?? '') === project
    for (const p of db.projects) p.status.jails = countByProject()[p.metadata.name] ?? 0
    const extra = extraRoutes(seg, m, body, url, req, project)
    if (extra) return extra

    switch (seg[0]) {
      case 'jails': return jailsRoute(seg, m, body, inProject, url)
      case 'images':
        if (seg.length === 1) return ok(db.images)
        if (seg[2] === 'generations' && m === 'POST') {
          const img = db.images.find((i) => i.metadata.name === seg[1])
          if (!img) return fail(404, { code: 'not_found', message: `Image ${seg[1]} not found` })
          return startOp(`Build ${seg[1]}#g${(img.status.generations?.length ?? 0) + 1}`, { images: [`/api/v1/images/${seg[1]}`] }, 4000, () => {
            const n = Math.max(...(img.status.generations ?? []).map((g) => g.number)) + 1
            img.status.generations!.push({ number: n, name: `${seg[1]}#g${n}`, parent: n - 1, manifest: Math.random().toString(16).slice(2).padEnd(40, 'e'), packages: 222, created_at: new Date().toISOString(), refs: [] })
          })
        }
        if (seg[2] === 'upgrade' && m === 'POST') {
          const img = db.images.find((i) => i.metadata.name === seg[1])
          if (!img) return fail(404, { code: 'not_found', message: `Image ${seg[1]} not found` })
          return startOp(`Upgrade image ${seg[1]}`, { images: [`/api/v1/images/${seg[1]}`] }, 6000, () => {
            const n = Math.max(...(img.status.generations ?? []).map((g) => g.number)) + 1
            img.status.generations!.push({ number: n, name: `${seg[1]}#g${n}`, parent: n - 1, manifest: Math.random().toString(16).slice(2).padEnd(40, 'e'), packages: 222, created_at: new Date().toISOString(), refs: [] })
          })
        }
        if (seg[2] === 'aliases' && seg[3] && (m === 'PUT' || m === 'DELETE')) {
          const img = db.images.find((i) => i.metadata.name === seg[1])
          if (!img) return fail(404, { code: 'not_found', message: `Image ${seg[1]} not found` })
          const aliases = (img.status.aliases ??= {})
          if (m === 'DELETE') {
            if (!(seg[3] in aliases)) return fail(404, { code: 'not_found', message: `Image ${seg[1]} has no alias ${seg[3]}` })
            delete aliases[seg[3]]
            img.spec.aliases = (img.spec.aliases ?? []).filter((a) => a !== seg[3])
          } else {
            const n = body?.generation || Math.max(0, ...(img.status.generations ?? []).map((g) => g.number))
            if (!(img.status.generations ?? []).some((g) => g.number === n)) return fail(404, { code: 'not_found', message: `Generation g${n} does not exist` })
            aliases[seg[3]] = n
            if (!(img.spec.aliases ?? []).includes(seg[3])) img.spec.aliases = [...(img.spec.aliases ?? []), seg[3]]
          }
          return ok(img)
        }
        break
      case 'networks':
        if (seg.length === 1 && m === 'POST') {
          if (db.networks.some((n) => n.metadata.name === body.metadata?.name)) return fail(409, { code: 'already_exists', message: `Network ${body.metadata.name} already exists.` })
          const sp = body.spec as import('@jailops/shared/api/types').Network['spec']
          if (sp.ipv6?.mode === 'proxy' && !/^fe80:/i.test(sp.ipv6.gateway)) return fail(400, { code: 'invalid', message: 'in proxy mode the gateway must be the bridge\'s link-local address', hint: 'use a gateway such as fe80::1' })
          const net = { metadata: { name: body.metadata.name, scope: 'host' as const, generation: 1, created_at: new Date().toISOString() }, spec: { bridge: `jo${db.networks.length}`, ...sp }, status: { bridge_up: true, allocated: 0, free: 100 } }
          return startOp(`Create network ${body.metadata.name}`, {}, 500, () => db.networks.push(net as never), undefined, false)
        }
        if (seg.length === 1) return ok(db.networks)
        if (seg[2] === 'leases') return ok(db.leases[seg[1]] ?? [])
        break
      case 'firewall': return firewallRoute(seg, m, body, req)
      case 'backups': return backupsRoute(seg, m, body)
      case 'backup-targets': return ok(db.targets)
      case 'backup-policies':
        return ok(db.bpol)
      case 'doctor': return ok({ time: new Date().toISOString(), host: 'bsd01.example.net', status: 'fail', checks: [
        { name: 'zfs.pool', status: 'ok', message: 'pool tank is ONLINE' },
        { name: 'kernel.module.if_bridge', status: 'ok', message: 'loaded' },
        { name: 'kernel.module.pf', status: 'ok', message: 'loaded' },
        { name: 'pf.anchor.mail', status: 'fail', message: 'anchor jailops/mail is not loaded', hint: 'Fix the syntax error at line 14 of the policy mail-edge, then run jailops reconcile.', detail: { policy: 'mail-edge', line: '14' } },
        { name: 'linuxulator', status: 'warn', message: 'linprocfs is not mounted', hint: 'Add linprocfs to /etc/fstab.' },
        { name: 'jail.kernel.vimage', status: 'unknown', message: 'could not be determined on this platform' },
        { name: 'apps.access', status: 'ok', message: '2 grant(s); every firewall rule and pg_hba.conf line is in place' },
        { name: 'jails.rate_limits', status: 'info', message: '1 jail(s) set a rate limit, which is not enforced yet: web/web', hint: 'the limits are stored and reported; traffic is not shaped until a later version wires a shaper' },
      ] })
      case 'restores': return restore(body)
      case 'remotes': return ok(db.remotes)
      case 'operations':
        if (seg.length === 1) return ok(db.ops.map(view))
        {
          const o = db.ops.find((x) => x.id === seg[1])
          if (!o) return fail(404, { code: 'not_found', message: `Operation ${seg[1]} not found` })
          if (m === 'DELETE') {
            if (!o.may_cancel) return fail(403, { code: 'forbidden', message: 'This operation cannot be cancelled.' })
            o.status = 'cancelled'
            o.done = true
            return ok({})
          }
          return ok(view(o))
        }
      case 'audit': return ok(db.audit)
      case 'tokens':
        if (m === 'GET') return ok(db.tokens)
        if (m === 'POST') {
          if (db.tokens.some((t) => t.metadata.name === body.metadata?.name)) return fail(409, { code: 'already_exists', message: `Token ${body.metadata.name} already exists` })
          const t: Token = { metadata: { ...body.metadata, scope: 'fleet', created_at: new Date().toISOString() }, spec: body.spec }
          db.tokens.push(t)
          emit('token-created', `/api/v1/tokens/${t.metadata.name}`)
          return ok({ ...t, secret: 'jot_' + Array.from({ length: 40 }, () => 'abcdefghijklmnopqrstuvwxyz0123456789'[Math.floor(Math.random() * 36)]).join('') })
        }
        if (m === 'DELETE') {
          db.tokens = db.tokens.filter((t) => t.metadata.name !== seg[1])
          return ok({})
        }
        break
      case 'users':
        if (m === 'POST' && seg.length === 1) {
          if (db.users.some((u) => u.metadata.name === body.metadata?.name)) return fail(409, { code: 'already_exists', message: `User ${body.metadata.name} already exists` })
          const u = { metadata: { ...body.metadata, scope: 'fleet', created_at: new Date().toISOString() }, spec: body.spec }
          db.users.push(u)
          return ok({ user: u, ...(body.password ? {} : { password: 'demo-generated-passphrase' }) })
        }
        if (seg.length === 2 && seg[1] === 'password') return ok(body.password ? {} : { password: 'demo-generated-passphrase' })
        if (seg.length === 2 && m === 'PATCH') {
          const u = db.users.find((x) => x.metadata.name === seg[1])
          if (!u) return fail(404, { code: 'not_found', message: `No such user ${seg[1]}` })
          u.spec = { ...u.spec, ...body.spec }
          return ok(u)
        }
        if (seg.length === 2 && m === 'DELETE') {
          db.users = db.users.filter((x) => x.metadata.name !== seg[1])
          return ok({})
        }
        return ok(db.users)
      case 'certificates':
        if (m === 'DELETE') {
          db.certs = db.certs.filter((c) => c.metadata.name !== seg[1])
          return ok({})
        }
        return ok(db.certs)
    }
    return fail(404, { code: 'not_found', message: `No such route: ${m} ${path}` })
  }

  function jailsRoute(seg: string[], m: string, body: Record<string, any>, inProject: (j: Jail) => boolean, url: URL): RawResponse {
    if (seg.length === 1 && m === 'POST') {
      const name = String(body.metadata?.name ?? '')
      if (db.jails.some((x) => x.metadata.name === name && inProject(x))) return fail(409, { code: 'already_exists', message: `Jail ${name} already exists.` })
      const project = url.searchParams.get('project') ?? ''
      const jail: Jail = { metadata: { name, scope: 'host', generation: 1, created_at: new Date().toISOString(), ...(project ? { project } : {}) }, spec: body.spec, status: { power: 'stopped', addresses: [], conditions: [{ type: 'Reconciled', status: 'True' }] } }
      return startOp(`Create jail ${name}`, { jails: [`/api/v1/jails/${name}`] }, 800, () => { db.jails.push(jail) }, undefined, false)
    }
    if (seg.length === 1) return ok(db.jails.filter(inProject))
    const j = jailOr404(seg[1])
    if (!j || !inProject(j)) return fail(404, { code: 'not_found', message: `Jail ${seg[1]} not found` })
    const name = j.metadata.name
    const src = `/api/v1/jails/${name}`
    if (seg.length === 2 && url.searchParams.get('expanded') === 'true') {
      const profs = [...db.profiles.project, ...db.profiles.host].filter((p) => (j.spec.profiles ?? []).includes(p.metadata.name))
      let merged = JSON.parse(JSON.stringify(j.spec)) as Jail['spec']
      const own = flatten(j.spec)
      const src = expandedSources(j.spec, profs)
      const sources: Record<string, string> = {}
      for (const [k, v] of Object.entries(src)) {
        sources[k] = v.source === 'jail' ? 'jail' : 'profile:' + v.source
        const f = lookupKey(k)
        if (f && !(k in own)) merged = setValue(merged, f.def, f.dev, v.value)
      }
      return ok({ jail: { ...j, spec: merged }, sources })
    }
    if (seg.length === 3 && seg[2] === 'config' && m === 'POST') {
      const set = (body.set ?? {}) as Record<string, string>
      const unset = (body.unset ?? []) as string[]
      let spec = JSON.parse(JSON.stringify(j.spec)) as Jail['spec']
      const restart: string[] = []
      for (const [k, v] of Object.entries(set)) {
        const f = lookupKey(k)
        if (!f) return fail(400, { code: 'invalid', message: `unknown configuration key "${k}"`, hint: 'jailops config keys lists them', detail: { key: k } })
        if (f.def.type === 'int' && !/^-?\d+$/.test(v)) return fail(400, { code: 'invalid', message: `${k}: "${v}" must be a number`, detail: { key: k } })
        if (k.endsWith('.mtu') && (Number(v) < 576 || Number(v) > 9216)) return fail(400, { code: 'invalid', message: `${k}: "${v}" must be a number between 576 and 9216`, detail: { key: k } })
        if (k === 'shell' && v && v !== '/bin/sh' && v !== '/usr/local/bin/bash') return fail(400, { code: 'invalid', message: `shell: ${v} does not exist in the jail`, detail: { key: k } })
        if (f.def.restart_required && flatten(j.spec)[k] !== v) restart.push(k)
        spec = setValue(spec, f.def, f.dev, v)
      }
      for (const k of unset) {
        const f = lookupKey(k)
        if (!f) return fail(400, { code: 'invalid', message: `unknown configuration key "${k}"`, detail: { key: k } })
        if (f.def.restart_required && flatten(j.spec)[k] !== undefined) restart.push(k)
        spec = setValue(spec, f.def, f.dev, '')
      }
      j.spec = spec
      j.metadata.generation = (j.metadata.generation ?? 0) + 1
      emit('jail-updated', src)
      return startOp(`Configure ${name}`, { jails: [src] }, 300, () => undefined, { ...j, restart_required: restart.sort(), restarted: false }, false)
    }
    if (seg.length === 2) return ok(j, etagOf(j.metadata.generation))
    switch (seg[2]) {
      case 'snapshots':
        if (m === 'GET') return ok(db.snaps[name] ?? [])
        return startOp(`Snapshot ${name}`, { jails: [src] }, 1200, () => {
          const n = `manual-${Date.now().toString(36)}`
          ;(db.snaps[name] ??= []).push({ metadata: { name: n, scope: 'host', created_at: new Date().toISOString() }, spec: { jail: name, comment: body.spec?.comment }, status: { written: 0 } })
          emit('jail-snapshot-created', src)
        }, undefined, false)
      case 'state': {
        const a = body.action as string
        if (a === 'start' && j.status.power === 'running') return fail(409, { code: 'busy', message: `Jail ${name} is already running.` })
        if (a === 'start' && name === 'mail') return fail(500, { code: 'internal', message: 'jail: mail: exec.prestart failed', hint: 'Run jailops doctor on the host, then retry.', detail: { exit_code: '1' } })
        const to: PowerState = a === 'stop' ? 'stopped' : 'running'
        j.status.power = a === 'stop' ? 'stopping' : 'starting'
        pending.push({ jail: name, to, at: Date.now() + 1500 / speed })
        return startOp(`${a} ${name}`, { jails: [src] }, 1500, () => emit(`jail-${a === 'stop' ? 'stopped' : 'started'}`, src), undefined, true)
      }
      case 'rollback': {
        const r = body as JailRollback
        if ((r.scope === 'data' || r.scope === 'all') && !r.confirm_data_loss) return fail(400, { code: 'data_loss_unconfirmed', message: 'This rollback discards data and was not confirmed.', hint: 'Set confirm_data_loss after showing the bytes to the operator.' })
        return startOp(`Rollback ${name} (${r.scope ?? 'root'})`, { jails: [src] }, 2200, () => {
          if (r.scope === 'data' || r.scope === 'all') (db.snaps[name] ?? []).forEach((s) => (s.status.written = 0))
          if (r.scope !== 'data') j.status.generation = 'freebsd-15#g3'
          emit('jail-rolled-back', src)
        })
      }
      case 'upgrade': {
        const u = body as JailUpgrade
        const target = u.generation || 'freebsd-15#g5'
        const preview = {
          from_generation: j.status.generation ?? '', to_generation: target,
          steps: ['Preflight: no dependent clones, replication idle', 'Snapshot all datasets (rollback point)', 'Stop the jail', 'Clone the target generation to root.new', 'Merge /etc with etcupdate', 'Swap root and root.new', 'Start and run the health check'],
          etc_conflicts: name === 'web' ? ['/etc/ssl/openssl.cnf'] : [], warnings: j.status.power === 'running' ? ['The jail will be stopped for about 20 seconds.'] : [],
        }
        if (u.dry_run) return ok(preview)
        return startOp(`Upgrade ${name} to ${target}`, { jails: [src] }, 3500, () => {
          j.status.generation = target
          emit('jail-upgraded', src)
        })
      }
    }
    return fail(404, { code: 'not_found', message: 'No such route' })
  }

  function firewallRoute(seg: string[], m: string, body: Record<string, any>, req: RawRequest): RawResponse {
    const isSet = seg[1] === 'address-sets'
    const list = (isSet ? db.sets : db.policies) as Array<FirewallPolicy | AddressSet>
    if (seg.length === 2) return ok(list)
    const item = list.find((x) => x.metadata.name === seg[2])
    if (!item) return fail(404, { code: 'not_found', message: `${seg[2]} not found` })
    if (m === 'GET') return ok(item, etagOf(item.metadata.generation))
    if (req.headers?.['If-Match'] && req.headers['If-Match'] !== etagOf(item.metadata.generation)) return fail(412, { code: 'conflict', message: 'The object changed since you loaded it.', hint: 'Reload and apply your change again.' })
    if (!isSet) {
      const rules = ((body as FirewallPolicy).spec.rules ?? [])
      for (let i = 0; i < rules.length; i++) {
        const r = rules[i]
        if (r.redirect && r.direction !== 'in') return fail(400, { code: 'invalid', message: `rules[${i}]: a redirect is allowed on inbound rules only`, detail: { [`rules[${i}].redirect`]: 'redirect is allowed on inbound rules only' } })
        for (const a of r.from ?? []) if (a.startsWith('@') && !db.sets.some((s) => s.metadata.name === a.slice(1))) return fail(400, { code: 'invalid', message: `rules[${i}]: unknown address set ${a}`, hint: 'Create the address set first.', detail: { [`rules[${i}].from`]: `unknown address set ${a}` } })
      }
    }
    return startOp(`Save ${seg[2]}`, { firewall: [`/api/v1/firewall/${seg[1]}/${seg[2]}`] }, 700, () => {
      item.spec = (body as FirewallPolicy).spec as never
      item.metadata.generation = (item.metadata.generation ?? 0) + 1
      emit('firewall-updated', `/api/v1/firewall/${seg[1]}/${seg[2]}`)
    }, undefined, false)
  }

  function backupsRoute(seg: string[], m: string, body: Record<string, any> = {}): RawResponse {
    if (seg[1] === 'run' && m === 'POST') {
      const pol = db.bpol.find((p) => p.metadata.name === body.policy)
      if (!pol) return fail(404, { code: 'not_found', message: `Backup policy ${body.policy} not found` })
      const run = { id: 'run-' + ++seq, kind: 'backup', policy: pol.metadata.name, jail: 'web', started: new Date().toISOString(), finished: new Date().toISOString(), ok: true }
      return startOp(`Run policy ${pol.metadata.name}`, {}, 2000, () => emit('backup-run', '/api/v1/backups/run'), [run], true)
    }
    if (seg.length === 1) return ok(db.backups)
    const b = db.backups.find((x) => x.metadata.name === seg[1])
    if (!b) return fail(404, { code: 'not_found', message: `Backup ${seg[1]} not found` })
    if (seg[2] === 'verify') return startOp(`Verify ${b.spec.snapshot}`, {}, 1500, () => { b.status.verified = true; b.status.failed = false }, { id: 'run-' + ++seq, kind: 'drill', jail: b.spec.jail, depth: body.depth ?? 'deep', ok: true, started: new Date().toISOString(), finished: new Date().toISOString() }, true)
    if (m === 'DELETE') {
      if (b.status.protected) return fail(409, { code: 'busy', message: 'The snapshot is protected by retention.', hint: 'Change the retention policy on the backup node first.' })
      return startOp(`Request deletion of ${b.spec.snapshot}`, {}, 600, () => (b.status.pending_deletion = new Date(Date.now() + 2 * 86400_000).toISOString()), undefined, false)
    }
    return ok(b)
  }

  function restore(body: Record<string, any>): RawResponse {
    const b: Backup | undefined = db.backups.find((x) => x.metadata.name === body.backup)
    if (!b) return fail(404, { code: 'not_found', message: `Backup ${body.backup} not found` })
    const map = (body.network_map ?? {}) as Record<string, string>
    const missing: string[] = []
    const changes: string[] = []
    const errors: string[] = []
    for (const a of b.spec.bundle.spec.network ?? []) {
      const target = map[a.network] ?? a.network
      if (!db.networks.some((n) => n.metadata.name === target)) missing.push(`network ${target}`)
      else if (a.ipv4 && a.ipv4 !== 'auto' && a.ipv4 !== 'none' && !body.keep_ip) changes.push(`allocate a new address on ${target} (was ${a.ipv4})`)
      else if (body.keep_ip && a.ipv4?.startsWith('10.99.')) errors.push(`address ${a.ipv4} is outside every range of network ${target}`)
    }
    const target = body.as || b.spec.jail
    if (db.jails.some((j) => j.metadata.name === target) && !body.replace) errors.push(`jail ${target} already exists; choose another name or allow replacing it`)
    changes.push(`create jail ${target} from ${b.spec.snapshot}`, `use image ${b.spec.bundle.image_generation}`)
    const report: RestorePreflight = { ok: !missing.length && !errors.length, missing, changes, errors }
    if (body.dry_run) return ok(report)
    if (!report.ok) return fail(409, { code: 'invalid', message: 'The restore preflight failed.', hint: 'Run the preflight and fix what it lists.' })
    return startOp(`Restore ${target}`, { jails: [`/api/v1/jails/${target}`] }, 4000, () => {
      emit('jail-restored', `/api/v1/jails/${target}`)
    })
  }


  /** A network belongs to one project: the message the server gives when another one holds it. */
  function heldNetwork(spec: { networks?: Array<{ network: string }> }, self: string): string | undefined {
    for (const g of spec.networks ?? []) {
      const other = db.projects.find((x) => x.metadata.name !== self && (x.spec.networks ?? []).some((h) => h.network === g.network))
      if (other) return `network ${g.network} is already granted to project ${other.metadata.name}`
    }
    return undefined
  }
  function hostRoute(seg: string[], m: string, body: Record<string, any>): RawResponse | undefined {
    if (seg[1] === 'identity' && m === 'GET') return ok(dh.identity(db.host))
    if (seg[1] === 'identity' && m === 'PUT') {
      const spec = body.spec ?? {}
      if (spec.hostname && !validDNSName(spec.hostname)) return fail(400, { code: 'invalid', message: `hostname "${spec.hostname}" is not a valid host name` })
      const r = dh.applyIdentity(db.host, spec, !!body.dry_run)
      if (!r.dry_run && r.changed) emit('host-identity-updated', '/api/v1/host/identity')
      return ok(r)
    }
    if (seg[1] === 'snapshots' && seg.length === 2 && m === 'GET') return ok(dh.snapshots(db.host))
    if (seg[1] === 'snapshots' && seg[3] === 'restore' && m === 'POST') {
      const r = dh.restoreSnapshot(db.host, seg[2], !!body.dry_run)
      return r ? ok(r) : fail(404, { code: 'not_found', message: `snapshot ${seg[2]} not found` })
    }
    return undefined
  }

  function wireguardRoute(seg: string[], m: string, body: Record<string, any>): RawResponse {
    const wg = (n: string) => db.wireguard.find((w) => w.metadata.name === n)
    const missing = (n: string) => fail(404, { code: 'not_found', message: `wireguard "${n}" not found` })
    const touch = (w: import('@jailops/shared/api/types').WireGuard) => { w.metadata.generation = (w.metadata.generation ?? 0) + 1; w.metadata.updated_at = new Date().toISOString(); emit('wireguard-updated', `/api/v1/wireguard/${w.metadata.name}`) }
    if (seg.length === 1 && m === 'GET') return ok(db.wireguard.map(dw.observe))
    if (seg.length === 1 && m === 'POST') {
      const name = String(body.metadata?.name ?? '')
      if (!/^[a-z][a-z0-9-]{0,62}$/.test(name)) return fail(400, { code: 'invalid', message: `name "${name}" must match ^[a-z][a-z0-9-]{0,62}$` })
      if (wg(name)) return fail(409, { code: 'already_exists', message: `wireguard "${name}" already exists` })
      const port = body.spec?.listen_port ?? 51820
      if (db.wireguard.some((w) => w.spec.listen_port === port)) return fail(409, { code: 'already_exists', message: `listen port ${port} is used by another tunnel` })
      const addr: string[] = body.spec?.address ?? []
      const w = {
        metadata: { name, scope: 'fleet' as const, generation: 1, created_at: new Date().toISOString() },
        spec: { ...body.spec, interface: `wg-${name}`, listen_port: port, allowed_ips: addr.map((a) => (a.includes(':') ? a.replace(/::[0-9a-f]*\//, '::/') : a.replace(/\.\d+\/(\d+)$/, '.0/$1'))), endpoint: body.spec?.endpoint || `vpn.example.com:${port}`, peers: [] },
        status: { public_key: dw.demoKey(), interface: `wg-${name}`, up: true, listen_port: port, firewall_rule: `pass in on $ext_if proto udp to port ${port}`, peers: [], conditions: [] },
      }
      db.wireguard.push(w)
      emit('wireguard-created', `/api/v1/wireguard/${name}`)
      return { ...ok(dw.observe(w)), status: 201 }
    }
    if (seg[1] === 'join' && m === 'POST') {
      const w = dw.fromToken(String(body.token ?? ''), body.name)
      if (!w) return fail(400, { code: 'invalid', message: 'not a WireGuard join token', hint: 'copy the whole token that `jailops wireguard peer add` printed; it starts with jwg1.' })
      if (wg(w.metadata.name)) return fail(409, { code: 'already_exists', message: `wireguard "${w.metadata.name}" already exists` })
      db.wireguard.push(w)
      emit('wireguard-created', `/api/v1/wireguard/${w.metadata.name}`)
      return { ...ok(dw.observe(w)), status: 201 }
    }
    const w = wg(seg[1])
    if (!w) return missing(seg[1])
    if (seg.length === 2 && m === 'GET') return ok(dw.observe(w))
    if (seg.length === 2 && m === 'DELETE') { db.wireguard = db.wireguard.filter((x) => x !== w); emit('wireguard-deleted', `/api/v1/wireguard/${w.metadata.name}`); return ok({}) }
    if (seg[2] === 'peers' && m === 'POST' && seg.length === 3) {
      if (w.spec.mode !== 'server') return fail(400, { code: 'invalid', message: `wireguard "${w.metadata.name}" is a client; peers are added on the server` })
      const name = String(body.name ?? '')
      if (!/^[a-z][a-z0-9-]{0,62}$/.test(name)) return fail(400, { code: 'invalid', message: `name "${name}" must match ^[a-z][a-z0-9-]{0,62}$` })
      if ((w.spec.peers ?? []).some((p) => p.name === name)) return fail(409, { code: 'already_exists', message: `peer "${name}" of wireguard "${w.metadata.name}" already exists`, hint: 'remove it first to issue a new token' })
      const out = dw.enroll(w, name, body.address)
      touch(w)
      return { ...ok(out), status: 201 }
    }
    if (seg[2] === 'peers' && m === 'DELETE' && seg[3]) {
      if (!(w.spec.peers ?? []).some((p) => p.name === seg[3])) return fail(404, { code: 'not_found', message: `peer "${seg[3]}" not found` })
      w.spec.peers = (w.spec.peers ?? []).filter((p) => p.name !== seg[3])
      touch(w)
      return ok(dw.observe(w))
    }
    if ((seg[2] === 'up' || seg[2] === 'down') && m === 'POST') { w.spec.down = seg[2] === 'down'; touch(w); return ok(dw.observe(w)) }
    if (seg[2] === 'rotate-keys' && m === 'POST') {
      w.status.public_key = dw.demoKey()
      return ok({ public_key: w.status.public_key, warning: w.spec.mode === 'server' ? 'every peer must be given the new public key: issue new tokens (jailops wireguard peer remove, then peer add)' : 'the server of this client must be given the new public key: remove the peer there and enroll it again' })
    }
    return fail(404, { code: 'not_found', message: `No such route: ${m} /wireguard/${seg.slice(1).join('/')}` })
  }
  function extraRoutes(seg: string[], m: string, body: Record<string, any>, url: URL, req: RawRequest, project: string): RawResponse | undefined {
    const name = seg[1]
    switch (seg[0]) {
      case 'apps': return appsRoute(seg, m, body, project)
      case 'host': return hostRoute(seg, m, body)
      case 'wireguard': return wireguardRoute(seg, m, body)
      case 'projects': {
        if (seg.length === 1 && m === 'GET') return ok(db.projects)
        if (seg.length === 1 && m === 'POST') {
          if (db.projects.some((p) => p.metadata.name === body.metadata?.name)) return fail(409, { code: 'already_exists', message: `Project ${body.metadata.name} already exists.` })
          const spec = { ...body.spec }
          const held = heldNetwork(spec, '')
          if (held) return fail(409, { code: 'conflict', message: held })
          if (spec.new_network) { delete spec.new_network; spec.networks = [...(spec.networks ?? []), { network: `br-${body.metadata.name}` }] }
          const created = d2.projects({})[0]
          created.metadata = { name: body.metadata.name, scope: 'host', generation: 1, created_at: new Date().toISOString() }
          created.spec = spec
          created.status = { jails: 0, cpu: 0, memory_used: 0, disk_used: 0, addresses: 0 }
          db.projects.push(created)
          emit('project-created', `/api/v1/projects/${body.metadata.name}`)
          return startOp(`Create project ${body.metadata.name}`, { projects: [] }, 500, () => undefined, undefined, false)
        }
        const p = db.projects.find((x) => x.metadata.name === name)
        if (!p) return fail(404, { code: 'not_found', message: `Project ${name} not found` })
        if (m === 'GET') return url.searchParams.get('host') === 'all' ? ok(d2.aggregate(p)) : ok(p, etagOf(p.metadata.generation))
        if (m === 'PUT') { const held = heldNetwork(body.spec, name); if (held) return fail(409, { code: 'conflict', message: held }); p.spec = body.spec; p.metadata.generation = (p.metadata.generation ?? 0) + 1; return startOp(`Save project ${name}`, {}, 400, () => undefined, undefined, false) }
        if (m === 'DELETE') {
          if ((countByProject()[name] ?? 0) > 0) return fail(409, { code: 'busy', message: `Project ${name} still holds jails.`, hint: 'Delete or move its jails first.' })
          return startOp(`Delete project ${name}`, {}, 400, () => { db.projects = db.projects.filter((x) => x !== p) }, undefined, false)
        }
        return undefined
      }
      case 'storage': {
        if (m === 'GET') return ok(db.storage)
        if (m === 'POST') {
          if (db.storage.some((x) => x.metadata.name === body.metadata?.name)) return fail(409, { code: 'already_exists', message: 'Storage exists.' })
          return startOp(`Create storage ${body.metadata.name}`, {}, 600, () => db.storage.push({ metadata: { name: body.metadata.name, scope: 'host', created_at: new Date().toISOString() }, spec: body.spec, status: { pool: String(body.spec.dataset).split('/')[0], health: 'ONLINE', size: 500 * GiB, used: 1 * GiB, available: 499 * GiB } }), undefined, false)
        }
        if (m === 'DELETE') {
          if (db.projects.some((p) => (p.spec.storage ?? []).some((g) => g.storage === name))) return fail(409, { code: 'busy', message: `Storage ${name} is granted to a project.`, hint: 'Remove the grant first.' })
          return startOp(`Delete storage ${name}`, {}, 400, () => { db.storage = db.storage.filter((x) => x.metadata.name !== name) }, undefined, false)
        }
        return undefined
      }
      case 'trust': {
        if (seg[1] === 'tokens' && m === 'POST') {
          if (!body.global && !(body.projects ?? []).length) return fail(400, { code: 'invalid', message: 'An identity needs projects or global.', hint: 'Choose at least one project, or set global.' })
          const exp = new Date(Date.now() + (body.ttl_seconds || 3600) * 1000).toISOString()
          return ok({ name: body.name, token: 'jbc_' + btoa(JSON.stringify({ v: 1, name: body.name, fp: 'ab12' })).replace(/=/g, '') + '.' + Array.from({ length: 24 }, () => 'abcdef0123456789'[Math.floor(Math.random() * 16)]).join(''), expires_at: exp }, undefined)
        }
        return undefined
      }
      case 'sessions': {
        if (m === 'GET') return ok(db.sessions)
        if (m === 'DELETE') { db.sessions = db.sessions.filter((x) => x.id !== name); return ok(null) }
        return undefined
      }
      case 'certificates': {
        const c = db.certs2.find((x) => x.metadata.name === name)
        if (m === 'GET' && seg.length === 1) return ok(db.certs2)
        if (!c) return fail(404, { code: 'not_found', message: 'Certificate not found' })
        if (m === 'GET') return ok(c, etagOf(c.metadata.generation))
        if (m === 'PUT') {
          if (!body.spec.global && !(body.spec.projects ?? []).length) return fail(400, { code: 'invalid', message: 'An identity needs projects or global.' })
          c.spec = body.spec
          return startOp(`Save ${name}`, {}, 300, () => undefined, undefined, false)
        }
        if (m === 'DELETE') return startOp(`Revoke ${name}`, {}, 300, () => { db.certs2 = db.certs2.filter((x) => x !== c) }, undefined, false)
        return undefined
      }
      case 'profiles': {
        const list = project === 'host' ? db.profiles.host : db.profiles.project
        if (m === 'GET') return ok(list)
        if (m === 'POST') {
          if (list.some((p) => p.metadata.name === body.metadata?.name)) return fail(409, { code: 'already_exists', message: 'Profile exists.' })
          list.push({ ...body, metadata: { ...body.metadata, project } } as never)
          return startOp(`Create profile ${body.metadata.name}`, {}, 300, () => undefined, undefined, false)
        }
        const p = list.find((x) => x.metadata.name === name)
        if (!p) return fail(404, { code: 'not_found', message: 'Profile not found' })
        if (m === 'PUT') { p.spec = body.spec; return startOp(`Save profile ${name}`, {}, 300, () => undefined, undefined, false) }
        if (m === 'DELETE') {
          if (db.jails.some((j) => (j.spec.profiles ?? []).includes(name))) return fail(409, { code: 'busy', message: `Profile ${name} is used by a jail.` })
          const i = list.indexOf(p); list.splice(i, 1)
          return startOp(`Delete profile ${name}`, {}, 300, () => undefined, undefined, false)
        }
        return undefined
      }
      case 'updates': {
        if (seg.length === 1 && m === 'GET') return ok(db.updates.filter((u) => u.project === project))
        if (seg[1] === 'check' && m === 'POST') {
          const names: string[] = body.all ? db.updates.filter((u) => u.project === project).map((u) => u.jail) : body.jails ?? []
          return startOp('Check updates', {}, 800, () => { for (const u of db.updates) if (names.includes(u.jail)) u.checked_at = new Date().toISOString() }, db.updates.filter((u) => names.includes(u.jail)), false)
        }
        if (seg[1] === 'apply' && m === 'POST') {
          const names: string[] = body.all ? db.updates.filter((u) => u.project === project && u.available).map((u) => u.jail) : body.jails ?? []
          const targets = db.updates.filter((u) => names.includes(u.jail))
          if (!targets.length || targets.every((u) => !u.available)) return fail(400, { code: 'invalid', message: 'There is nothing to apply.' })
          const recs: import('@jailops/shared/api/types').UpdateRecord[] = targets.filter((u) => u.available).map((u) => ({ id: 'u' + ++seq, jail: u.jail, project, kind: u.os ? 'os' : 'packages', result: 'ok', started: new Date().toISOString(), finished: new Date().toISOString(), snapshot: 'pre-update-' + Date.now().toString(36), packages: u.packages }))
          return startOp(`Update ${names.join(', ')}`, {}, 2500, () => {
            for (const r of recs) { (db.history[r.jail] ??= []).unshift(r); const u = db.updates.find((x) => x.jail === r.jail)!; u.available = false; u.packages = undefined; u.os = undefined }
            emit('update-applied', '/api/v1/updates')
          }, recs)
        }
        return undefined
      }
      case 'backup': {
        if (seg[1] !== 'key') return undefined
        if (seg[2] === 'escrow') { db.key.status = 'escrowed'; return ok({ ...db.key }) }
        if (m === 'GET') return ok({ ...db.key })
        if (db.key.status !== 'none') return fail(409, { code: 'already_exists', message: 'The host already has a backup key.' })
        db.key.status = (body.recipients ?? []).length ? 'escrowed' : 'present'
        return ok({ domain: db.key.domain, recovery_key: 'JORK-' + Array.from({ length: 6 }, () => Math.random().toString(36).slice(2, 7).toUpperCase()).join('-'), escrowed: db.key.status === 'escrowed' }, undefined)
      }
      case 'jails': {
        const j = db.jails.find((x) => x.metadata.name === name && (x.metadata.project ?? '') === project)
        if (!j || !seg[2]) return undefined
        const src = `/api/v1/jails/${name}`
        if (seg[2] === 'updates' && m === 'GET') return ok(db.history[name] ?? [])
        if (seg[2] === 'access') {
          if (!j.metadata.labels?.[da.LABEL]) return fail(400, { code: 'invalid', message: `jail "${name}" was not installed from the catalog (it has no label jailops.app)` })
          if (seg.length === 3 && m === 'GET') return ok(access.get(j))
          const r = seg.length === 3 && m === 'POST' ? access.allow(j, body as never) : seg[3] === 'deny' && m === 'POST' ? access.deny(j, body as never) : undefined
          if (!r) return undefined
          if ('ok' in r) return ok(r.ok)
          return fail(r.status, { code: r.code, message: r.message, hint: r.hint } as ApiErrorBody)
        }
        if (seg[2] === 'logs' && m === 'GET') {
          const app = j.metadata.labels?.[da.LABEL] ?? ''
          // A stopped jail's root is not mounted: the server answers busy and says how to mount it.
          if (j.status.power === 'stopped') return fail(409, { code: 'busy', message: `jail "${name}" is not mounted`, hint: `start the jail, or mount its root dataset on the host: zfs mount tank/jailops/${project}/${name}/root` })
          if (seg.length === 3) return ok(logs.list(name, app))
          const file = seg.slice(3).join('/')
          const r = logs.read(name, app, file, url.searchParams)
          if (r === 'missing') return fail(404, { code: 'not_found', message: `jail "${name}" has no log file "${file}"`, hint: `list them with GET /api/v1/jails/${name}/logs` })
          if (r === 'compressed') return fail(400, { code: 'invalid', message: `${file} is not readable (compressed)`, hint: 'read the plain rotated copies, or the file from inside the jail' })
          return ok(r)
        }
        if (seg[2] === 'provision' && m === 'POST') {
          if (j.status.provisioned_at && !body.again) return fail(409, { code: 'already_exists', message: 'Provisioning has run before.', hint: 'Use again to run it again.' })
          return startOp(`Provision ${name}`, { jails: [src] }, 1500, () => { j.status.provisioned_at = new Date().toISOString() }, { jail: name, provisioned_at: new Date().toISOString(), steps: ['packages', 'files', 'services'] })
        }
        if (seg[2] === 'console') {
          if (m === 'POST' && seg.length === 3) {
            if (j.status.power !== 'running') return fail(409, { code: 'busy', message: `Jail ${name} is not running.` })
            const sh = (j.spec as unknown as { shell?: string }).shell
            if (sh && sh !== '/bin/sh' && sh !== '/usr/local/bin/bash') return fail(404, { code: 'not_found', message: `The shell ${sh} does not exist in the jail.`, hint: 'Set a shell that exists, or clear the setting to use /bin/sh.' })
            const id = 'x' + ++seq
            shells.set(id, { h: undefined as never, line: '', jail: name })
            return ok({ id }, undefined)
          }
          const sess = shells.get(seg[3])
          if (!sess) return fail(404, { code: 'not_found', message: 'No such exec session.' })
          if (seg[4] === 'input') { shellInput(seg[3], String(body.data ?? '')); return ok(null) }
          if (seg[4] === 'resize') return ok(null)
          if (m === 'DELETE') { shells.delete(seg[3]); return ok(null) }
        }
        return undefined
      }
    }
    void req
    return undefined
  }

  function appsRoute(seg: string[], m: string, body: Record<string, any>, project: string): RawResponse {
    const name = seg[1]
    if (seg.length === 1 && m === 'GET') return ok(da.catalog())
    const det = name ? da.detail(name) : undefined
    if (det === 'invalid') return fail(400, { code: 'invalid', message: `app ${name} is invalid: ${da.invalidApp.error}`, hint: 'fix the file in the catalog directory, or reinstall the jailops-apps package', detail: { file: `/usr/local/share/jailops/apps/${name}.yaml` } })
    if (!det) return fail(404, { code: 'not_found', message: `app ${name} is not in the catalog`, hint: 'jailops app list shows the catalog' })
    if (seg.length === 2 && m === 'GET') return ok(det)
    if (seg[2] !== 'install' || m !== 'POST') return fail(404, { code: 'not_found', message: 'No such route' })
    if (!project) return fail(400, { code: 'invalid', message: 'a project is required', hint: "name one with --project NAME (list them with 'jailops project list')" })
    const p = db.projects.find((x) => x.metadata.name === project)
    if (!p) return fail(404, { code: 'not_found', message: `project ${project} not found` })
    if (body.project && body.project !== project) return fail(400, { code: 'invalid', message: `the body names project ${body.project}, the request ${project}` })
    const jail = String(body.jail || name)
    if (!/^[a-z][a-z0-9-]{0,62}$/.test(jail)) return fail(400, { code: 'invalid', message: `jail name "${jail}" is not a name`, hint: 'lower-case letters, digits and dashes, starting with a letter' })
    if (db.jails.some((x) => x.metadata.name === jail && (x.metadata.project ?? '') === project)) return fail(409, { code: 'already_exists', message: `jail ${jail} exists in project ${project}`, hint: 'name the jail with --name, or delete the jail first' })
    const nets = (p.spec.networks ?? []).map((g) => g.network)
    const net = String(body.network || (nets.length === 1 ? nets[0] : ''))
    if (!net) return fail(400, { code: 'invalid', message: `no network named and project ${project} is granted ${nets.length ? 'several' : 'none'}`, hint: `jailops project show ${project} lists the grants` })
    if (!nets.includes(net)) return fail(403, { code: 'forbidden', message: `network ${net} is not granted to project ${project}`, hint: `jailops project show ${project} lists the grants` })
    const stoGrants = (p.spec.storage ?? []).map((g) => g.storage)
    const stos = stoGrants.includes('*') ? db.storage.map((x) => x.metadata.name) : stoGrants
    const sto = String(body.storage || (stos.length === 1 ? stos[0] : ''))
    if (!sto) return fail(400, { code: 'invalid', message: `no storage named and project ${project} is granted ${stos.length ? 'several' : 'none'}`, hint: `jailops project show ${project} lists the grants` })
    if (!stos.includes(sto)) return fail(403, { code: 'forbidden', message: `storage ${sto} is not granted to project ${project}`, hint: `jailops project show ${project} lists the grants` })
    const a = det.definition
    const image = String(body.image || 'freebsd-15@stable')
    const steps = da.installSteps(a, jail, project, image)
    const subnet = db.networks.find((x) => x.metadata.name === net)?.spec.ipv4?.subnet ?? '10.20.0.0/24'
    const addr = subnet.replace(/\.\d+\/\d+$/, '') + '.' + (40 + db.jails.length)
    const limits = { memory: body.limits?.memory ?? a.resources?.memory, disk: body.limits?.disk ?? a.resources?.disk, cpu: Number(body.limits?.cpu ?? a.resources?.cpu ?? 1) }
    const version = a.packages[0]?.version ?? ''
    const src = `/api/v1/jails/${jail}`
    const j: Jail = {
      metadata: { name: jail, scope: 'host', project, generation: 1, created_at: new Date().toISOString(), labels: { [da.LABEL]: a.name, [`${da.LABEL}.hash`]: det.hash, [`${da.LABEL}.version`]: version } },
      spec: { image, storage: sto, layout: 'thin', network: [{ network: net, ipv4: addr }], volumes: (a.data ?? []).map((x) => ({ name: volumeName(x.path), mount: x.path })), resources: limits, boot: { autostart: true } },
      status: { power: 'running', jid: 120 + db.jails.length, generation: 'freebsd-15#g4', addresses: [`${addr}/${subnet.split('/')[1] ?? '24'}`], started_at: new Date().toISOString(), conditions: [{ type: 'Reconciled', status: 'True' }] },
    }
    const result = {
      app: a.name, hash: det.hash, version, jail: j, addresses: j.status.addresses, ports: (a.ports ?? []).map((x) => ({ ...x, endpoint: `${addr}:${x.port}` })),
      data: a.data, config: a.config, logs: a.logs, notes: a.notes, snapshot: 'app-installed', steps,
    }
    if (body.dry_run) return startOp(`Install app ${a.name} as jail ${jail} (dry run)`, {}, 300, () => undefined, { ...result, jail: { metadata: { name: jail }, spec: { image }, status: { power: 'stopped' } }, version: undefined, addresses: undefined, ports: undefined, snapshot: undefined, dry_run: true }, false)
    // A jail name that starts with "fail" makes the demo fail at the health check and keep the jail, stopped.
    if (jail.startsWith('fail')) {
      const r = startOp(`Install app ${a.name} as jail ${jail}`, { jails: [src] }, 3500, () => {
        j.status = { power: 'stopped', addresses: [], conditions: [{ type: 'Reconciled', status: 'True' }] }
        db.jails.push(j)
        emit('app.install.failed', src)
      }, undefined, false)
      const op = db.ops[0]
      op.failure = {
        code: 'internal', message: `installing app ${a.name} as jail ${jail} failed at step health-check: no answer within ${a.health?.wait_seconds ?? 60} attempts`,
        hint: `the jail was left stopped for inspection: look inside it with \`jailops shell ${jail}\` after \`jailops start ${jail}\`, and delete it with \`jailops delete ${jail}\` before trying again`,
        detail: { jail, step: 'health-check', jail_kept: 'true' },
      } as ApiErrorBody
      return r
    }
    return startOp(`Install app ${a.name} as jail ${jail}`, { jails: [src] }, 5000, () => {
      db.jails.push(j)
      access.install(jail)
      db.snaps[jail] = [{ metadata: { name: 'app-installed', scope: 'host', created_at: new Date().toISOString() }, spec: { jail, comment: `installation of ${a.name}` }, status: { written: 0 } }]
      for (const ev of ['jail-created', 'jail-started', 'jail-snapshot-created', 'app.installed']) emit(ev, src)
    }, result, false)
  }

  const PROMPT = '\x1b[32mroot@jail\x1b[0m:\x1b[34m~\x1b[0m# '
  function shellOut(id: string, text: string): void {
    const s = shells.get(id)
    if (!s?.h) return
    s.h.onEvent({ type: 'logging', timestamp: new Date().toISOString(), metadata: { data: btoa(text) } } as ApiEvent)
  }
  function shellInput(id: string, data: string): void {
    const s = shells.get(id)
    if (!s) return
    for (const ch of data) {
      if (ch === '\r') {
        const cmd = s.line.trim()
        s.line = ''
        let out = ''
        if (cmd === 'hostname') out = `${s.jail}.example.net`
        else if (cmd === 'uname -a') out = 'FreeBSD ' + s.jail + ' 15.1-RELEASE FreeBSD 15.1-RELEASE amd64'
        else if (cmd === 'ls') out = '\x1b[34mbin\x1b[0m  \x1b[34metc\x1b[0m  \x1b[34musr\x1b[0m  \x1b[34mvar\x1b[0m'
        else if (cmd.startsWith('echo ')) out = cmd.slice(5)
        else if (cmd === 'exit') { shellOut(id, '\r\nlogout\r\n'); s.h.onEvent({ type: 'logging', timestamp: new Date().toISOString(), metadata: { exit: 0 } } as ApiEvent); return }
        else if (cmd) out = `sh: ${cmd.split(' ')[0]}: not found`
        shellOut(id, '\r\n' + (out ? out + '\r\n' : '') + PROMPT)
      } else if (ch === '\x7f') {
        if (s.line) { s.line = s.line.slice(0, -1); shellOut(id, '\b \b') }
      } else if (ch >= ' ') {
        s.line += ch
        shellOut(id, ch)
      }
    }
  }

  return {
    request: (req) => route(req),
    stream(path: string, h: StreamHandlers): EventStream {
      const ex = /\/console\/([^/?]+)\/output/.exec(path)
      if (ex) {
        const sess = shells.get(ex[1])
        if (sess) {
          sess.h = h
          const t = setTimeout(() => { h.onOpen?.(); shellOut(ex[1], 'JailOps demo shell. Try: hostname, uname -a, ls, echo hi, exit\r\n' + PROMPT) }, 0)
          return { close: () => { clearTimeout(t); shells.delete(ex[1]) } }
        }
      }
      subs.add(h)
      const t = setTimeout(() => h.onOpen?.(), 0)
      const tick = setInterval(() => {
        const e: ApiEvent = { type: 'logging', timestamp: new Date().toISOString(), metadata: { message: 'reconcile: 7 jails observed, 1 drifted' } }
        h.onEvent(e)
      }, 20_000)
      return { close: () => { clearTimeout(t); clearInterval(tick); subs.delete(h) } }
    },
  }
}
