// The demo's answer to POST /jails/NAME/expose and /unexpose: the exposure is kept on the demo jail (spec.expose) and
// read by the reach rows. IPv6 is reached on the jail's own address, an IPv4 address through a forward of the host. Only
// loaded for `?demo`.
import type { ExposeEntry, ExposeFamily, ExposeRequest, ExposeResult, UnexposeRequest, Jail } from '@jailops/shared/api/types'
import { jailAddrs } from './fwtest'

type Result = { ok: ExposeResult } | { status: number; code: string; message: string; hint?: string }

const same = (a: string[], b: string[]): boolean => a.length === b.length && a.every((x, i) => x === b[i])

function families(j: Jail, ports: string[], only: string[] | undefined, hostPort: number | undefined): ExposeFamily[] {
  const out: ExposeFamily[] = []
  const list = ports.join(', ')
  for (const a of jailAddrs(j)) {
    const fam = a.includes(':') ? '6' : '4'
    if (only?.length && !only.includes(fam)) continue
    if (fam === '6') out.push({ family: '6', address: a, network: j.spec.network?.[0]?.network ?? '', via: 'uplink', ports: list })
    else out.push({ family: '4', address: a, network: j.spec.network?.[0]?.network ?? '', via: 'forward', ports: list, forwards: ports.map((p) => `${hostPort || p}->${p}`) })
  }
  return out
}

export function demoExpose(j: Jail, b: ExposeRequest): Result {
  if (!b.ports?.length) return { status: 400, code: 'invalid', message: 'name the ports to expose' }
  const protos = b.proto === 'tcp,udp' ? ['tcp', 'udp'] : [b.proto || 'tcp']
  const spec = j.spec as Jail['spec'] & { expose?: ExposeEntry[] }
  const next = [...(spec.expose ?? [])]
  let changed = false
  for (const proto of protos) {
    const e: ExposeEntry = { proto, ports: [...b.ports], ...(b.families?.length ? { families: [...b.families] } : {}), ...(b.from?.length ? { from: [...b.from] } : {}), ...(b.host_port ? { host_port: b.host_port } : {}), ...(b.comment ? { comment: b.comment } : {}), by: 'alice' }
    const i = next.findIndex((x) => (x.proto || 'tcp') === proto && same(x.ports, e.ports))
    if (i < 0) { next.push(e); changed = true } else if (JSON.stringify({ ...next[i], by: '', at: '' }) !== JSON.stringify({ ...e, by: '' })) { next[i] = e; changed = true }
  }
  if (changed && !b.dry_run) spec.expose = next
  const warnings: string[] = []
  if (!jailAddrs(j).some((a) => a.includes(':')) && (!b.families?.length || b.families.includes('6'))) warnings.push('the jail has no IPv6 address: only IPv4 is exposed')
  return { ok: { jail: j.metadata.name, changed, entries: b.dry_run ? (spec.expose ?? []) : next, families: families(j, b.ports, b.families, b.host_port), ...(warnings.length ? { warnings } : {}) } }
}

export function demoUnexpose(j: Jail, b: UnexposeRequest): Result {
  const spec = j.spec as Jail['spec'] & { expose?: ExposeEntry[] }
  const before = spec.expose ?? []
  const proto = b.proto
  const next = before.filter((e) => !((!proto || (e.proto || 'tcp') === proto) && (!b.ports?.length || same(e.ports, b.ports))))
  const changed = next.length !== before.length
  spec.expose = next
  return { ok: { jail: j.metadata.name, changed, entries: next, families: [] } }
}
