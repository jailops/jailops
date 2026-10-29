// The jail log viewer's pure parts: the query string, control-character stripping and the follow
// state (offsets, rotation, the bounded buffer). The component only adds timers and the DOM.
import type { JailLogFile, JailLogRead } from '@jailops/shared/api/types'

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
