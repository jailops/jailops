import { describe, expect, it } from 'vitest'
import { applyFollow, applyUnified, emptyFollow, filterFromQuery, filterToQuery, jailColour, jailHue, lineTime, logFileOf, logQuery, pickFile, plainText, sortFiles, sourceLabel, startFrom, startUnified, stripControls, unifiedQuery, unifiedText } from './logs'
import type { JailLogFile, JailLogRead, LogsRead } from '@jailops/shared/api/types'

const read = (lines: string[], next: number, extra: Partial<JailLogRead> = {}): JailLogRead => ({ jail: 'web', file: 'messages', lines: lines.map((text) => ({ text })), next_offset: next, ...extra })

describe('stripControls', () => {
  it('removes escape sequences whole and every C0/C1 control except tab', () => {
    expect(stripControls('\x1b[31mERROR\x1b[0m done')).toBe('ERROR done')
    expect(stripControls('a\x1b]0;evil title\x07b')).toBe('ab')
    expect(stripControls('a\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\b')).toBe('alinkb')
    expect(stripControls('bell\x07 back\x08space\r\n')).toBe('bell backspace')
    expect(stripControls('c1\x9b2Jcsi \x85nel \x9dosc\x9c end')).toBe('c1csi nel  end')
    expect(stripControls('col1\tcol2')).toBe('col1\tcol2')
    expect(stripControls('\x1bcreset \x7fdel')).toBe('reset del')
    expect(stripControls('plain ünïcödé ✓')).toBe('plain ünïcödé ✓')
  })
})

describe('logQuery', () => {
  it('leaves defaults out and encodes the rest', () => {
    expect(logQuery({})).toBe('')
    expect(logQuery({ tail: 200 })).toBe('')
    expect(logQuery({ tail: 500, since: '15m', grep: 'GET /a b', offset: 1024 })).toBe('?tail=500&since=15m&grep=GET+%2Fa+b&offset=1024')
    expect(logQuery({ offset: 0, grep: '' })).toBe('')
  })
})

describe('follow', () => {
  it('starts from a tail, marks a truncated start and remembers the offset', () => {
    const st = startFrom(read(['one', 'two'], 120, { truncated: true }))
    expect(st.lines.map((l) => l.marker ?? l.text)).toEqual(['truncated', 'one', 'two'])
    expect(st.offset).toBe(120)
  })
  it('appends from next_offset and counts unseen lines only away from the end', () => {
    let st = startFrom(read(['a'], 10))
    st = applyFollow(st, read(['b', 'c'], 20), true)
    expect(st.offset).toBe(20)
    expect(st.unseen).toBe(0)
    st = applyFollow(st, read(['d'], 25), false)
    st = applyFollow(st, read(['e', 'f'], 31), false)
    expect(st.unseen).toBe(3)
    expect(plainText(st.lines)).toBe('a\nb\nc\nd\ne\nf\n')
    st = applyFollow(st, read([], 31), true)
    expect(st.unseen).toBe(0)
    expect(st.offset).toBe(31)
  })
  it('a rotation inserts a marker and continues from the start of the new file', () => {
    let st = startFrom(read(['old-1', 'old-2'], 500))
    st = applyFollow(st, read(['new-1'], 6, { rotated: true }), true)
    expect(st.lines.map((l) => l.marker ?? l.text)).toEqual(['old-1', 'old-2', 'rotated', 'new-1'])
    expect(st.offset).toBe(6)
    expect(st.rotations).toBe(1)
    st = applyFollow(st, read(['new-2'], 12), true)
    expect(st.lines.at(-1)?.text).toBe('new-2')
    expect(st.offset).toBe(12)
  })
  it('strips controls on the way in, keeps ids unique and bounds the buffer', () => {
    let st = emptyFollow()
    st = applyFollow(st, read(['\x1b[1mbold\x1b[0m'], 9), true)
    expect(st.lines[0].text).toBe('bold')
    for (let i = 0; i < 5; i++) st = applyFollow(st, read([`l${i}`, `m${i}`], 20 + i), true, 4)
    expect(st.lines.map((l) => l.text)).toEqual(['l3', 'm3', 'l4', 'm4'])
    expect(new Set(st.lines.map((l) => l.id)).size).toBe(4)
  })
})

describe('files', () => {
  const f = (name: string, kind: 'active' | 'rotated', compressed = false): JailLogFile => ({ name, kind, size: 1, modified_at: '', ...(compressed ? { compressed } : {}) })
  const files = [f('messages.1.gz', 'rotated', true), f('nginx/access.log', 'active'), f('messages.0', 'rotated'), f('auth.log', 'active'), f('messages', 'active')]
  it('picks the asked file, one below an asked directory, else messages; never a compressed one', () => {
    expect(pickFile(files, 'nginx/access.log')).toBe('nginx/access.log')
    expect(pickFile(files, 'nginx')).toBe('nginx/access.log')
    expect(pickFile(files, 'gone.log')).toBe('messages')
    expect(pickFile(files, 'messages.1.gz')).toBe('messages')
    expect(pickFile([f('x.gz', 'rotated', true)])).toBe('')
  })
  it('sorts active files first', () => {
    expect(sortFiles(files).map((x) => x.name)).toEqual(['auth.log', 'messages', 'nginx/access.log', 'messages.0', 'messages.1.gz'])
  })
  it('maps an app log path to a name below /var/log', () => {
    expect(logFileOf('/var/log/nginx/access.log')).toBe('nginx/access.log')
    expect(logFileOf('/var/log/forgejo')).toBe('forgejo')
    expect(logFileOf('/var/db/app/log.txt')).toBe('')
  })
})

