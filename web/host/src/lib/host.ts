// The host identity form: validation that mirrors sdk/api/host.go (the server checks again) and the spec it sends.
import type { HostIdentity, HostIdentitySpec } from '@jailops/shared/api/types'

export const MAX_HOSTNAME = 253
export const MAX_NAMESERVERS = 3
export const MAX_SEARCH = 6
export const MAX_SEARCH_LEN = 256
export const MAX_NTP = 4

export interface HostForm {
  hostname: string
  nameservers: string[]
  search: string[]
  ntpServers: string[]
  ntpEnable: boolean
}

const label = /^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/

function ipv4(s: string): boolean {
  const p = s.split('.')
  return p.length === 4 && p.every((x) => /^(0|[1-9][0-9]{0,2})$/.test(x) && Number(x) <= 255)
}

/** Expands an IPv6 literal to eight groups, or null. No zone, no IPv4-mapped form. */
function ipv6Groups(s: string): number[] | null {
  if (!s.includes(':') || s.includes('%') || /[^0-9A-Fa-f:.]/.test(s)) return null
  let body = s
  const lastColon = s.lastIndexOf(':')
  const last = s.slice(lastColon + 1)
  if (last.includes('.')) {
    // a trailing dotted quad is two groups
    if (!ipv4(last)) return null
    const o = last.split('.').map(Number)
    body = s.slice(0, lastColon + 1) + ((o[0] << 8) | o[1]).toString(16) + ':' + ((o[2] << 8) | o[3]).toString(16)
  }
  const parts = body.split('::')
  if (parts.length > 2) return null
  const grp = (x: string) => (x === '' ? [] : x.split(':'))
  const head = grp(parts[0])
  const rest = parts.length === 2 ? grp(parts[1]) : []
  if ([...head, ...rest].some((g) => !/^[0-9A-Fa-f]{1,4}$/.test(g))) return null
  const n = head.length + rest.length
  if (parts.length === 1 ? n !== 8 : n > 7) return null
  const mid = new Array(8 - n).fill(0)
  return [...head.map((g) => parseInt(g, 16)), ...(parts.length === 2 ? mid : []), ...rest.map((g) => parseInt(g, 16))]
}

/** The canonical text of an IP literal that names a host, or null (not an address, a zone, unspecified, IPv4-mapped). */
export function hostAddr(s: string): string | null {
  if (ipv4(s)) return s === '0.0.0.0' ? null : s
  const g = ipv6Groups(s)
  if (!g) return null
  if (g.every((x) => x === 0)) return null
  if (g.slice(0, 5).every((x) => x === 0) && g[5] === 0xffff) return null
  return g.map((x) => x.toString(16)).join(':')
}

/** A host or domain name of RFC 1123 labels, at most 253 characters, that is not an IP address. */
export function validDNSName(s: string): boolean {
  if (!s || s.length > MAX_HOSTNAME || ipv4(s) || ipv6Groups(s)) return false
  return s.split('.').every((l) => label.test(l))
}

export interface FieldError { field: string; message: string }

/** Every problem of the form; empty when it may be sent. */
export function validateForm(f: HostForm): FieldError[] {
  const out: FieldError[] = []
  const hostname = f.hostname.trim()
  if (!hostname) out.push({ field: 'hostname', message: 'A hostname is required.' })
  else if (!validDNSName(hostname)) out.push({ field: 'hostname', message: 'Letters, digits and hyphens in labels of at most 63 characters, separated by dots, at most 253 in all; not an IP address.' })
  const ns = clean(f.nameservers)
  if (ns.length === 0 || ns.length > MAX_NAMESERVERS) out.push({ field: 'nameservers', message: `Give 1 to ${MAX_NAMESERVERS} nameservers.` })
  const seen = new Set<string>()
  ns.forEach((n, i) => {
    const a = hostAddr(n)
    if (!a) out.push({ field: `nameserver-${i}`, message: `${n} is not an IPv4 or IPv6 address.` })
    else if (seen.has(a)) out.push({ field: `nameserver-${i}`, message: `${n} is listed twice.` })
    else seen.add(a)
  })
  const sd = clean(f.search)
  if (sd.length > MAX_SEARCH) out.push({ field: 'search', message: `At most ${MAX_SEARCH} search domains.` })
  const seenD = new Set<string>()
  let total = 0
  sd.forEach((d, i) => {
    if (!validDNSName(d)) out.push({ field: `search-${i}`, message: `${d} is not a valid domain name.` })
    else if (seenD.has(d.toLowerCase())) out.push({ field: `search-${i}`, message: `${d} is listed twice.` })
    seenD.add(d.toLowerCase())
    total += d.length + 1
  })
  if (total > MAX_SEARCH_LEN) out.push({ field: 'search', message: `The search domains are longer than ${MAX_SEARCH_LEN} characters together.` })
  const ntp = clean(f.ntpServers)
  if (ntp.length > MAX_NTP) out.push({ field: 'ntp', message: `At most ${MAX_NTP} NTP servers.` })
  const seenN = new Set<string>()
  ntp.forEach((v, i) => {
    const a = hostAddr(v)
    const key = a ?? v.toLowerCase()
    if (!a && !validDNSName(v)) out.push({ field: `ntp-${i}`, message: `${v} is neither a host name nor an IP address.` })
    else if (seenN.has(key)) out.push({ field: `ntp-${i}`, message: `${v} is listed twice.` })
    seenN.add(key)
  })
  return out
}

const clean = (l: string[]) => l.map((x) => x.trim()).filter(Boolean)

/** The form filled from what the host has. */
export function formOf(h: HostIdentity): HostForm {
  return {
    hostname: h.spec.hostname ?? '',
    nameservers: [...(h.spec.resolvers?.nameservers ?? [])],
    search: [...(h.spec.resolvers?.search ?? [])],
    ntpServers: [...(h.spec.ntp?.servers ?? [])],
    ntpEnable: h.spec.ntp?.enable ?? false,
  }
}

/** The whole spec of the form: every part is sent, so what is applied is what the form shows. */
export function specOf(f: HostForm): HostIdentitySpec {
  const search = clean(f.search)
  return {
    hostname: f.hostname.trim(),
    resolvers: { nameservers: clean(f.nameservers), ...(search.length ? { search } : {}) },
    ntp: { servers: clean(f.ntpServers), enable: f.ntpEnable },
  }
}

/** A stable key of a spec: a preview is valid for the form only while the key is unchanged. */
export const specKey = (s: HostIdentitySpec) => JSON.stringify(s)

export type DiffKind = 'add' | 'del' | 'hunk' | 'file' | 'ctx'

/** Classifies the lines of a unified diff for display. */
export function diffLines(diff: string): Array<{ kind: DiffKind; text: string }> {
  if (!diff) return []
  return diff.replace(/\n$/, '').split('\n').map((text) => {
    if (text.startsWith('+++') || text.startsWith('---')) return { kind: 'file' as const, text }
    if (text.startsWith('@@')) return { kind: 'hunk' as const, text }
    if (text.startsWith('+')) return { kind: 'add' as const, text }
    if (text.startsWith('-')) return { kind: 'del' as const, text }
    return { kind: 'ctx' as const, text }
  })
}

/** The configuration lines still missing to enable the identity module. */
export function enableLines(h: HostIdentity): { config?: string; helper?: string } {
  return {
    ...(h.status.config_enabled ? {} : { config: 'host:\n  manage:\n    modules: [identity]' }),
    ...(h.status.helper_enabled ? {} : { helper: 'host-manage = identity' }),
  }
}

export const RESTART = 'service jailops_priv restart && service jailopsd restart'
