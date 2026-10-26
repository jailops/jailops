// An in-process Transport for the backup-node console (`?demo`). It answers like jailops-backupd's
// management API (internal/backupd/api.go), including the roles: add `&role=readonly` or
// `&role=backup-admin` to the URL to see the console with fewer rights.
import { storedToken } from '@jailops/shared/api/client'
import type { EventStream, RawRequest, RawResponse, StreamHandlers, Transport } from '@jailops/shared/api/client'
import type { ApiErrorBody } from '@jailops/shared/api/types'
import type { Job, Mode, NodeToken, Snapshot, Host } from '@/types'
import * as d from './data'

export interface DemoOptions { signedOut?: boolean; speed?: number; role?: string }
const rank: Record<string, number> = { readonly: 0, 'backup-admin': 1, admin: 2 }

interface DemoJob extends Job { duration: number; startedMs: number; apply?: () => void; final?: unknown; settled?: boolean }

export function demoTransport(opts: DemoOptions = {}): Transport {
  const speed = opts.speed ?? 1
  const role = opts.role ?? (typeof location !== 'undefined' ? new URLSearchParams(location.search).get('role') : null) ?? 'admin'
  let authed = !opts.signedOut
  const db = {
    snaps: d.snapshots(), hosts: d.hosts(), quarantine: d.quarantine(), policies: d.policies(), audit: d.audit(), users: d.users(), tokens: d.tokens() as NodeToken[],
    jobs: d.jobs() as DemoJob[], sessions: d.sessions(), mode: 'online' as Mode,
  }
  let seq = 100
  // A real server answers with fresh JSON every time; clone so in-place demo mutations are seen as changes.
  const ok = (metadata: unknown, status = 200): RawResponse => ({ status, envelope: { type: 'sync', status: 'OK', status_code: status, metadata: metadata === undefined || metadata === null ? metadata : structuredClone(metadata) } })
  const fail = (status: number, e: ApiErrorBody): RawResponse => ({ status, envelope: { type: 'error', status: 'Failure', status_code: status, error: e } })
  const forbidden = (need: string) => fail(403, { code: 'forbidden', message: `This needs the ${need} role.`, hint: 'Ask an administrator.' })
  const audit = (action: string, target: string, detail?: unknown) => db.audit.unshift({ id: ++seq, at: new Date().toISOString(), actor: 'demo', action, target, detail, remote: '127.0.0.1' })

  function jobView(j: DemoJob): Job {
    if (!j.settled && j.duration > 0) {
      const t = Date.now() - j.startedMs
      if (t >= j.duration) {
        j.settled = true
        j.state = 'succeeded'
        j.finished_at = new Date().toISOString()
        j.result = j.final
        j.apply?.()
      } else j.state = t < 100 / speed ? 'queued' : 'running'
    }
    const { duration: _a, startedMs: _b, apply: _c, final: _d, settled: _e, ...pub } = j
    return pub
  }
  function startJob(kind: Job['kind'], params: unknown, ms: number, final: unknown, apply?: () => void): RawResponse {
    if (db.jobs.some((j) => !j.settled && j.duration > 0 && (j.state === 'running' || j.state === 'queued') && ['gc', 'verify', 'retention'].includes(j.kind) && ['gc', 'verify', 'retention'].includes(kind)))
      return fail(409, { code: 'busy', message: 'Another repository-wide job is running.', hint: 'Wait for it to finish or cancel it.' })
    const j: DemoJob = { id: ++seq, kind, state: 'queued', params: params as never, created_by: 'demo', created_at: new Date().toISOString(), started_at: new Date().toISOString(), duration: ms / speed, startedMs: Date.now(), final, apply }
    db.jobs.unshift(j)
    return ok(jobView(j), 202)
  }
  const find = (g: string, s: string): Snapshot | undefined => db.snaps.find((x) => x.group_id === g && x.snapshot_id === s)

  function need(method: string, seg: string[]): string {
    if (method === 'GET') return ['audit', 'users', 'tokens'].includes(seg[0]) ? 'admin' : 'readonly'
    if (seg[0] === 'auth' || (seg[0] === 'hosts') || seg[0] === 'users' || seg[0] === 'tokens') return seg[0] === 'auth' ? 'readonly' : 'admin'
    return 'backup-admin'
  }

  async function route(req: RawRequest): Promise<RawResponse> {
    await Promise.resolve()
    if (!authed && storedToken()) authed = true
    const url = new URL(req.path, 'http://x')
    const seg = url.pathname.replace(/\/$/, '').split('/').filter(Boolean).map(decodeURIComponent)
    const m = req.method
    const body = (req.body === undefined ? {} : JSON.parse(JSON.stringify(req.body))) as Record<string, any> // the wire is JSON: never keep the caller's reactive objects
    if (!seg.length && m === 'GET') return ok({ ...d.server(), auth: authed ? 'trusted' : 'untrusted' })
    if (seg[0] === 'auth') {
      if (seg[1] === 'login') {
        if (!body.username) return fail(400, { code: 'invalid', message: 'username is required' })
        authed = true
        return ok({ user: body.username, role, csrf: 'demo-csrf' })
      }
      if (seg[1] === 'logout') { authed = false; return ok(null) }
      if (seg[1] === 'session') return authed ? ok({ user: 'demo', role, csrf: 'demo-csrf' }) : fail(401, { code: 'unauthorized', message: 'No session.' })
    }
    if (!authed) return fail(401, { code: 'unauthorized', message: 'Authentication is required.', hint: 'Sign in with a token or a user.' })
    const needed = need(m, seg)
    if (rank[role] < rank[needed]) return forbidden(needed)

    switch (seg[0]) {
      case 'repository': return ok(d.repository(db.snaps, db.quarantine.length, db.mode, db.sessions.length))
      case 'certificate': return ok({ fingerprint: d.NODE_FINGERPRINT })
      case 'sessions': return ok(db.sessions)
      case 'hosts': return hosts(seg, m, body)
      case 'groups': return groups(seg, m, body)
      case 'jobs': return jobs(seg, m, body, url)
      case 'maintenance':
        if (m === 'PUT') {
          if (!['online', 'read-only', 'offline'].includes(body.mode)) return fail(400, { code: 'invalid', message: 'mode must be online, read-only or offline' })
          audit('maintenance.mode', body.mode, { from: db.mode })
          db.mode = body.mode
        }
        return ok({ mode: db.mode })
      case 'quarantine':
        if (seg[1] === 'purge') { const n = db.quarantine.filter((q) => q.healed).length; db.quarantine = db.quarantine.filter((q) => !q.healed); audit('quarantine.purge', ''); return ok({ removed: n }) }
        return ok(db.quarantine)
      case 'retention-policies':
        if (m === 'GET') return ok(db.policies)
        if (m === 'PUT') { db.policies = db.policies.filter((p) => p.scope !== seg[1]); db.policies.push({ scope: seg[1], policy: body.policy, timezone: body.timezone ?? 'UTC', updated_at: new Date().toISOString(), updated_by: 'demo' }); audit('retention.policy.set', seg[1]); return ok(body) }
        db.policies = db.policies.filter((p) => p.scope !== seg[1]); audit('retention.policy.delete', seg[1]); return ok(null)
      case 'audit': {
        const before = Number(url.searchParams.get('before') ?? 0)
        const act = url.searchParams.get('action') ?? ''
        return ok(db.audit.filter((a) => (!before || a.id < before) && a.action.startsWith(act)).sort((a, b) => b.id - a.id))
      }
      case 'users':
        if (m === 'GET') return ok(db.users)
        if (m === 'POST') {
          if (db.users.some((u) => u.name === body.name)) return fail(409, { code: 'already_exists', message: 'The user exists.' })
          if (!body.password || body.password.length < 12) return fail(400, { code: 'invalid', message: 'The password must have at least 12 characters.' })
          db.users.push({ name: body.name, role: body.role, disabled: false, created_at: new Date().toISOString() }); audit('user.create', body.name, { role: body.role }); return ok(null, 201)
        }
        if (m === 'PUT') { const u = db.users.find((x) => x.name === seg[1]); if (!u) return fail(404, { code: 'not_found', message: 'no such user' }); if (body.role) u.role = body.role; u.disabled = !!body.disabled; return ok(u) }
        db.users = db.users.filter((u) => u.name !== seg[1]); return ok(null)
      case 'tokens':
        if (m === 'GET') return ok(db.tokens)
        if (m === 'POST') {
          if (db.tokens.some((t) => t.name === body.name)) return fail(409, { code: 'already_exists', message: 'A token with this name exists.' })
          const t: NodeToken = { id: 't' + ++seq, name: body.name, role: body.role, allow_from: body.allow_from, expires_at: body.expires_at, created_at: new Date().toISOString() }
          db.tokens.push(t); audit('token.create', t.name, { role: t.role })
          return ok({ ...t, secret: 'jot_' + Array.from({ length: 40 }, () => 'abcdefghijklmnopqrstuvwxyz0123456789'[Math.floor(Math.random() * 36)]).join('') }, 201)
        }
        db.tokens = db.tokens.filter((t) => t.name !== seg[1]); return ok(null)
    }
    return fail(404, { code: 'not_found', message: `No such route: ${m} /${seg.join('/')}` })
  }

  function hosts(seg: string[], m: string, body: Record<string, any>): RawResponse {
    if (m === 'GET') return ok(db.hosts)
    if (m === 'POST') {
      if (db.hosts.some((h) => h.name === body.name)) return fail(409, { code: 'already_exists', message: 'A host with this name or fingerprint is already enrolled.' })
      if (!body.fingerprint && !body.certificate) return fail(400, { code: 'invalid', message: 'a fingerprint or a certificate is required' })
      const h: Host = { id: 'h' + ++seq, name: body.name, fingerprint: body.fingerprint ?? 'SHA256:' + 'ab:'.repeat(31) + 'ab', scopes: body.scopes ?? ['backup-push', 'backup-pull'], note: body.note, source: 'api', created_at: new Date().toISOString() }
      db.hosts.push(h); audit('host.enrol', h.name)
      return ok(h, 201)
    }
    const h = db.hosts.find((x) => x.name === seg[1])
    if (!h) return fail(404, { code: 'not_found', message: 'no such host' })
    if (m === 'PUT') { h.scopes = body.scopes; audit('host.scopes', h.name); return ok(h) }
    h.revoked_at = new Date().toISOString(); audit('host.revoke', h.name)
    return ok(null)
  }

  function groups(seg: string[], m: string, body: Record<string, any>): RawResponse {
    if (seg.length === 1) return ok(d.groups(db.snaps))
    const g = seg[1]
    if (seg[2] !== 'snapshots') return fail(404, { code: 'not_found', message: 'No such route' })
    if (seg.length === 3) return ok(db.snaps.filter((x) => x.group_id === g))
    const s = find(g, seg[3])
    if (!s) return fail(404, { code: 'not_found', message: 'no such snapshot' })
    if (seg.length === 4) return ok(s)
    if (seg[4] === 'protected') { s.protected = !!body.protected; audit('snapshot.protect', `${g}/${s.snapshot_id}`, { protected: s.protected }); return ok(body) }
    if (seg[4] === 'deletion' && seg[5] === 'execute') {
      if (!s.pending_deletion_after) return fail(404, { code: 'not_found', message: 'no deletion is requested' })
      if (Date.parse(s.pending_deletion_after) > Date.now()) return fail(409, { code: 'busy', message: 'The delay has not passed yet.', hint: `Executable from ${s.pending_deletion_after}.` })
      db.snaps = db.snaps.filter((x) => x !== s); audit('snapshot.delete', `${g}/${s.snapshot_id}`)
      return ok(null)
    }
    if (seg[4] === 'deletion') {
      if (m === 'DELETE') { delete s.pending_deletion_after; delete s.pending_deletion_requested_at; delete s.pending_deletion_by; audit('snapshot.deletion.cancel', `${g}/${s.snapshot_id}`); return ok(null) }
      if (s.protected) return fail(409, { code: 'busy', message: 'The snapshot is protected.', hint: 'Remove the protection first.' })
      const hours = body.delay ? parseInt(String(body.delay), 10) : 72
      if (!Number.isFinite(hours) || hours <= 0) return fail(400, { code: 'invalid', message: 'malformed delay' })
      const req = new Date(), after = new Date(Date.now() + hours * 3600_000)
      s.pending_deletion_requested_at = req.toISOString(); s.pending_deletion_after = after.toISOString(); s.pending_deletion_by = 'demo'
      audit('snapshot.deletion.request', `${g}/${s.snapshot_id}`, { delay: body.delay ?? '72h' })
      return ok({ requested_at: s.pending_deletion_requested_at, execute_after: s.pending_deletion_after, requested_by: 'demo' }, 201)
    }
    return fail(404, { code: 'not_found', message: 'No such route' })
  }

  function jobs(seg: string[], m: string, body: Record<string, any>, url: URL): RawResponse {
    if (seg.length === 1) {
      const k = url.searchParams.get('kind')
      return ok(db.jobs.filter((j) => !k || j.kind === k).map(jobView))
    }
    if (m === 'GET') {
      const j = db.jobs.find((x) => String(x.id) === seg[1])
      return j ? ok(jobView(j)) : fail(404, { code: 'not_found', message: 'no such job' })
    }
    if (seg[2] === 'cancel') {
      const j = db.jobs.find((x) => String(x.id) === seg[1])
      if (!j) return fail(404, { code: 'not_found', message: 'no such job' })
      j.settled = true; j.state = 'cancelled'; j.finished_at = new Date().toISOString()
      return ok(null, 202)
    }
    switch (seg[1]) {
      case 'gc': return startJob('gc', body, 2500, { scanned: 482113, live: 470000, kept_young: 1900, kept_pinned: 0, removed: body.dry_run ? 5200 : 5200, bytes_reclaimed: 2.1 * 1024 ** 3, chunk_objects: 477000, chunk_bytes: 94 * 1024 ** 3, quarantine_purged: 0, orphans_removed: 0, snapshots_marked: 0, dry_run: !!body.dry_run, grace: body.grace ?? '36h' })
      case 'verify': return startJob('verify', body, 3000, { mode: body.mode, chunks_read: 482113, snapshots: db.snaps.length, skipped: 0, failed: 1, quarantined: 2 }, () => { db.audit.length })
      case 'retention': {
        const plan = d.retentionPlan(!!body.dry_run, body.group)
        return startJob('retention', body, 1200, plan, () => {
          if (body.dry_run) return
          const ids = new Set(plan.groups.flatMap((g) => g.delete.map((x) => x.snapshot)))
          db.snaps = db.snaps.filter((x) => !ids.has(x.snapshot_id) || x.protected)
          audit('retention.run', body.group ?? 'all')
        })
      }
      case 'reconcile': return startJob('reconcile', {}, 800, {})
    }
    return fail(404, { code: 'not_found', message: 'No such route' })
  }

  return {
    request: (r) => route(r),
    stream(_p: string, h: StreamHandlers): EventStream {
      const t = setTimeout(() => h.onOpen?.(), 0)
      return { close: () => clearTimeout(t) }
    },
  }
}
