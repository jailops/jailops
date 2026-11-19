// The live usage page: the order of the jails and the words for the numbers. The server measures; nothing here
// computes a rate.
import type { TopJail } from '@jailops/shared/api/types'

export type TopSort = 'cpu' | 'mem' | 'disk' | 'net' | 'name'
export const topSorts: TopSort[] = ['cpu', 'mem', 'disk', 'net', 'name']

/** What the jail receives and sends together, in bytes per second; unknown while neither is. */
export function netTotal(j: TopJail): number | undefined {
  if (j.net_in_bytes_per_second === undefined && j.net_out_bytes_per_second === undefined) return undefined
  return (j.net_in_bytes_per_second ?? 0) + (j.net_out_bytes_per_second ?? 0)
}

function value(j: TopJail, key: TopSort): number | undefined {
  switch (key) {
    case 'cpu': return j.cpu_percent
    case 'mem': return j.memory_bytes
    case 'disk': return j.disk_bytes
    case 'net': return netTotal(j)
    default: return undefined
  }
}

/** CPU, memory, disk and network biggest first, the name alphabetically; a jail without the number comes last. */
export function sortJails(jails: TopJail[], key: TopSort): TopJail[] {
  return [...jails].sort((a, b) => {
    if (key !== 'name') {
      const x = value(a, key)
      const y = value(b, key)
      if (x !== undefined && y === undefined) return -1
      if (x === undefined && y !== undefined) return 1
      if (x !== undefined && y !== undefined && x !== y) return y - x
    }
    return a.project.localeCompare(b.project) || a.name.localeCompare(b.name)
  })
}

/** 137 (of 200) with the limit, one decimal below ten. */
export function cpuText(j: TopJail): string {
  if (j.cpu_percent === undefined) return 'n/a'
  const v = j.cpu_percent < 10 ? j.cpu_percent.toFixed(1) : String(Math.round(j.cpu_percent))
  return (j.approximate ? '~' : '') + v + '%'
}

/** Seconds as 4m, 2h3m, 3d1h. */
export function uptimeText(seconds: number | undefined): string {
  if (!seconds || seconds <= 0) return '-'
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h${Math.floor((seconds % 3600) / 60)}m`
  return `${Math.floor(seconds / 86400)}d${Math.floor((seconds % 86400) / 3600)}h`
}

/** A share of a limit, 0 to 100, for a meter; undefined without a limit or a number. */
export function share(used: number | undefined, limit: number | undefined): number | undefined {
  if (used === undefined || !limit || limit <= 0) return undefined
  return Math.min(100, (used / limit) * 100)
}
