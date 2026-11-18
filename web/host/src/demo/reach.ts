// The demo's answer to GET /jails/NAME/reach and GET /exposed: what the Internet reaches of a jail, read from the demo's
// policies (the project's mandatory ones first, then the jail's own). An allow in rule with an external source is an
// entry over IPv6 (the jail's routed address); `jailops expose` adds the entries of spec.expose: IPv6 on the jail's address, IPv4 through a forward of the host. Only loaded for `?demo`.
import type { ExposedEntry, ExposedJail, JailReach, ReachRow } from '@jailops/shared/api/types'
import { jailAddrs, type DemoFwState } from './fwtest'

const HOST_V4 = '10.20.0.1'

function entriesOf(jail: DemoFwState['jails'][number], st: DemoFwState): ExposedEntry[] {
  const [v4, v6] = (() => {
    const a = jailAddrs(jail)
    return [a.find((x) => !x.includes(':')) ?? '', a.find((x) => x.includes(':')) ?? '']
  })()
  const out: ExposedEntry[] = []
  const names = [...st.projectPolicies, ...(jail.spec.firewall?.policies ?? [])]
  for (const pn of names) {
    const pol = st.policies.find((p) => p.metadata.name === pn)
    pol?.spec.rules?.forEach((r, i) => {
      if (r.disabled || r.direction !== 'in' || r.action !== 'allow') return
      const from = r.from?.length ? r.from : ['any']
      if (!from.some((f) => f === 'any' || !/^(jail:|tag:|host$)/.test(f))) return
      const restricted = !(from.length === 1 && from[0] === 'any')
      const by = `policy ${pn} rule ${i + 1}`
      if (v6) out.push({ proto: r.proto ?? 'tcp', ports: (r.ports ?? []).join(','), family: '6', address: v6, from, restricted, via: 'uplink', by })
    })
  }
  for (const e of (jail.spec as { expose?: ExposeEntryLite[] }).expose ?? []) {
    const from = e.from?.length ? e.from : ['any']
    const restricted = !(from.length === 1 && from[0] === 'any')
    for (const fam of ['4', '6']) {
      if (e.families?.length && !e.families.includes(fam)) continue
      const ports = e.ports.join(',')
      if (fam === '6' && v6) out.push({ proto: e.proto || 'tcp', ports, family: '6', address: v6, from, restricted, via: 'uplink', by: 'expose' })
      if (fam === '4' && v4) out.push({ proto: e.proto || 'tcp', ports, family: '4', address: v4, host_address: HOST_V4, host_port: e.host_port ?? (e.ports.length === 1 ? Number(e.ports[0]) : 0), from, restricted, via: 'forward', by: 'expose' })
    }
  }
  return out.sort((a, b) => b.family.localeCompare(a.family))
}

type ExposeEntryLite = { proto?: string; ports: string[]; families?: string[]; from?: string[]; host_port?: number }

export function demoReach(jail: DemoFwState['jails'][number], st: DemoFwState): JailReach {
  const exposed = entriesOf(jail, st)
  const rows: ReachRow[] = exposed.map((e) => ({
    source: 'internet', family: e.family, proto: e.proto, ports: e.ports || 'all', from: e.from, restricted: e.restricted,
    decision: e.via === 'forward' ? 'forward' : 'pass', by: e.by, host_address: e.host_address, host_port: e.host_port,
  }))
  if (!rows.length) rows.push({ source: 'internet', family: '', proto: '', decision: 'none', by: 'no allow in rule with an external source and no port forward' })
  rows.push({ source: 'jails', family: '4,6', proto: 'any', ports: 'all', decision: 'pass', by: 'the default of the jail\'s policies (allow)' })
  rows.push({ source: 'host', family: '4,6', proto: 'icmp,udp', ports: '53', decision: 'pass', by: 'the host answers DNS and ping' })
  rows.push({ source: 'other-projects', family: '', proto: '', decision: 'none', by: 'the isolation of projects' })
  return {
    project: jail.metadata.project ?? '', jail: jail.metadata.name, rows, exposed,
    out: { internet: 'all', host: 'DNS and ping', jails: 'all', other_projects: 'none' },
    default_in: 'allow', default_out: 'allow', rules: [],
  }
}

export function demoExposed(st: DemoFwState): { entries: ExposedJail[] } {
  const entries: ExposedJail[] = []
  for (const j of st.jails) for (const e of entriesOf(j, st)) entries.push({ project: j.metadata.project ?? '', jail: j.metadata.name, ...e })
  return { entries }
}
