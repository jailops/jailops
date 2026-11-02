// The host identity module of the demo: a managed host with two snapshots. The files are rendered like the helper
// renders them, and the diff is one hunk per file (enough for the demo; the helper's diff has context hunks).
import type { HostChange, HostFirewall, HostFirewallRule, HostFirewallSpec, HostIdentity, HostIdentitySpec, HostPending, HostPendingChange, HostPendingOutcome, HostSnapshot } from '@jailops/shared/api/types'

interface HostState {
  spec: Required<Pick<HostIdentitySpec, 'hostname'>> & { resolvers: NonNullable<HostIdentitySpec['resolvers']>; ntp: NonNullable<HostIdentitySpec['ntp']> }
  applied: HostIdentitySpec | null
  appliedAt: string | null
  snapshots: Array<HostSnapshot & { spec: HostState['spec'] }>
}

const ago = (h: number) => new Date(Date.now() - h * 3600_000)
const snapName = (d: Date) => d.toISOString().replace(/[-:]/g, '').replace(/\.\d+Z$/, 'Z')
const files = ['/etc/resolv.conf', '/etc/ntp.conf', '/etc/rc.conf']

export function hostState(): HostState {
  const spec = { hostname: 'bsd01.example.net', resolvers: { nameservers: ['192.0.2.53', '2001:db8::53'], search: ['example.net'] }, ntp: { servers: ['ntp1.example.org', 'ntp2.example.org'], enable: true } }
  const older = { ...structuredClone(spec), resolvers: { nameservers: ['198.51.100.53'], search: ['example.net'] } }
  return {
    spec,
    applied: structuredClone(spec),
    appliedAt: ago(26).toISOString(),
    snapshots: [
      { name: snapName(ago(26)), module: 'identity', time: ago(26).toISOString(), files, reason: 'apply', spec: older },
      { name: snapName(ago(240)), module: 'identity', time: ago(240).toISOString(), files: ['/etc/resolv.conf', '/etc/rc.conf'], reason: 'apply', spec: { ...structuredClone(older), hostname: 'freebsd' } },
    ],
  }
}

export function identity(st: HostState): HostIdentity {
  return {
    kind: 'HostIdentity',
    spec: structuredClone(st.spec),
    status: {
      managed: true, config_enabled: true, helper_enabled: true,
      ...(st.applied ? { applied: structuredClone(st.applied), applied_at: st.appliedAt ?? undefined } : {}),
      rc_hostname: st.spec.hostname, resolv_conf_managed: true, ntp_block: st.spec.ntp.servers.length > 0,
      ntp_sync: st.spec.ntp.enable ? { synchronised: true, peer: st.spec.ntp.servers[0] ?? '' } : { synchronised: false, detail: 'ntpd is not running' },
    },
  }
}

const resolv = (s: HostState['spec']) => ['# managed by jailops', ...(s.resolvers.search?.length ? [`search ${s.resolvers.search.join(' ')}`] : []), ...s.resolvers.nameservers.map((n) => `nameserver ${n}`)]
const ntpBlock = (s: HostState['spec']) => s.ntp.servers.length ? ['# jailops begin', ...s.ntp.servers.map((n) => `server ${n} iburst`), '# jailops end'] : []
const rc = (s: HostState['spec']) => [`hostname="${s.hostname}"`, `ntpd_enable="${s.ntp.enable ? 'YES' : 'NO'}"`, `ntpd_sync_on_start="${s.ntp.enable ? 'YES' : 'NO'}"`]

function hunk(path: string, a: string[], b: string[], plain = false): string {
  if (a.join('\n') === b.join('\n')) return ''
  const head = plain ? '' : `@@ -1,${a.length} +1,${b.length} @@\n`
  const del = a.filter((l) => !b.includes(l)).map((l) => '-' + l)
  const add = b.filter((l) => !a.includes(l)).map((l) => '+' + l)
  const keep = plain ? [] : a.filter((l) => b.includes(l)).map((l) => ' ' + l)
  return `--- ${path}\n+++ ${path}\n${head}${[...keep, ...del, ...add].join('\n')}\n`
}

