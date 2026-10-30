// The host identity module of the demo: a managed host with two snapshots. The files are rendered like the helper
// renders them, and the diff is one hunk per file (enough for the demo; the helper's diff has context hunks).
import type { HostChange, HostIdentity, HostIdentitySpec, HostSnapshot } from '@jailops/shared/api/types'

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
