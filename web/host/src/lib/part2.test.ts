import { describe, expect, it } from 'vitest'
import { withProject, orderProjects, quotaRows, tightest, loadChoice, saveChoice } from './projectScope'
import { ALL_KEYS, getValue, setValue, rowsFor, flatten, validate, diff, expandedSources, lookupKey, checkProfile } from './configKeys'
import { Screen, keyToBytes } from './vt100'
import { remoteAddCommand, scopeValid } from './trust'
import { parseSize } from '@jailops/shared/lib/format'
import type { JailSpec, Profile } from '@jailops/shared/api/types'

const msgs = { int: 'int', size: 'size', rate: 'rate', mac: 'mac', address: 'addr', listen: 'listen', bool: 'bool', name: 'name', cpuset: 'cpuset', enum: (o: string[]) => 'enum ' + o.join('|') }
const key = (n: string) => ALL_KEYS.find((k) => k.name === n)!
const spec: JailSpec = { image: 'freebsd-15-1', resources: { cpu: 2, memory: '2G' }, network: [{ network: 'lan', ipv4: 'auto', mtu: 1500 }], volumes: [{ volume: 'data', mount: '/var/db' }], boot: { autostart: true }, packages: ['nginx'], profiles: ['web-limits'] }

describe('project scope', () => {
  it('adds ?project= only to project-level routes', () => {
    expect(withProject('/jails', 'web')).toBe('/jails?project=web')
    expect(withProject('/jails/a/state', 'web')).toBe('/jails/a/state?project=web')
    expect(withProject('/events', 'web')).toBe('/events?project=web')
    expect(withProject('/networks', 'web')).toBe('/networks')
    expect(withProject('/projects', 'web')).toBe('/projects')
    expect(withProject('/storage', 'web')).toBe('/storage')
    expect(withProject('/jails?x=1', 'web')).toBe('/jails?x=1&project=web')
    expect(withProject('/jails?project=host', 'web')).toBe('/jails?project=host')
    expect(withProject('/updates', 'web')).toBe('/updates?project=web')
    expect(withProject('', 'web')).toBe('')
    expect(withProject('/jails', '')).toBe('/jails') // no project chosen: nothing is added, the page asks for one
  })
  it('orders by name (none is special) and remembers the choice per remote', () => {
    const l = orderProjects([{ metadata: { name: 'web' } }, { metadata: { name: 'default' } }, { metadata: { name: 'api' } }])
    expect(l.map((p) => p.metadata.name)).toEqual(['api', 'default', 'web'])
    saveChoice('web', 'https://a')
    saveChoice('db', 'https://b')
    expect(loadChoice('https://a')).toBe('web')
    expect(loadChoice('https://b')).toBe('db')
    expect(loadChoice('https://c')).toBe('')
  })
  it('computes quota usage and the tightest one', () => {
    const rows = quotaRows({ quotas: { jails: 10, memory: '8G', cpu: 0 } }, { jails: 9, cpu: 5, memory_used_bytes: 2 * 1024 ** 3, addresses: 0 }, parseSize)
    expect(rows.map((r) => r.key)).toEqual(['jails', 'memory'])
    expect(tightest(rows)?.key).toBe('jails')
    expect(rows[0].pct).toBe(90)
  })
})