function change(from: HostState['spec'], to: HostState['spec']): { diff: string; actions: string[] } {
  const diff = hunk('/etc/resolv.conf', resolv(from), resolv(to)) + hunk('/etc/ntp.conf', ntpBlock(from), ntpBlock(to)) + hunk('/etc/rc.conf', rc(from), rc(to), true)
  const actions: string[] = []
  if (from.hostname !== to.hostname) actions.push(`set the hostname to ${to.hostname} (hostname and rc.conf)`)
  if (resolv(from).join() !== resolv(to).join()) actions.push('write /etc/resolv.conf')
  if (ntpBlock(from).join() !== ntpBlock(to).join()) actions.push('write the jailops block of /etc/ntp.conf')
  if (from.ntp.enable !== to.ntp.enable) actions.push(`set ntpd_enable and ntpd_sync_on_start to ${to.ntp.enable ? 'YES' : 'NO'}`)
  if (ntpBlock(from).join() !== ntpBlock(to).join() || from.ntp.enable !== to.ntp.enable) actions.push(to.ntp.enable ? 'service ntpd restart' : 'service ntpd onestop')
  return { diff, actions }
}

function snapshot(st: HostState, reason: string): string {
  let name = snapName(new Date())
  for (let i = 1; st.snapshots.some((x) => x.name === name); i++) name = `${snapName(new Date())}-${i}`
  st.snapshots.unshift({ name, module: 'identity', time: new Date().toISOString(), files, reason, spec: structuredClone(st.spec) })
  return name
}

/** PUT /host/identity: a part left out is unchanged. */
export function applyIdentity(st: HostState, want: HostIdentitySpec, dry: boolean): HostChange {
  const to: HostState['spec'] = {
    hostname: want.hostname || st.spec.hostname,
    resolvers: want.resolvers ? structuredClone(want.resolvers) : structuredClone(st.spec.resolvers),
    ntp: want.ntp ? structuredClone(want.ntp) : structuredClone(st.spec.ntp),
  }
  const c = change(st.spec, to)
  const changed = c.actions.length > 0
  if (dry || !changed) return { dry_run: dry || undefined, changed, diff: c.diff || undefined, actions: c.actions }
  const snap = snapshot(st, 'apply')
  st.spec = to
  st.applied = { ...(st.applied ?? {}), ...structuredClone(want) }
  st.appliedAt = new Date().toISOString()
  return { changed, diff: c.diff, actions: c.actions, snapshot: snap }
}

export function restoreSnapshot(st: HostState, name: string, dry: boolean): HostChange | null {
  const s = name === 'latest' ? st.snapshots[0] : st.snapshots.find((x) => x.name === name)
  if (!s) return null
  const c = change(st.spec, s.spec)
  const actions = [...s.files.map((f) => `copy ${f} back from snapshot ${s.name}`), ...c.actions.filter((a) => a.startsWith('service'))]
  if (dry) return { dry_run: true, changed: c.actions.length > 0, diff: c.diff || undefined, actions }
  const snap = snapshot(st, 'restore')
  st.spec = structuredClone(s.spec)
  return { changed: c.actions.length > 0, diff: c.diff || undefined, actions, snapshot: snap }
}

export const snapshots = (st: HostState): HostSnapshot[] => st.snapshots.map(({ spec: _spec, ...x }) => structuredClone(x))

// ---- the firewall module and commit confirmed ----
// A managed host with the custom-rules template; an apply waits for its confirmation and the demo reverts it when the
// deadline has passed (checked whenever the state is read, like a timer that fired meanwhile).

export interface FwState {
  spec: HostFirewallSpec
  appliedAt: string
  pending: { change: HostPendingChange; prev: HostFirewallSpec; prevAt: string } | null
  last: HostPendingOutcome | null
}

const CONSOLE_PORT = 39110

export function firewallState(pending = false): FwState {
  const st: FwState = {
    spec: {
      template: 'custom-rules', ssh: { ports: [22], allow_from: ['@admins'] }, console: { port: CONSOLE_PORT },
      allow: [{ proto: 'tcp', ports: ['80', '443'], comment: 'web jails behind the host' }, { proto: 'udp', ports: ['51820'], from: ['198.51.100.0/24'], comment: 'WireGuard' }],
      sets: [{ name: 'admins', members: ['192.0.2.0/24', '2001:db8:10::/48'] }], icmp: true, nat: 'none',
    },
    appliedAt: ago(30).toISOString(), pending: null, last: null,
  }
  if (pending) applyFirewall(st, { ...structuredClone(st.spec), icmp: false }, false, 600)
  return st
}

const from = (l?: string[]) => (l?.length ? l.join(', ') : 'any')

