// The jail log viewer's pure parts: the query string, control-character stripping and the follow
// state (offsets, rotation, the bounded buffer). The component only adds timers and the DOM.
import type { JailLogFile, JailLogRead, LogsRead, LogSource } from '@jailops/shared/api/types'

/** The query of GET /jails/{n}/logs/{file}; defaults are left out, as the server documents them. */
export interface LogQuery { tail?: number; since?: string; grep?: string; offset?: number }

export const DEFAULT_TAIL = 200
export const MAX_TAIL = 5000
export const TAILS = [100, 200, 500, 1000, 5000]
export const SINCE = ['', '15m', '1h', '1d'] as const
/** Lines kept in the browser while following; the oldest go first. */
export const MAX_LINES = 10_000
export const FOLLOW_MS = 1000

export function logQuery(q: LogQuery): string {
  const p = new URLSearchParams()
  if (q.tail && q.tail !== DEFAULT_TAIL) p.set('tail', String(q.tail))
  if (q.since) p.set('since', q.since)
  if (q.grep) p.set('grep', q.grep)
  if (q.offset && q.offset > 0) p.set('offset', String(q.offset))
  const s = p.toString()
  return s ? `?${s}` : ''
}

// Escape sequences go whole (CSI, OSC, the other ESC forms), then every remaining C0 and C1 control except tab,
// so a log line can never move a cursor, set a title or hide text. The browser only ever gets plain text.
const CSI = /(?:\x1b\[|\x9b)[0-?]*[ -/]*[@-~]/g
const OSC = /(?:\x1b\]|\x9d)[^\x07\x1b\x9c]*(?:\x07|\x1b\\|\x9c)?/g
const ESC2 = /\x1b[ -/]*[0-~]/g
const CTRL = /[\x00-\x08\x0a-\x1f\x7f-\x9f]/g

export function stripControls(text: string): string {
  return text.replace(CSI, '').replace(OSC, '').replace(ESC2, '').replace(CTRL, '')
}

export interface ViewLine { id: number; text: string; n?: number; marker?: 'rotated' | 'truncated' }

export interface FollowState {
  lines: ViewLine[]
  /** Where the next read continues: the next_offset of the last reply. */
  offset: number
  seq: number
  /** Lines appended while the reader was scrolled away from the end. */
  unseen: number
  rotations: number
}

export function emptyFollow(): FollowState {
  return { lines: [], offset: 0, seq: 0, unseen: 0, rotations: 0 }
}

/** The first read (a tail): replaces the buffer. */
export function startFrom(r: JailLogRead): FollowState {
  const st = emptyFollow()
  if (r.truncated) st.lines.push({ id: ++st.seq, text: '', marker: 'truncated' })
  for (const l of r.lines ?? []) st.lines.push({ id: ++st.seq, text: stripControls(l.text), n: l.n })
  st.offset = r.next_offset
  return st
}

/**
 * A follow read (offset = st.offset): appends what was written since. A rotated file starts over from the
 * beginning of the new file, so the reply is marked and its lines follow the marker. `atEnd` says whether the
 * reader sits at the end; otherwise the new lines are counted for the "new lines" button.
 */
export function applyFollow(st: FollowState, r: JailLogRead, atEnd: boolean, max = MAX_LINES): FollowState {
  const lines = [...st.lines]
  let seq = st.seq
  let added = 0
  let rotations = st.rotations
  if (r.rotated) {
    rotations++
    lines.push({ id: ++seq, text: '', marker: 'rotated' })
  }
  for (const l of r.lines ?? []) {
    lines.push({ id: ++seq, text: stripControls(l.text), n: l.n })
    added++
  }
  const drop = Math.max(0, lines.length - max)
  return { lines: drop ? lines.slice(drop) : lines, offset: r.next_offset, seq, unseen: atEnd ? 0 : st.unseen + added, rotations }
}

