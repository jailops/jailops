// The sink form of log forwarding: the fields as typed, the checks the server makes too (so the form can say
// what is wrong before sending), and the LogSink it becomes. The server checks everything again.
import type { LogForward, LogSink, LogSinkSource, LogSinkStatus, LogSinkType } from '@jailops/shared/api/types'

export const FACILITIES = ['local0', 'local1', 'local2', 'local3', 'local4', 'local5', 'local6', 'local7'] as const
export const DEFAULT_QUEUE = 10_000
export const MIN_QUEUE = 100
export const MAX_QUEUE = 1_000_000
const NAME = /^[a-z][a-z0-9-]{0,31}$/
const JAIL = /^[a-z][a-z0-9-]{0,62}$/
const FILE_SEG = /^[A-Za-z0-9_-][A-Za-z0-9._-]*$/

export interface SinkForm {
  name: string
  type: LogSinkType
  to: string
  facility: string
  caFile: string
  tokenFile: string
  queue: string
  projects: string
  jails: string
  labels: string
  excludeJails: string
  excludeLabels: string
  files: string
  appLogs: boolean
  jailLogs: boolean
  audit: boolean
}

export function blankSink(): SinkForm {
  return { name: '', type: 'syslog', to: '', facility: 'local0', caFile: '', tokenFile: '', queue: '', projects: '', jails: '', labels: '', excludeJails: '', excludeLabels: '', files: '', appLogs: true, jailLogs: true, audit: false }
}

const join = (xs: string[] | undefined) => (xs ?? []).join(', ')
const joinLabels = (m: Record<string, string> | undefined) => Object.entries(m ?? {}).map(([k, v]) => `${k}=${v}`).join(', ')

/** The form of an existing sink. */
export function formOf(k: LogSink): SinkForm {
  const src = k.sources ?? {}
  return {
    name: k.name, type: k.type, to: k.to, facility: k.facility || 'local0', caFile: k.ca_file ?? '', tokenFile: k.token_file ?? '',
    queue: k.queue ? String(k.queue) : '', projects: join(src.projects), jails: join(src.jails), labels: joinLabels(src.labels),
    excludeJails: join(src.exclude_jails), excludeLabels: joinLabels(src.exclude_labels), files: join(src.files),
    appLogs: !src.no_app_logs, jailLogs: !src.no_jails, audit: !!src.audit,
  }
}

/** "a, b c" as a list: commas or spaces separate. */
export function splitList(s: string): string[] {
  return s.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean)
}

/** "k=v, k2=v2" as a map, or the first entry that is not key=value. */
export function parseLabels(s: string): { labels: Record<string, string>; bad?: string } {
  const labels: Record<string, string> = {}
  for (const part of s.split(',').map((x) => x.trim()).filter(Boolean)) {
    const i = part.indexOf('=')
    if (i <= 0) return { labels, bad: part }
    labels[part.slice(0, i).trim()] = part.slice(i + 1).trim()
  }
  return { labels }
}