export function fwRules(s: HostFirewallSpec): HostFirewallRule[] {
  const fixed = (action: string, direction: string, comment: string, proto = ''): HostFirewallRule => ({ action, direction, ...(proto ? { proto } : {}), from: 'any', comment, fixed: true })
  const out = [fixed('pass', 'in', 'loopback (lo0 is not filtered)'), fixed('pass', 'in', "the jails' own anchors (jailops/*) come first")]
  out.push(s.template === 'open' ? fixed('pass', 'in', 'everything (open template)') : fixed('block', 'in', 'everything not passed below' + (s.logging ? ', logged' : '')))
  out.push(fixed('pass', 'out', 'everything the host sends, and the replies to it'), fixed('pass', 'in', 'neighbour discovery and router advertisements', 'icmp6'), fixed('pass', 'in', 'path MTU discovery and ICMP errors', 'icmp, icmp6'))
  if (s.template === 'open') return out
  out.push({ action: 'pass', direction: 'in', proto: 'tcp', ports: (s.ssh?.ports ?? [22]).join(', '), from: from(s.ssh?.allow_from), comment: 'ssh' })
  out.push({ action: 'pass', direction: 'in', proto: 'tcp', ports: String(CONSOLE_PORT), from: from(s.console?.allow_from), comment: 'JailOps console and API' })
  if (s.template === 'custom-rules') for (const a of s.allow ?? []) out.push({ action: 'pass', direction: 'in', proto: a.proto, ports: a.ports.join(', '), from: from(a.from), ...(a.comment ? { comment: a.comment } : {}) })
  if (s.icmp) out.push({ action: 'pass', direction: 'in', proto: 'icmp, icmp6', from: 'any', comment: 'ping (echo requests)' })
  return out
}

const pfFrom = (l?: string[]) => (!l?.length ? 'any' : l.length === 1 ? l[0].replace(/^@(.*)$/, '<jo_hf_$1>') : `{ ${l.map((x) => x.replace(/^@(.*)$/, '<jo_hf_$1>')).join(', ')} }`)
const pfPorts = (l: string[]) => (l.length === 1 ? l[0].replace('-', ':') : `{ ${l.map((x) => x.replace('-', ':')).join(', ')} }`)

/** pf.conf as the helper renders it, closely enough for a diff in the demo. */
export function pfConf(s: HostFirewallSpec): string[] {
  const l = ['# managed by jailops', '# Rendered from the host firewall settings; a change made here is reported as drift.']
  for (const t of s.sets ?? []) l.push(`table <jo_hf_${t.name}> const { ${t.members.join(', ')} }`)
  l.push('set skip on lo0', 'set state-policy if-bound', 'scrub in all fragment reassemble', 'nat-anchor "jailops/*"', 'rdr-anchor "jailops/*"', 'anchor "jailops/*"')
  if (s.template === 'open') l.push('pass in all')
  else l.push(s.logging ? 'block log in all' : 'block in all')
  l.push('pass out all keep state', 'pass in inet6 proto icmp6 icmp6-type { neighbrsol, neighbradv, routersol, routeradv }', 'pass in inet6 proto icmp6 icmp6-type { toobig, unreach, timex, paramprob }', 'pass in inet proto icmp icmp-type unreach code needfrag')
  if (s.template === 'open') return l
  l.push(`pass in proto tcp from ${pfFrom(s.ssh?.allow_from)} to (self) port ${pfPorts((s.ssh?.ports ?? [22]).map(String))}`)
  l.push(`pass in proto tcp from ${pfFrom(s.console?.allow_from)} to (self) port ${CONSOLE_PORT}`)
  if (s.template === 'custom-rules') for (const a of s.allow ?? []) { if (a.comment) l.push(`# ${a.comment}`); l.push(`pass in proto ${a.proto} from ${pfFrom(a.from)} to (self) port ${pfPorts(a.ports)}`) }
  if (s.icmp) l.push('pass in inet proto icmp icmp-type echoreq', 'pass in inet6 proto icmp6 icmp6-type echoreq')
  return l
}

function warnings(s: HostFirewallSpec): string[] {
  const w: string[] = []
  const zero = (v: string) => v.endsWith('/0')
  const from = s.ssh?.allow_from ?? []
  if (s.template === 'open' || !from.length || from.some((v) => zero(v) || (s.sets ?? []).some((x) => `@${x.name}` === v && x.members.some(zero))))
    w.push('ssh is open to the world: narrow ssh allow_from to the addresses you administer from')
  if (s.template === 'open') w.push('the open template passes everything to the host; use it only behind another firewall')
  return w
}