/** The file to open first: the asked one, else one below the asked directory, else messages, else the first readable. */
export function pickFile(files: JailLogFile[], want = ''): string {
  const ok = files.filter((f) => !f.compressed)
  if (want) {
    const exact = ok.find((f) => f.name === want)
    if (exact) return exact.name
    const below = ok.find((f) => f.name.startsWith(want.replace(/\/$/, '') + '/') && f.kind === 'active')
    if (below) return below.name
  }
  return (ok.find((f) => f.name === 'messages') ?? ok.find((f) => f.kind === 'active') ?? ok[0])?.name ?? ''
}

/** Active files first, then by name, so the rotated copies sit under the file they came from. */
export function sortFiles(files: JailLogFile[]): JailLogFile[] {
  return [...files].sort((a, b) => (a.kind === b.kind ? a.name.localeCompare(b.name, undefined, { numeric: true }) : a.kind === 'active' ? -1 : 1))
}

/** A log path of an app definition as a name below /var/log, or '' when it lies elsewhere (the viewer reads /var/log only). */
const VAR_LOG = '/var/log/'
export function logFileOf(path: string): string {
  return path.startsWith(VAR_LOG) ? path.slice(VAR_LOG.length) : ''
}

/** The text of the buffer for copy and download: markers are left out. */
export function plainText(lines: ViewLine[]): string {
  return lines.filter((l) => !l.marker).map((l) => l.text).join('\n') + '\n'
}

// ---- the unified view (/logs): many jails merged into one stream by the server ----

/** The filters of the Logs page. `all` reads every project the identity may use instead of `project`. */
export interface UnifiedFilter { project: string; all: boolean; jails: string[]; app: string; grep: string; since: string; file: string; appLogs: boolean }
export const UNIFIED_SINCE = ['15m', '1h', '6h', '1d', ''] as const
export const DEFAULT_FILE = 'messages'
/** Lines kept while following the merged stream; the oldest go first. */
export const MAX_UNIFIED = 5000
export const UNIFIED_FOLLOW_MS = 2000

const str = (v: unknown): string => (typeof v === 'string' ? v : Array.isArray(v) && typeof v[0] === 'string' ? v[0] : '')
const strs = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : typeof v === 'string' ? [v] : []).flatMap((x) => x.split(',')).map((x) => x.trim()).filter(Boolean)

/** The filters from the page's query string (`/logs?jail=web&since=1h`); without a project the current one, else every project. */
export function filterFromQuery(q: Record<string, unknown>, current: string): UnifiedFilter {
  const all = str(q.project) === '*' || (!str(q.project) && !current)
  const since = str(q.since)
  return {
    project: all ? '' : str(q.project) || current,
    all,
    jails: [...new Set(strs(q.jail))],
    app: str(q.app),
    grep: str(q.grep),
    since: q.since === undefined ? '1h' : (UNIFIED_SINCE as readonly string[]).includes(since) ? since : '1h',
    file: str(q.file) || DEFAULT_FILE,
    appLogs: str(q.app_logs) === 'true' || str(q.app_logs) === '1',
  }
}

/** The page's query string for the filters: what equals the default is left out, so links stay short. */
export function filterToQuery(f: UnifiedFilter, current: string): Record<string, string | string[]> {
  const q: Record<string, string | string[]> = {}
  if (f.all) q.project = '*'
  else if (f.project && f.project !== current) q.project = f.project
  if (f.jails.length) q.jail = [...f.jails]
  if (f.app) q.app = f.app
  if (f.grep) q.grep = f.grep
  if (f.since !== '1h') q.since = f.since
  if (f.file && f.file !== DEFAULT_FILE) q.file = f.file
  if (f.appLogs) q.app_logs = 'true'
  return q
}

