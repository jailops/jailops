import { describe, expect, it } from 'vitest'
import { detailToSetErrors, setMember, setNameError, setSpec, setUsers, validateSet } from './firewall'
import { s } from '@/i18n'
import type { AddressSet, FirewallPolicy } from '@jailops/shared/api/types'

describe('address set members as the server parses them', () => {
  it('takes addresses and CIDRs of both families, host bits included', () => {
    for (const a of ['192.0.2.1', '0.0.0.0', '192.0.2.0/24', '192.0.2.7/24', '10.0.0.0/0', '2001:db8::1', '::', '2001:db8:aa::/48', '::1.2.3.4', 'fe80::1/64', '2001:DB8::1']) expect(setMember(a), a).toBe(true)
  })
  it('refuses what netip refuses or the server rejects', () => {
    for (const a of ['', 'any', '@office', 'tag:web', 'example.net', '192.0.2', '192.0.2.01', '256.0.0.1', '192.0.2.0/33', '192.0.2.0/024', '192.0.2.0/+8', '192.0.2.0/',
      '2001:db8::/129', 'fe80::1%em0', '::ffff:192.0.2.1', '::ffff:c000:201/120', '1::2::3', ':1', '2001:db8::12345', '192.0.2.0/24/8']) expect(setMember(a), a).toBe(false)
  })
})

describe('address set names', () => {
  it('follows ^[a-z][a-z0-9_]{0,23}$ and is not taken', () => {
    expect(setNameError('office', [], s.firewall)).toBe('')
    expect(setNameError('ops_v6', [], s.firewall)).toBe('')
    expect(setNameError('', [], s.firewall)).toBe(s.firewall.setRequired)
    expect(setNameError('a'.repeat(25), [], s.firewall)).toBe(s.firewall.setTooLong)
    expect(setNameError('a'.repeat(24), [], s.firewall)).toBe('')
    for (const n of ['Office', 'web-1', '1abc', '_x', 'a b']) expect(setNameError(n, [], s.firewall), n).toBe(s.firewall.setBadName)
    expect(setNameError('admins', ['admins'], s.firewall)).toBe(s.firewall.setExists)
  })
})

describe('address set specs', () => {
  const others = { admins: { addresses: ['198.51.100.7'], includes: ['office'] }, office: { addresses: ['192.0.2.128/26'] } }
  it('splits members by lines (and commas) and leaves includes out when there are none', () => {
    expect(setSpec('192.0.2.1\n 198.51.100.0/24 ,2001:db8::1\n\n', '')).toEqual({ addresses: ['192.0.2.1', '198.51.100.0/24', '2001:db8::1'] })
    expect(setSpec('', 'office, admins')).toEqual({ addresses: [], includes: ['office', 'admins'] })
  })
  it('allows an empty set, as the server does', () => {
    expect(validateSet('empty', { addresses: [] }, others, s.firewall)).toEqual({})
  })
  it('names the first member that is no address', () => {
    expect(validateSet('x', { addresses: ['192.0.2.1', 'nope', '@office'] }, others, s.firewall)).toEqual({ addresses: s.firewall.setMember('nope') })
  })
  it('refuses a bad include name, a missing set and a cycle, with the path', () => {
    expect(validateSet('x', { addresses: [], includes: ['Office'] }, others, s.firewall).includes).toBe(s.firewall.setIncludeName('Office'))
    expect(validateSet('x', { addresses: [], includes: ['ghost'] }, others, s.firewall).includes).toBe(s.firewall.setMissing('ghost'))
    expect(validateSet('x', { addresses: [], includes: ['x'] }, others, s.firewall).includes).toBe(s.firewall.setCycle('x -> x'))
    // office gains an include of admins, which includes office
    expect(validateSet('office', { addresses: [], includes: ['admins'] }, { admins: others.admins }, s.firewall).includes).toBe(s.firewall.setCycle('office -> admins -> office'))
    // two paths to the same set are no cycle
    expect(validateSet('x', { addresses: [], includes: ['admins', 'office'] }, others, s.firewall)).toEqual({})
  })
  it('puts the server message on the field its detail names', () => {
    expect(detailToSetErrors({ message: 'address set "ghost" does not exist', detail: { field: 'includes' } })).toEqual({ includes: 'address set "ghost" does not exist' })
    expect(detailToSetErrors({ message: 'busy', detail: { policies: 'web' } })).toEqual({})
    expect(detailToSetErrors(null)).toEqual({})
  })
})

describe('what uses an address set', () => {
  it('finds rules with @name in from or to, and sets that include it', () => {
    const pol = (name: string, from: string[], to: string[] = []): FirewallPolicy => ({ metadata: { name }, spec: { rules: [{ action: 'allow', direction: 'in', from, to }] } }) as FirewallPolicy
    const set = (name: string, includes?: string[]): AddressSet => ({ metadata: { name }, spec: { addresses: [], includes } }) as AddressSet
    const u = setUsers('office', [pol('a', ['@office']), pol('b', [], ['@office']), pol('c', ['@officex', 'office'])], [set('admins', ['office']), set('office')])
    expect(u).toEqual({ policies: ['a', 'b'], sets: ['admins'] })
  })
})
