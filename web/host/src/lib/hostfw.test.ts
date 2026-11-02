import { describe, it, expect } from 'vitest'
import { validateFw, fwSpecOf, fwFormOf, sshOpenToWorld, parsePrefix, validPort, commentError, secondsLeft, countdown, confirmWindow, fwEnableLines, type FwForm } from './hostfw'
import type { HostFirewall } from '@jailops/shared/api/types'

const good = (): FwForm => ({
  template: 'custom-rules', sshPorts: '22', sshFrom: '@admins, 2001:db8::/48', consoleFrom: '', icmp: true, logging: false,
  allow: [{ proto: 'tcp', ports: '80, 443', from: '', comment: 'web' }, { proto: 'udp', ports: '51820', from: '198.51.100.7', comment: '' }],
  sets: [{ name: 'admins', members: '192.0.2.0/24, 203.0.113.9' }],
})
const fields = (f: FwForm) => validateFw(f).map((e) => e.field)

describe('sources', () => {
  it('accepts addresses and prefixes without host bits', () => {
    for (const ok of ['192.0.2.7', '192.0.2.0/24', '0.0.0.0/0', '2001:db8::7', '2001:db8::/48', '::/0', '2001:db8::1/128'])
      expect(parsePrefix(ok).p, ok).toBeTruthy()
  })
  it('refuses what the server refuses', () => {
    for (const bad of ['', '0.0.0.0', '::', '192.0.2.1/24', '2001:db8::1/48', '192.0.2.0/33', '2001:db8::/129', '::ffff:192.0.2.1', '::ffff:192.0.2.0/120', '2001:db8::1%em0', 'example.com', '192.0.2.0/24/1', '192.0.2.0/024', '192.0.2.1\npass'])
      expect(parsePrefix(bad).p, bad).toBeUndefined()
    expect(parsePrefix('192.0.2.1/24').error).toContain('host bits')
  })
})

describe('ports and comments', () => {
  it('takes a port or a range', () => {
    for (const ok of ['1', '22', '65535', '8000-8100']) expect(validPort(ok), ok).toBe(true)
    for (const bad of ['0', '65536', '080', '8100-8000', '80-80', '-1', '1-65536', '80,443', 'ssh', '+22', ' 22']) expect(validPort(bad), bad).toBe(false)
  })
  it('keeps a comment on its line', () => {
    expect(commentError('web server, port 443')).toBeUndefined()
    expect(commentError('x'.repeat(80))).toBeUndefined()
    for (const bad of ['x'.repeat(81), 'a\nb', 'a\rb', 'say "hi"', "it's", 'a`b`', 'a\\b', 'tab\there']) expect(commentError(bad), bad).toBeDefined()
  })
})