/** The query of GET /logs. A cursor (the `next` of the previous reply) continues a follow. */
export function unifiedQuery(f: UnifiedFilter, cursor?: Record<string, number>, tail?: number): string {
  const p = new URLSearchParams()
  if (f.all) p.set('all_projects', 'true')
  else if (f.project) p.set('project', f.project)
  for (const j of f.jails) p.append('jail', j)
  if (f.app) p.set('app', f.app)
  if (f.appLogs) p.set('app_logs', 'true')
  if (f.file && f.file !== DEFAULT_FILE) p.set('file', f.file)
  if (tail && tail !== DEFAULT_TAIL) p.set('tail', String(tail))
  if (f.since) p.set('since', f.since)
  if (f.grep) p.set('grep', f.grep)
  if (cursor && Object.keys(cursor).length) p.set('cursor', JSON.stringify(cursor))
  const s = p.toString()
  return s ? `?${s}` : ''
}

export interface UnifiedLine { id: number; time?: string; project: string; jail: string; file: string; text: string }

export interface UnifiedState {
  lines: UnifiedLine[]
  /** Where each source continues: the `next` map of the last reply, sent back as the cursor. */
  next: Record<string, number>
  /** The sources of the last reply that could not be read (not mounted, no such file). */
  failed: LogSource[]
  seq: number
  truncated: boolean
  unseen: number
}

export function emptyUnified(): UnifiedState {
  return { lines: [], next: {}, failed: [], seq: 0, truncated: false, unseen: 0 }
}

function addLines(lines: UnifiedLine[], seq: number, r: LogsRead): number {
  for (const l of r.lines ?? []) lines.push({ id: ++seq, time: l.time, project: l.project, jail: l.jail, file: l.file, text: stripControls(l.line) })
  return seq
}

/** The first read: replaces the buffer. */
export function startUnified(r: LogsRead): UnifiedState {
  const st = emptyUnified()
  st.seq = addLines(st.lines, 0, r)
  st.next = { ...(r.next ?? {}) }
  st.failed = (r.sources ?? []).filter((x) => x.error)
  st.truncated = !!r.truncated
  return st
}

/** A follow read: appends what every source wrote since, keeps at most `max` lines and merges the cursors. */
export function applyUnified(st: UnifiedState, r: LogsRead, atEnd: boolean, max = MAX_UNIFIED): UnifiedState {
  const lines = [...st.lines]
  const before = lines.length
  const seq = addLines(lines, st.seq, r)
  const added = lines.length - before
  const drop = Math.max(0, lines.length - max)
  return {
    lines: drop ? lines.slice(drop) : lines,
    next: { ...st.next, ...(r.next ?? {}) },
    failed: (r.sources ?? []).filter((x) => x.error),
    seq,
    truncated: st.truncated || drop > 0,
    unseen: atEnd ? 0 : st.unseen + added,
  }
}

/** Hues that stay apart from each other; a jail always gets the same one. */
const HUES = [212, 152, 28, 280, 340, 190, 95, 250, 0, 55]

/** A stable colour index for a jail name (FNV-1a), so a jail keeps its colour across reloads and pages. */
export function jailColour(name: string): number {
  let h = 0x811c9dc5
  for (let i = 0; i < name.length; i++) {
    h ^= name.charCodeAt(i)
    h = Math.imul(h, 0x01000193) >>> 0
  }
  return h % HUES.length
}

export function jailHue(name: string): number {
  return HUES[jailColour(name)]
}

/** The label of a line's source: jail/file, with the project when lines of several projects are shown. */
export function sourceLabel(l: { project: string; jail: string; file: string }, withProject: boolean): string {
  return `${withProject ? `${l.project}/` : ''}${l.jail}/${l.file}`
}

/** The time column: the server's timestamp in the reader's clock, or '' for a line that has none. */
export function lineTime(iso: string | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const two = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${two(d.getMonth() + 1)}-${two(d.getDate())} ${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`
}

/** The text of the merged buffer for copy and download. */
export function unifiedText(lines: UnifiedLine[]): string {
  return lines.map((l) => `${l.jail}/${l.file} ${l.text}`).join('\n') + '\n'
}
