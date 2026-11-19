// The demo's answer to GET /top: the running jails with numbers that move a little at every call, so the page has
// something to show. Only loaded for `?demo`.
import type { Jail, Top, TopJail } from '@jailops/shared/api/types'

const GiB = 1024 ** 3
let calls = 0

/** A number that wanders around base with the call count and the name. */
function wander(name: string, base: number, spread: number, phase = 0): number {
  let h = 0
  for (const c of name) h = (h * 31 + c.charCodeAt(0)) % 997
  return Math.max(0, base + spread * Math.sin((calls + h + phase) / 3))
}

export function demoTop(jails: Jail[], project: string): Top {
  calls++
  const out: TopJail[] = []
  let notes = false
  for (const j of jails) {
    if (j.status.power !== 'running') continue
    const p = j.metadata.project ?? ''
    if (project && p !== project) continue
    const name = j.metadata.name
    const linux = name === 'alpine-app'
    const memLimit = name === 'db' ? 4 * GiB : name === 'web' ? 2 * GiB : 0
    out.push({
      project: p, name,
      uptime_seconds: 3600 * (2 + (name.length % 5)) + calls * 2,
      cpu_percent: Math.round(wander(name, name === 'db' ? 85 : 12, 10) * 10) / 10,
      cpu_limit_percent: name === 'web' ? 200 : undefined,
      memory_bytes: Math.round(wander(name, name === 'db' ? 2.4 * GiB : 0.4 * GiB, 0.05 * GiB, 1)),
      memory_limit_bytes: memLimit || undefined,
      processes: 5 + (name.length * 3) % 40,
      disk_bytes: Math.round((0.3 + (name.length % 4)) * GiB),
      disk_quota_bytes: name === 'web' ? 10 * GiB : undefined,
      net_in_bytes_per_second: calls > 1 ? Math.round(wander(name, 40_000, 30_000, 2)) : undefined,
      net_out_bytes_per_second: calls > 1 ? Math.round(wander(name, 90_000, 60_000, 4)) : undefined,
      approximate: linux || undefined,
    })
    notes = notes || linux
  }
  out.sort((a, b) => a.project.localeCompare(b.project) || a.name.localeCompare(b.name))
  const top: Top = {
    measured_at: new Date().toISOString(),
    interval_seconds: calls > 1 ? 2 : undefined,
    jails: out,
    host: {
      load_1: Math.round(wander('host', 0.9, 0.4) * 100) / 100, load_5: 0.82, load_15: 0.7, cpus: 8,
      cpu_percent: calls > 1 ? Math.round(wander('host', 14, 8) * 10) / 10 : undefined,
      memory_total_bytes: 32 * GiB, memory_used_bytes: Math.round(wander('mem', 9 * GiB, 0.2 * GiB)), arc_bytes: 6 * GiB,
      pool: 'tank', pool_size_bytes: 1800 * GiB, pool_allocated_bytes: 640 * GiB, pool_free_bytes: 1160 * GiB,
      running_jails: jails.filter((j) => j.status.power === 'running').length,
    },
  }
  if (notes) top.notes = ['racct is off: cpu and memory are approximate (from the process list); kern.racct.enable=1 in /boot/loader.conf and a reboot give exact numbers']
  return top
}