/** The transport of a destination, '' when it is not a URL with a host. */
export function schemeOf(to: string): string {
  const m = /^([a-z]+):\/\/([^/?#]+)/.exec(to.trim())
  return m && m[2] && !m[2].includes('@') ? m[1] : ''
}

/** A plain udp or tcp destination that is not a loopback address: the lines cross the network in the clear. */
export function plaintextRemote(type: LogSinkType, to: string): boolean {
  const sch = schemeOf(to)
  if (type !== 'syslog' || (sch !== 'udp' && sch !== 'tcp')) return false
  const host = (/^[a-z]+:\/\/(\[[^\]]+\]|[^:/]+)/.exec(to.trim())?.[1] ?? '').replace(/^\[|\]$/g, '')
  return !(host === 'localhost' || host === '::1' || /^127\./.test(host))
}

/** Problems of the form by field; an empty object is a form the server should accept. */
export function checkSink(f: SinkForm, taken: string[]): Record<string, string> {
  const p: Record<string, string> = {}
  if (f.name && !NAME.test(f.name)) p.name = 'name'
  else if (f.name && taken.includes(f.name)) p.name = 'taken'
  const to = f.to.trim()
  const sch = schemeOf(to)
  if (to) {
    if (!sch) p.to = 'url'
    else if (f.type === 'syslog' && !['udp', 'tcp', 'tls'].includes(sch)) p.to = 'syslogScheme'
    else if (f.type === 'webhook' && sch !== 'https') p.to = 'https'
    else {
      const port = /^[a-z]+:\/\/(?:\[[^\]]+\]|[^:/?#]+)(?::(\d*))?/.exec(to)?.[1]
      if (port !== undefined && (!/^\d+$/.test(port) || Number(port) < 1 || Number(port) > 65535)) p.to = 'port'
      else if (f.type === 'syslog' && /^[a-z]+:\/\/[^/]+\/./.test(to)) p.to = 'path'
    }
  }
  if (f.caFile && (f.type === 'syslog' && sch !== 'tls')) p.caFile = 'caOnlyTLS'
  for (const k of ['caFile', 'tokenFile'] as const) {
    const v = f[k].trim()
    if (v && (!v.startsWith('/') || v.includes('/../') || v.endsWith('/..') || v.includes('//') || (v.length > 1 && v.endsWith('/')))) p[k] = 'path'
  }
  if (f.queue.trim()) {
    const n = Number(f.queue)
    if (!Number.isInteger(n) || n < MIN_QUEUE || n > MAX_QUEUE) p.queue = 'queue'
  }
  for (const k of ['projects', 'jails', 'excludeJails'] as const) if (splitList(f[k]).some((x) => !JAIL.test(x))) p[k] = 'names'
  for (const k of ['labels', 'excludeLabels'] as const) if (parseLabels(f[k]).bad !== undefined) p[k] = 'labels'
  const files = splitList(f.files)
  if (files.some((x) => x.split('/').length > 2 || x.split('/').some((s) => !FILE_SEG.test(s)) || /\.(\d+|gz|bz2|xz)$/.test(x))) p.files = 'files'
  else if (files.length > 8) p.files = 'tooMany'
  if (!f.jailLogs && !f.audit) p.audit = 'nothing'
  return p
}

/** The LogSink the form describes; empty fields are left out. */
export function sinkBody(f: SinkForm): LogSink {
  const src: LogSinkSource = {}
  const list = (s: string) => { const l = splitList(s); return l.length ? l : undefined }
  const labels = (s: string) => { const m = parseLabels(s).labels; return Object.keys(m).length ? m : undefined }
  const set = <K extends keyof LogSinkSource>(k: K, v: LogSinkSource[K] | undefined) => { if (v !== undefined) src[k] = v }
  set('projects', list(f.projects))
  set('jails', list(f.jails))
  set('labels', labels(f.labels))
  set('exclude_jails', list(f.excludeJails))
  set('exclude_labels', labels(f.excludeLabels))
  set('files', list(f.files))
  if (!f.appLogs) src.no_app_logs = true
  if (!f.jailLogs) src.no_jails = true
  if (f.audit) src.audit = true
  const k: LogSink = { name: f.name.trim(), type: f.type, to: f.to.trim(), sources: src }
  if (f.type === 'syslog' && f.facility && f.facility !== 'local0') k.facility = f.facility
  if (f.caFile.trim() && (f.type === 'webhook' || schemeOf(f.to) === 'tls')) k.ca_file = f.caFile.trim()
  if (f.type === 'webhook' && f.tokenFile.trim()) k.token_file = f.tokenFile.trim()
  if (f.queue.trim()) k.queue = Number(f.queue)
  return k
}

/** The sink list with one sink added, replaced (by its old name) or removed. */
export function withSink(sinks: LogSink[], sink: LogSink | null, replace?: string): LogSink[] {
  const out = sinks.filter((k) => k.name !== (replace ?? sink?.name))
  if (!sink) return out
  const at = sinks.findIndex((k) => k.name === (replace ?? sink.name))
  if (at < 0) return [...out, sink]
  out.splice(at, 0, sink)
  return out
}

/** The status of a sink, matched by name. */
export function statusOf(fw: LogForward | undefined, name: string): LogSinkStatus | undefined {
  return fw?.status.sinks?.find((x) => x.name === name)
}

/** A sink that has failed for longer than the doctor's threshold (five minutes). */
export function failingLong(st: LogSinkStatus | undefined, now = Date.now()): boolean {
  return !!st?.failing_since && now - Date.parse(st.failing_since) > 5 * 60_000
}
