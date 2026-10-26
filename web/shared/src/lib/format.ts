const UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']

/** Human bytes with binary units, one decimal under 100. */
export function bytes(n: number | undefined | null): string {
  if (n === undefined || n === null || Number.isNaN(n)) return '-'
  let v = Math.abs(n)
  let i = 0
  while (v >= 1024 && i < UNITS.length - 1) {
    v /= 1024
    i++
  }
  const s = i === 0 || v >= 100 ? Math.round(v).toString() : v.toFixed(1)
  return `${n < 0 ? '-' : ''}${s} ${UNITS[i]}`
}

export function percent(part: number, whole: number): number {
  if (!whole || whole <= 0) return 0
  return Math.max(0, Math.min(100, (part / whole) * 100))
}

/** Seconds since a timestamp rendered as "3m ago". Takes now for testability. */
export function ago(ts: string | undefined, now = Date.now()): string {
  if (!ts) return '-'
  const t = Date.parse(ts)
  if (Number.isNaN(t) || t <= 0 || ts.startsWith('0001')) return '-'
  const s = Math.round((now - t) / 1000)
  if (s < 0) return 'in the future'
  if (s < 5) return 'just now'
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}

export function stamp(ts: string | undefined): string {
  if (!ts || ts.startsWith('0001')) return '-'
  const d = new Date(ts)
  if (Number.isNaN(d.getTime())) return ts
  return d.toISOString().replace('T', ' ').replace(/\.\d+Z$/, 'Z')
}

/** Count the addresses of an inclusive "a-b" IPv4 or IPv6 range; 0 if malformed. */
export function rangeSize(range: string): number {
  const [a, b] = range.split('-')
  if (!a || !b) return 0
  const x = ipv4ToInt(a.trim())
  const y = ipv4ToInt(b.trim())
  if (x === null || y === null || y < x) return 0
  return y - x + 1
}
function ipv4ToInt(s: string): number | null {
  const p = s.split('.')
  if (p.length !== 4) return null
  let n = 0
  for (const o of p) {
    const v = Number(o)
    if (!Number.isInteger(v) || v < 0 || v > 255) return null
    n = n * 256 + v
  }
  return n
}

/** Parse "2G", "512M", "10GiB" into bytes; null when not a size. */
export function parseSize(s: string | undefined): number | null {
  if (!s) return null
  const m = /^(\d+(?:\.\d+)?)\s*([KMGTP]?)(?:i?B)?$/i.exec(s.trim())
  if (!m) return null
  const mul = 'KMGTP'.indexOf(m[2].toUpperCase()) + 1
  return Math.round(parseFloat(m[1]) * 1024 ** mul)
}

export function plural(n: number, one: string, many = one + 's'): string {
  return `${n} ${n === 1 ? one : many}`
}

/** Compress one IPv6 address (RFC 5952): lower case, leading zeros dropped, the longest run of two or more zero
 *  groups becomes "::". An optional "/prefix" is kept. Anything that is not IPv6 (IPv4, "auto", "none") is returned as is. */
export function compressIPv6(input: string): string {
  const [addr, ...rest] = input.split('/')
  if (!addr || !addr.includes(':') || rest.length > 1 || /[^0-9a-fA-F:.]/.test(addr)) return input
  const suffix = rest.length ? '/' + rest[0] : ''
  let head = addr
  let tail: string[] = []
  const dbl = addr.indexOf('::')
  if (dbl >= 0) {
    if (addr.indexOf('::', dbl + 1) >= 0) return input
    head = addr.slice(0, dbl)
    tail = addr.slice(dbl + 2) ? addr.slice(dbl + 2).split(':') : []
  }
  const front = head ? head.split(':') : []
  if (tail.some((g) => g === '') || front.some((g) => g === '')) return input
  let groups: string[]
  if (dbl >= 0) {
    const fill = 8 - front.length - tail.length
    if (fill < 1) return input
    groups = [...front, ...Array<string>(fill).fill('0'), ...tail]
  } else groups = front
  if (groups.length !== 8 || groups.some((g) => !/^[0-9a-fA-F]{1,4}$/.test(g))) return input
  const g = groups.map((x) => parseInt(x, 16).toString(16))
  let best = -1
  let bestLen = 0
  for (let i = 0; i < 8; ) {
    if (g[i] !== '0') { i++; continue }
    let j = i
    while (j < 8 && g[j] === '0') j++
    if (j - i > bestLen) { best = i; bestLen = j - i }
    i = j
  }
  if (bestLen < 2) return g.join(':') + suffix
  return g.slice(0, best).join(':') + '::' + g.slice(best + bestLen).join(':') + suffix
}

/** An address or "a-b" range with every IPv6 part compressed. */
export function shortAddr(s: string | undefined | null): string {
  if (!s) return ''
  return s.split('-').map((p) => compressIPv6(p.trim())).join('-')
}
