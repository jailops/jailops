// The demo's answer to POST /jails/NAME/expose and /unexpose: the exposure is kept on the demo jail (spec.expose) and
// read by the reach rows. IPv6 is reached on the jail's own address, an IPv4 address through a forward of the host (the
// demo's networks are behind NAT). A port is exposed once per protocol, as in the daemon. Only loaded for `?demo`.
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

const portKey = (p: string): number => Number(p.split('-')[0])
const sortPorts = (p: string[]): string[] => [...new Set(p)].sort((a, b) => portKey(a) - portKey(b))

export function demoExpose(j: Jail, b: ExposeRequest): Result {
  if (!b.ports?.length) return { status: 400, code: 'invalid', message: 'name the ports to expose' }
  if (b.host_port && b.ports.length > 1) {
    return { status: 400, code: 'invalid', message: `--host-port takes one port: jailops expose ${j.metadata.name} ${b.ports[0]} --host-port ${b.host_port}, then jailops expose ${j.metadata.name} ${b.ports.slice(1).join(',')}` }
  }
  const protos = [...new Set((b.proto || 'tcp').split(',').map((x) => x.trim()))].sort()
  const spec = j.spec as Jail['spec'] & { expose?: ExposeEntry[] }
  let next = [...(spec.expose ?? [])]
  let changed = false
  const ports = sortPorts(b.ports.flatMap((p) => p.split(',').map((x) => x.trim()).filter(Boolean)))
  for (const proto of protos) {
    const e: ExposeEntry = { proto, ports: [...ports], ...(b.families?.length ? { families: [...b.families] } : {}), ...(b.from?.length ? { from: [...b.from] } : {}), ...(b.host_port ? { host_port: b.host_port } : {}), ...(b.comment ? { comment: b.comment } : {}), by: 'alice' }
    const settings = (x: ExposeEntry): string => JSON.stringify({ ...x, ports: [], by: '', at: '', proto: x.proto || 'tcp' })
    if (next.some((x) => settings(x) === settings(e) && e.ports.every((p) => x.ports.includes(p)))) continue
    next = next.flatMap((x) => ((x.proto || 'tcp') === proto ? (x.ports.filter((p) => !e.ports.includes(p)).length ? [{ ...x, ports: x.ports.filter((p) => !e.ports.includes(p)) }] : []) : [x]))
    next.push(e)
    changed = true
  }
  if (changed && !b.dry_run) spec.expose = next
  const warnings: string[] = []
  if (!jailAddrs(j).some((a) => a.includes(':')) && (!b.families?.length || b.families.includes('6'))) warnings.push('the jail has no IPv6 address: only IPv4 is exposed')
  return { ok: { jail: j.metadata.name, changed, entries: b.dry_run ? (spec.expose ?? []) : next, families: families(j, ports, b.families, b.host_port), exposed: `${ports.join(',')}/${protos.join(',')}`, ...(warnings.length ? { warnings } : {}) } }
}

export function demoUnexpose(j: Jail, b: UnexposeRequest): Result {
  const spec = j.spec as Jail['spec'] & { expose?: ExposeEntry[] }
  const before = spec.expose ?? []
  const proto = b.proto
  const gone: string[] = []
  const next = before.flatMap((e) => {
    if (proto && (e.proto || 'tcp') !== proto) return [e]
    const hit = e.ports.filter((p) => !b.ports?.length || b.ports.includes(p))
    gone.push(...hit)
    const rest = e.ports.filter((p) => !hit.includes(p))
    return rest.length ? [{ ...e, ports: rest }] : []
  })
  const changed = gone.length > 0
  spec.expose = next
  const notes: string[] = []
  if (!changed) {
    for (const p of b.ports ?? []) {
      const n = Number(p)
      const range = before.flatMap((e) => e.ports).find((x) => x.includes('-') && Number(x.split('-')[0]) <= n && n <= Number(x.split('-')[1]))
      if (range) notes.push(`${p} is inside the exposed range ${range}: unexpose the range to remove it`)
    }
  }
  return { ok: { jail: j.metadata.name, changed, entries: next, families: [], ...(changed ? { exposed: `${sortPorts(gone).join(',')}/${proto || 'tcp'}` } : {}), ...(notes.length ? { notes } : {}) } }
}