describe('unified view', () => {
  const ur = (lines: Array<[string, string, string?]>, next: Record<string, number>, extra: Partial<LogsRead> = {}): LogsRead => ({
    lines: lines.map(([jail, line, time]) => ({ project: 'web', jail, file: 'messages', line, ...(time ? { time } : {}) })), sources: [], next, ...extra,
  })

  it('reads the filters from the query string, with the current project and defaults', () => {
    expect(filterFromQuery({}, 'web')).toEqual({ project: 'web', all: false, jails: [], app: '', grep: '', since: '1h', file: 'messages', appLogs: false })
    expect(filterFromQuery({}, '')).toMatchObject({ all: true, project: '' })
    expect(filterFromQuery({ project: '*', jail: ['web', 'db', 'web'], since: '', grep: 'x', file: 'auth.log', app_logs: 'true' }, 'web'))
      .toEqual({ project: '', all: true, jails: ['web', 'db'], app: '', grep: 'x', since: '', file: 'auth.log', appLogs: true })
    expect(filterFromQuery({ jail: 'web,db', since: 'yesterday', project: 'shop' }, 'web')).toMatchObject({ project: 'shop', jails: ['web', 'db'], since: '1h' })
  })

  it('writes the filters back without the defaults, so a link round-trips', () => {
    const f = filterFromQuery({ jail: 'web', since: '15m' }, 'web')
    expect(filterToQuery(f, 'web')).toEqual({ jail: ['web'], since: '15m' })
    expect(filterFromQuery(filterToQuery(f, 'web'), 'web')).toEqual(f)
    const all = filterFromQuery({ project: '*', app: 'nginx', app_logs: '1', file: 'cron' }, 'web')
    expect(filterToQuery(all, 'web')).toEqual({ project: '*', app: 'nginx', app_logs: 'true', file: 'cron' })
    expect(filterToQuery(filterFromQuery({ project: 'shop' }, 'web'), 'web')).toEqual({ project: 'shop' })
  })

  it('builds the server query, with the cursor of a follow', () => {
    const f = filterFromQuery({ jail: ['web', 'db'], grep: 'GET /a' }, 'web')
    expect(unifiedQuery(f)).toBe('?project=web&jail=web&jail=db&since=1h&grep=GET+%2Fa')
    const all = { ...f, all: true, project: '', jails: [], grep: '', since: '', appLogs: true, file: 'auth.log' }
    expect(unifiedQuery(all, { 'web/web/messages': 120 }, 5000)).toBe('?all_projects=true&app_logs=true&file=auth.log&tail=5000&cursor=%7B%22web%2Fweb%2Fmessages%22%3A120%7D')
    expect(unifiedQuery({ ...all, all: false, appLogs: false, file: 'messages' }, {})).toBe('')
  })

  it('starts from a read, keeps the failed sources and the cursors', () => {
    const st = startUnified(ur([['web', '\x1b[31mred\x1b[0m', '2026-03-01T08:58:00Z'], ['db', '  continued']], { 'web/web/messages': 10, 'web/db/messages': 4 }, {
      truncated: true, sources: [{ key: 'web/build/messages', project: 'web', jail: 'build', file: 'messages', next_offset: 0, error: { code: 'busy', message: 'jail "build" is not mounted' } }],
    }))
    expect(st.lines.map((l) => [l.jail, l.text, l.time])).toEqual([['web', 'red', '2026-03-01T08:58:00Z'], ['db', '  continued', undefined]])
    expect(st.next).toEqual({ 'web/web/messages': 10, 'web/db/messages': 4 })
    expect(st.failed.map((x) => x.jail)).toEqual(['build'])
    expect(st.truncated).toBe(true)
  })

  it('appends a follow read, merges the cursors and keeps at most max lines', () => {
    let st = startUnified(ur([['web', 'a'], ['db', 'b']], { 'web/web/messages': 2, 'web/db/messages': 2 }))
    st = applyUnified(st, ur([['web', 'c'], ['web', 'd']], { 'web/web/messages': 6 }), false, 3)
    expect(st.lines.map((l) => l.text)).toEqual(['b', 'c', 'd'])
    expect(st.next).toEqual({ 'web/web/messages': 6, 'web/db/messages': 2 })
    expect(st.unseen).toBe(2)
    expect(st.truncated).toBe(true)
    expect(new Set(st.lines.map((l) => l.id)).size).toBe(3)
    st = applyUnified(st, ur([['db', 'e']], {}), true, 3)
    expect(st.unseen).toBe(0)
    expect(st.lines.map((l) => l.text)).toEqual(['c', 'd', 'e'])
  })

  it('gives every jail a stable colour', () => {
    expect(jailColour('web')).toBe(jailColour('web'))
    expect(jailHue('db')).toBe(jailHue('db'))
    const names = ['web', 'db', 'mail', 'build', 'proxy', 'cache', 'alpine-app', 'shop']
    for (const n of names) expect(jailColour(n)).toBeGreaterThanOrEqual(0)
    expect(new Set(names.map(jailColour)).size).toBeGreaterThan(3)
  })

  it('labels and times a line', () => {
    expect(sourceLabel({ project: 'web', jail: 'db', file: 'messages' }, false)).toBe('db/messages')
    expect(sourceLabel({ project: 'web', jail: 'db', file: 'messages' }, true)).toBe('web/db/messages')
    expect(lineTime(undefined)).toBe('')
    expect(lineTime('nonsense')).toBe('')
    expect(lineTime('2026-03-01T08:58:00Z')).toMatch(/^2026-03-0[12] \d\d:58:00$/)
    expect(unifiedText([{ id: 1, project: 'web', jail: 'db', file: 'messages', text: 'x' }])).toBe('db/messages x\n')
  })
})