describe('config keys from the registry', () => {
  it('reads and writes by path, including devices', () => {
    expect(getValue(spec, key('resources.cpu'))).toBe('2')
    expect(getValue(spec, key('network.<name>.mtu'), 'eth0')).toBe('1500')
    expect(getValue(spec, key('volumes.<name>.mount'), 'data')).toBe('/var/db')
    const n = setValue(spec, key('network.<name>.mtu'), 'eth0', '1400')
    expect(n.network![0].mtu).toBe(1400)
    expect(spec.network![0].mtu).toBe(1500) // the input is not mutated
    expect(setValue(spec, key('resources.cpu'), '', '').resources?.cpu).toBeUndefined()
    expect(setValue(spec, key('packages'), '', 'a, b').packages).toEqual(['a', 'b'])
    expect(setValue(spec, key('shell'), '', '/usr/local/bin/bash') as unknown as { shell: string }).toMatchObject({ shell: '/usr/local/bin/bash' })
  })
  it('creates and removes devices', () => {
    const a = setValue(spec, key('network.<name>.network'), 'eth1', 'backend')
    expect(a.network).toHaveLength(2)
    expect(a.network![1]).toMatchObject({ name: 'eth1', network: 'backend' })
    const b = setValue(a, key('network.<name>'), 'eth1', '')
    expect(b.network).toHaveLength(1)
  })
  it('lists rows per device and flattens set keys', () => {
    const names = rowsFor(spec).map((r) => r.name)
    expect(names).toContain('network.eth0.mtu')
    expect(names).toContain('volumes.data.mount')
    expect(names).toContain('resources.memory')
    expect(flatten(spec)['boot.autostart']).toBe('true')
  })
  it('validates types and marks restart-required keys', () => {
    expect(validate(key('resources.memory'), '2G', msgs)).toBeUndefined()
    expect(validate(key('resources.memory'), 'lots', msgs)).toBe('size')
    expect(validate(key('resources.cpu'), 'x', msgs)).toBe('int')
    expect(validate(key('network.<name>.mac'), 'nope', msgs)).toBe('mac')
    expect(validate(key('update.mode'), 'weekly', msgs)).toBe('enum manual|notify|managed')
    expect(validate(key('resources.cpu'), '', msgs)).toBeUndefined()
    expect(validate(key('resources.cpuset'), '2,3', msgs)).toBeUndefined()
    expect(validate(key('resources.cpuset'), '0-3, 6', msgs)).toBeUndefined()
    expect(validate(key('resources.cpuset'), '3-', msgs)).toBe('cpuset')
    expect(validate(key('resources.cpuset'), 'a', msgs)).toBe('cpuset')
    expect(key('resources.cpuset').restart_required).toBe(true)
    expect(key('resources.cpu').restart_required).toBe(true)
    expect(key('resources.memory').restart_required).toBe(true)
    expect(key('boot.autostart').restart_required).toBe(false)
  })
  it('says which changed keys need a restart', () => {
    const d = diff(spec, setValue(setValue(spec, key('boot.priority'), '', '5'), key('resources.cpu'), '', '4'))
    expect(d.changed).toEqual(['boot.priority', 'resources.cpu'])
    expect(d.restart).toEqual(['resources.cpu'])
  })
  it('shows where each value comes from', () => {
    const profiles: Profile[] = [
      { metadata: { name: 'web-limits' }, spec: { config: { 'resources.memory': '1G', 'resources.pids': '100' } } },
      { metadata: { name: 'other' }, spec: { config: { 'resources.pids': '1' } } },
    ]
    const src = expandedSources(spec, profiles)
    expect(src['resources.pids']).toEqual({ value: '100', source: 'web-limits' }) // only listed profiles apply
    expect(src['resources.memory'].source).toBe('jail') // the jail wins
  })
  it('checks profile keys against the registry', () => {
    const text = { unknown: 'unknown', notAllowed: 'not allowed' }
    expect(lookupKey('network.eth1.mtu')?.dev).toBe('eth1')
    expect(checkProfile({ 'resources.cpu': '2' }, msgs, text)).toEqual([])
    expect(checkProfile({ 'bogus.key': '1' }, msgs, text)[0].message).toBe('unknown')
    expect(checkProfile({ hostname: 'a' }, msgs, text)[0].message).toBe('not allowed')
    expect(checkProfile({ 'resources.cpu': 'x' }, msgs, text)[0].message).toBe('int')
  })
})

describe('trust', () => {
  it('needs projects or global, never neither', () => {
    expect(scopeValid([], false)).toBe(false)
    expect(scopeValid(['a'], false)).toBe(true)
    expect(scopeValid([], true)).toBe(true)
  })
  it('builds the exact remote add command', () => {
    expect(remoteAddCommand('host.example:8443', 'jbc_x.y')).toBe('jailops remote add myhost host.example:8443 --token jbc_x.y')
  })
})

describe('VT100 subset', () => {
  it('renders text, newlines, backspace and wraps', () => {
    const s = new Screen(10, 3)
    s.write('abc\r\ndef')
    expect(s.text()).toBe('abc\ndef')
    s.write('\b\bX')
    expect(s.text()).toBe('abc\ndXf')
    const w = new Screen(4, 2)
    w.write('abcdef')
    expect(w.text()).toBe('abcd\nef')
  })
  it('scrolls into scrollback', () => {
    const s = new Screen(5, 2)
    s.write('1\r\n2\r\n3')
    expect(s.text()).toBe('2\n3')
    expect(s.allText()).toBe('1\n2\n3')
  })
  it('handles cursor movement, erase and colours; ignores unknown sequences', () => {
    const s = new Screen(10, 3)
    s.write('hello\x1b[3D\x1b[K')
    expect(s.text()).toBe('he')
    s.write('\x1b[2J\x1b[Hx\x1b[31my\x1b[0mz\x1b[?25l')
    expect(s.text()).toBe('xyz')
    expect(s.grid[0][1].fg).toBe(1)
    expect(s.grid[0][2].fg).toBe(-1)
  })
  it('decodes split UTF-8 and resizes', () => {
    const s = new Screen(5, 2)
    const bytes = new TextEncoder().encode('é')
    s.write(bytes.slice(0, 1))
    s.write(bytes.slice(1))
    expect(s.text()).toBe('é')
    s.resize(8, 3)
    expect([s.cols, s.rows]).toEqual([8, 3])
  })
  it('maps keys to bytes', () => {
    const k = (key: string, o: Partial<{ ctrlKey: boolean; altKey: boolean; metaKey: boolean }> = {}) => keyToBytes({ key, ctrlKey: false, altKey: false, metaKey: false, ...o })
    expect(k('Enter')).toBe('\r')
    expect(k('a')).toBe('a')
    expect(k('c', { ctrlKey: true })).toBe('\x03')
    expect(k('ArrowUp')).toBe('\x1b[A')
    expect(k('Shift')).toBeNull()
  })
})