describe('the firewall form', () => {
  it('accepts a complete form and builds the whole spec', () => {
    expect(validateFw(good())).toEqual([])
    expect(fwSpecOf(good(), 39110)).toEqual({
      template: 'custom-rules', ssh: { ports: [22], allow_from: ['@admins', '2001:db8::/48'] }, console: { port: 39110 },
      allow: [{ proto: 'tcp', ports: ['80', '443'], comment: 'web' }, { proto: 'udp', ports: ['51820'], from: ['198.51.100.7'] }],
      sets: [{ name: 'admins', members: ['192.0.2.0/24', '203.0.113.9'] }], icmp: true, nat: 'none',
    })
    // the default template sends no allow rows even when the form still holds some
    expect(fwSpecOf({ ...good(), template: 'default', allow: [] }, 0).allow).toBeUndefined()
  })
  it('names every bad field', () => {
    expect(fields({ ...good(), template: 'strict', allow: [] })).toEqual(['template'])
    expect(fields({ ...good(), template: 'default' })).toEqual(['allow'])
    expect(fields({ ...good(), template: 'open', allow: [] })).toEqual(['template'])
    expect(fields({ ...good(), sshPorts: '22 2222 2223 2224 2225' })).toEqual(['ssh-ports'])
    expect(fields({ ...good(), sshPorts: '22 22' })).toEqual(['ssh-ports'])
    expect(fields({ ...good(), sshPorts: '70000' })).toEqual(['ssh-ports'])
    expect(fields({ ...good(), sshFrom: '@nobody' })).toEqual(['ssh-from'])
    expect(fields({ ...good(), sshFrom: '192.0.2.7, 192.0.2.7/32' })).toEqual(['ssh-from'])
    expect(fields({ ...good(), consoleFrom: Array.from({ length: 33 }, (_, i) => `192.0.2.${i + 1}`).join(' ') })).toEqual(['console-from'])
    expect(fields({ ...good(), allow: [{ proto: 'icmp', ports: '1', from: '', comment: '' }] })).toEqual(['allow-0-proto'])
    expect(fields({ ...good(), allow: [{ proto: 'tcp', ports: '99999', from: '', comment: '' }] })).toEqual(['allow-0-ports'])
    expect(fields({ ...good(), allow: [{ proto: 'tcp', ports: '', from: '', comment: '' }] })).toEqual(['allow-0-ports'])
    expect(fields({ ...good(), allow: [{ proto: 'tcp', ports: Array.from({ length: 17 }, (_, i) => String(i + 1)).join(' '), from: '', comment: '' }] })).toEqual(['allow-0-ports'])
    expect(fields({ ...good(), allow: [{ proto: 'tcp', ports: '80', from: '192.0.2.1/24', comment: '' }] })).toEqual(['allow-0-from'])
    expect(fields({ ...good(), allow: [{ proto: 'tcp', ports: '80', from: '', comment: 'a "quote"' }] })).toEqual(['allow-0-comment'])
    expect(fields({ ...good(), allow: Array.from({ length: 65 }, () => ({ proto: 'tcp', ports: '80', from: '', comment: '' })) })).toEqual(['allow'])
    expect(fields({ ...good(), sets: [{ name: 'Admins', members: '192.0.2.1' }], sshFrom: '' })).toEqual(['set-0-name'])
    expect(fields({ ...good(), sets: [...good().sets, { name: 'admins', members: '192.0.2.9' }] })).toEqual(['set-1-name'])
    expect(fields({ ...good(), sets: [{ name: 'admins', members: '' }] })).toEqual(['set-0-members'])
    expect(fields({ ...good(), sets: [{ name: 'admins', members: '@other' }] })).toEqual(['set-0-members'])
    expect(fields({ ...good(), sshFrom: '', sets: Array.from({ length: 17 }, (_, i) => ({ name: `s${i}`, members: '192.0.2.1' })) })).toEqual(['sets'])
  })
  it('starts from the spec the server shows', () => {
    const h = { kind: 'HostFirewall', spec: { template: 'default', ssh: { ports: [22] }, console: { port: 39110 }, icmp: true, nat: 'none' }, status: { managed: false, config_enabled: true, helper_enabled: false, file_exists: true, file_managed: false, rules: [], console_port: 39110 } } as HostFirewall
    expect(fwFormOf(h)).toEqual({ template: 'default', sshPorts: '22', sshFrom: '', consoleFrom: '', icmp: true, logging: false, allow: [], sets: [] })
    expect(fwEnableLines(h)).toEqual({ helper: 'host-manage = firewall' })
  })
})

describe('ssh open to the world', () => {
  const spec = (f: Partial<FwForm>) => fwSpecOf({ ...good(), ...f }, 39110)
  it('is open without sources, with a /0, through a set, or with the open template', () => {
    expect(sshOpenToWorld(spec({ sshFrom: '' }))).toBe(true)
    expect(sshOpenToWorld(spec({ sshFrom: '0.0.0.0/0' }))).toBe(true)
    expect(sshOpenToWorld(spec({ sshFrom: '::/0' }))).toBe(true)
    expect(sshOpenToWorld(spec({ sshFrom: '@admins', sets: [{ name: 'admins', members: '192.0.2.0/24, 0.0.0.0/0' }] }))).toBe(true)
    expect(sshOpenToWorld(spec({ template: 'open', sshFrom: '', allow: [] }))).toBe(true)
  })
  it('is narrow with real sources', () => {
    expect(sshOpenToWorld(spec({}))).toBe(false)
    expect(sshOpenToWorld(spec({ sshFrom: '192.0.2.0/24' }))).toBe(false)
  })
})

describe('the countdown', () => {
  it('counts from what the server said, not from the browser clock', () => {
    expect(secondsLeft({ seconds_left: 120 }, 1000, 1000)).toBe(120)
    expect(secondsLeft({ seconds_left: 120 }, 1000, 11_500)).toBe(110)
    expect(secondsLeft({ seconds_left: 5 }, 0, 60_000)).toBe(0)
    expect(secondsLeft({ seconds_left: 90 }, 5000, 4000)).toBe(90)
    expect(countdown(125)).toBe('2:05')
    expect(countdown(0)).toBe('0:00')
    expect(countdown(-3)).toBe('0:00')
  })
  it('accepts windows of 10 to 1800 seconds', () => {
    expect(confirmWindow(120)).toBe(120)
    for (const bad of [0, 9, 1801, 12.5, NaN]) expect(confirmWindow(bad), String(bad)).toBeUndefined()
  })
})
