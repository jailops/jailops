import { describe, it, expect } from 'vitest'
import { hostAddr, validDNSName, validateForm, specOf, formOf, diffLines, enableLines, type HostForm } from './host'
import type { HostIdentity } from '@jailops/shared/api/types'

const good = (): HostForm => ({ hostname: 'host1.example.com', nameservers: ['192.0.2.53', '2001:db8::53'], search: ['example.com'], ntpServers: ['ntp.example.org', '198.51.100.7'], ntpEnable: true })
const fields = (f: HostForm) => validateForm(f).map((e) => e.field)

describe('host addresses', () => {
  it('accepts IPv4 and IPv6 literals that name a host', () => {
    expect(hostAddr('192.0.2.53')).toBe('192.0.2.53')
    expect(hostAddr('2001:db8::53')).toBe('2001:db8:0:0:0:0:0:53')
    expect(hostAddr('2001:0db8:0:0:0:0:0:0053')).toBe(hostAddr('2001:db8::53'))
    expect(hostAddr('::1')).not.toBeNull()
    expect(hostAddr('64:ff9b::192.0.2.1')).toBe('64:ff9b:0:0:0:0:c000:201')
  })
  it('refuses what the server refuses', () => {
    for (const bad of ['', '0.0.0.0', '::', '::ffff:192.0.2.1', '2001:db8::1%em0', '192.0.2.256', '192.0.2', '192.0.02.1', '2001:db8::53::1', '1:2:3:4:5:6:7:8:9', 'example.com', '192.0.2.53\nnameserver 198.51.100.1', ' 192.0.2.53'])
      expect(hostAddr(bad), bad).toBeNull()
  })
})

describe('host names', () => {
  it('follows RFC 1123', () => {
    expect(validDNSName('host1')).toBe(true)
    expect(validDNSName('host-1.example.com')).toBe(true)
    expect(validDNSName('a'.repeat(63) + '.example.com')).toBe(true)
    for (const bad of ['', '-host', 'host-', 'a'.repeat(64), 'host..example.com', 'host.example.com.', 'host_1', 'host 1', 'host;rm', '192.0.2.1', '2001:db8::1', 'host\nexample', ('a'.repeat(62) + '.').repeat(4) + 'abc'])
      expect(validDNSName(bad), bad).toBe(false)
  })
})

describe('the identity form', () => {
  it('accepts a complete form and builds the whole spec', () => {
    expect(validateForm(good())).toEqual([])
    expect(specOf({ ...good(), nameservers: [' 192.0.2.53 ', ''], search: [''] })).toEqual({
      hostname: 'host1.example.com', resolvers: { nameservers: ['192.0.2.53'] }, ntp: { servers: ['ntp.example.org', '198.51.100.7'], enable: true },
    })
  })
  it('names every bad field', () => {
    expect(fields({ ...good(), hostname: '' })).toEqual(['hostname'])
    expect(fields({ ...good(), hostname: 'bad_name' })).toEqual(['hostname'])
    expect(fields({ ...good(), nameservers: [] })).toEqual(['nameservers'])
    expect(fields({ ...good(), nameservers: ['192.0.2.1', '192.0.2.2', '192.0.2.3', '192.0.2.4'] })).toEqual(['nameservers'])
    expect(fields({ ...good(), nameservers: ['192.0.2.53', 'dns.example.com'] })).toEqual(['nameserver-1'])
    expect(fields({ ...good(), nameservers: ['2001:db8::53', '2001:0db8::0053'] })).toEqual(['nameserver-1'])
    expect(fields({ ...good(), search: ['a.example.com', 'A.example.com'] })).toEqual(['search-1'])
    expect(fields({ ...good(), search: Array.from({ length: 7 }, (_, i) => `d${i}.example.com`) })).toEqual(['search'])
    expect(fields({ ...good(), search: Array.from({ length: 5 }, (_, i) => `${'a'.repeat(50)}${i}.example.com`) })).toEqual(['search'])
    expect(fields({ ...good(), ntpServers: ['ntp.example.org', 'NTP.example.org'] })).toEqual(['ntp-1'])
    expect(fields({ ...good(), ntpServers: ['bad name'] })).toEqual(['ntp-0'])
    expect(fields({ ...good(), ntpServers: ['a.example.org', 'b.example.org', 'c.example.org', 'd.example.org', 'e.example.org'] })).toEqual(['ntp'])
  })
  it('starts from what the host has', () => {
    const h = { kind: 'HostIdentity', spec: { hostname: 'h.example.com', resolvers: { nameservers: ['192.0.2.53'] }, ntp: { servers: [], enable: false } }, status: { managed: false, config_enabled: true, helper_enabled: false, resolv_conf_managed: false, ntp_block: false } } as HostIdentity
    expect(formOf(h)).toEqual({ hostname: 'h.example.com', nameservers: ['192.0.2.53'], search: [], ntpServers: [], ntpEnable: false })
    expect(enableLines(h)).toEqual({ helper: 'host-manage = identity' })
    expect(enableLines({ ...h, status: { ...h.status, config_enabled: false } }).config).toContain('modules: [identity]')
  })
})

describe('diff lines', () => {
  it('classifies a unified diff', () => {
    const d = '--- /etc/resolv.conf\n+++ /etc/resolv.conf\n@@ -1,2 +1,2 @@\n # managed by jailops\n-nameserver 192.0.2.1\n+nameserver 192.0.2.53\n'
    expect(diffLines(d).map((l) => l.kind)).toEqual(['file', 'file', 'hunk', 'ctx', 'del', 'add'])
    expect(diffLines('')).toEqual([])
  })
})