/** Reverts a change whose deadline has passed: what the helper's timer does. */
export function settleFirewall(st: FwState, now = Date.now()): void {
  const p = st.pending
  if (!p || Date.parse(p.change.deadline) > now) return
  st.spec = p.prev
  st.appliedAt = p.prevAt
  st.last = { id: p.change.id, module: 'firewall', snapshot: p.change.snapshot, outcome: 'timed_out', time: p.change.deadline, detail: 'nobody confirmed the change before its deadline' }
  st.pending = null
}

function pendingView(p: HostPendingChange): HostPendingChange {
  return { ...p, seconds_left: Math.max(0, Math.ceil((Date.parse(p.deadline) - Date.now()) / 1000)) }
}

export function firewall(st: FwState): HostFirewall {
  settleFirewall(st)
  return {
    kind: 'HostFirewall',
    spec: structuredClone(st.spec),
    status: {
      managed: true, config_enabled: true, helper_enabled: true, file_exists: true, file_managed: true,
      applied: structuredClone(st.spec), applied_at: st.appliedAt, pf_enabled: true, rules: fwRules(st.spec), console_port: CONSOLE_PORT,
      ...(warnings(st.spec).length ? { warnings: warnings(st.spec) } : {}),
      ...(st.pending ? { pending: pendingView(st.pending.change) } : {}),
    },
  }
}

export function pendingState(st: FwState): HostPending {
  settleFirewall(st)
  return { ...(st.pending ? { pending: pendingView(st.pending.change) } : {}), ...(st.last ? { last: st.last } : {}) }
}

/** PUT /host/firewall: the whole spec. A real apply waits for its confirmation. */
export function applyFirewall(st: FwState, want: HostFirewallSpec, dry: boolean, within: number): HostChange {
  const to: HostFirewallSpec = { ...structuredClone(want), console: { ...(want.console ?? {}), port: CONSOLE_PORT }, nat: 'none' }
  if (to.template !== 'custom-rules') delete to.allow
  const diff = hunk('/etc/pf.conf', pfConf(st.spec), pfConf(to))
  const changed = diff !== ''
  const snap = snapName(new Date())
  const actions = changed ? [
    'check the rendered file with pfctl -n',
    dry ? 'keep a copy of /etc/pf.conf (snapshot)' : `keep a copy of /etc/pf.conf in snapshot ${snap}`,
    `write the pending record: the change reverts in ${within} seconds unless it is confirmed`,
    'write /etc/pf.conf', 'pfctl -f /etc/pf.conf', 'wait for the confirmation: jailops host confirm',
  ] : []
  const out: HostChange = { ...(dry ? { dry_run: true } : {}), changed, ...(diff ? { diff } : {}), actions, rules: fwRules(to), ...(warnings(to).length ? { warnings: warnings(to) } : {}) }
  if (dry || !changed) return out
  const now = Date.now()
  const change: HostPendingChange = { id: Array.from({ length: 16 }, () => '0123456789abcdef'[Math.floor(Math.random() * 16)]).join(''), module: 'firewall', snapshot: snap,
    created: new Date(now).toISOString(), deadline: new Date(now + within * 1000).toISOString(), seconds_left: within, confirm_within: within }
  st.pending = { change, prev: st.spec, prevAt: st.appliedAt }
  st.spec = to
  st.appliedAt = new Date(now).toISOString()
  return { ...out, snapshot: snap, pending: change }
}

/** Confirm or revert the pending change; null when there is none (or the id names another). */
export function endPending(st: FwState, how: 'confirm' | 'revert', id?: string): HostPending | 'none' | 'other' {
  settleFirewall(st)
  const p = st.pending
  if (!p) return 'none'
  if (id && id !== p.change.id) return 'other'
  if (how === 'revert') { st.spec = p.prev; st.appliedAt = p.prevAt }
  st.last = { id: p.change.id, module: 'firewall', snapshot: p.change.snapshot, outcome: how === 'confirm' ? 'confirmed' : 'reverted', time: new Date().toISOString() }
  st.pending = null
  return pendingState(st)
}

// The demo keeps the firewall and its pending change across a reload of the page (in this tab only), so the
// banner can be seen to come back like it does against a real server.
const FW_KEY = 'jailops.demo.firewall'
export function loadFirewall(): FwState | null {
  try {
    const v = sessionStorage.getItem(FW_KEY)
    return v ? (JSON.parse(v) as FwState) : null
  } catch {
    return null
  }
}
export function saveFirewall(st: FwState): void {
  try { sessionStorage.setItem(FW_KEY, JSON.stringify(st)) } catch { /* the demo forgets it */ }
}
