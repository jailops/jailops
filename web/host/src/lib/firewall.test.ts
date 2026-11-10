import { describe, expect, it } from 'vitest'
import { detailToSetErrors, setMember, setNameError, setSpec, setTextOf, setUsers, validateSet } from './firewall'
import { s } from '@/i18n'
import type { AddressSet, FirewallPolicy } from '@jailops/shared/api/types'

describe('address set members as the server parses them', () => {
  it('takes addresses and CIDRs of both families, host bits included', () => {
    for (const a of ['192.0.2.1', '0.0.0.0', '192.0.2.0/24', '192.0.2.7/24', '10.0.0.0/0', '2001:db8::1', '::', '2001:db8:aa::/48', '::1.2.3.4', 'fe80::1/64', '2001:DB8::1']) expect(setMember(a), a).toBe(true)
  })
  it('refuses what netip refuses or the server rejects', () => {
    for (const a of ['', 'any', '@Office', 'tag:web', 'example.net', '192.0.2', '192.0.2.01', '256.0.0.1', '192.0.2.0/33', '192.0.2.0/024', '192.0.2.0/+8', '192.0.2.0/',
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
  const others = { admins: { addresses: [{ address: '198.51.100.7' }, { address: '@office' }] }, office: { addresses: [{ address: '192.0.2.128/26' }] } }
  it('splits members by lines (and commas)', () => {
    expect(setSpec('192.0.2.1\n 198.51.100.0/24 ,2001:db8::1\n\n')).toEqual({ addresses: [{ address: '192.0.2.1' }, { address: '198.51.100.0/24' }, { address: '2001:db8::1' }] })
    expect(setSpec('@office, @admins')).toEqual({ addresses: [{ address: '@office' }, { address: '@admins' }] })
  })
  it('allows an empty set, as the server does', () => {
    expect(validateSet('empty', { addresses: [] }, others, s.firewall)).toEqual({})
  })
  it('names the first member that is no address', () => {
    expect(validateSet('x', { addresses: [{ address: '192.0.2.1' }, { address: 'nope' }, { address: '@office' }] }, others, s.firewall)).toEqual({ addresses: s.firewall.setMember('nope') })
  })
  it('refuses a cycle of members, with the path, and leaves a name that is no set to the server (it may be a jail)', () => {
    expect(validateSet('x', { addresses: [{ address: '@x' }] }, others, s.firewall).addresses).toBe(s.firewall.setCycle('x -> x'))
    // office gains the member @admins, which has @office as a member
    expect(validateSet('office', { addresses: [{ address: '@admins' }] }, { admins: others.admins }, s.firewall).addresses).toBe(s.firewall.setCycle('office -> admins -> office'))
    expect(validateSet('x', { addresses: [{ address: '@ghost' }] }, others, s.firewall)).toEqual({})
    // two paths to the same set are no cycle
    expect(validateSet('x', { addresses: [{ address: '@admins' }, { address: '@office' }] }, others, s.firewall)).toEqual({})
  })
  it('puts the server message on the field its detail names', () => {
    expect(detailToSetErrors({ message: 'address set "ghost" does not exist', detail: { field: 'addresses' } })).toEqual({ addresses: 'address set "ghost" does not exist' })
    expect(detailToSetErrors({ message: 'busy', detail: { policies: 'web' } })).toEqual({})
    expect(detailToSetErrors(null)).toEqual({})
  })
})

describe('what uses an address set', () => {
  it('finds rules with @name in from or to, and sets that have it as a member', () => {
    const pol = (name: string, from: string[], to: string[] = []): FirewallPolicy => ({ metadata: { name }, spec: { rules: [{ action: 'allow', direction: 'in', from, to }] } }) as FirewallPolicy
    const set = (name: string, members: string[] = []): AddressSet => ({ metadata: { name }, spec: { addresses: members.map((address) => ({ address })) } }) as AddressSet
    const u = setUsers('office', [pol('a', ['@office']), pol('b', [], ['@office']), pol('c', ['@officex', 'office'])], [set('admins', ['@office']), set('office')])
    expect(u).toEqual({ policies: ['a', 'b'], sets: ['admins'] })
  })
})

describe('setSpec reads a comment at the end of a member line', () => {
  it('takes `# text` as the comment of the one member on the line, and setTextOf writes it back', () => {
    const text = '192.0.2.9  # the office\n198.51.100.9\n@web # the web jail\n10.0.0.1 # the lab\n10.0.0.2 # the lab, too'
    const spec = setSpec(text, 'who may connect')
    expect(spec).toEqual({
      comment: 'who may connect',
      addresses: [
        { address: '192.0.2.9', comment: 'the office' },
        { address: '198.51.100.9' },
        { address: '@web', comment: 'the web jail' },
        { address: '10.0.0.1', comment: 'the lab' },
        { address: '10.0.0.2', comment: 'the lab, too' },
      ],
    })
    expect(setTextOf(setSpec(setTextOf(spec), 'who may connect'))).toBe(setTextOf(spec))
    expect(setTextOf({ addresses: [{ address: '192.0.2.9', comment: 'the office' }, { address: '198.51.100.9' }] })).toBe('192.0.2.9  # the office\n198.51.100.9')
    expect(setSpec('192.0.2.9')).toEqual({ addresses: [{ address: '192.0.2.9' }] })
    expect(setSpec('# only a comment')).toEqual({ addresses: [] })
    // two members and a comment on one line are not an address: the member check says so
    expect(validateSet('x', setSpec('10.0.0.1, 10.0.0.2  # both'), {}, s.firewall).addresses).toBe(s.firewall.setMember('10.0.0.1, 10.0.0.2'))
    expect(setSpec('192.0.2.9', '   ')).toEqual({ addresses: [{ address: '192.0.2.9' }] })
  })
})

describe('setMember accepts a jail by name', () => {
  it('takes @jail and @jail-2, not a bad name', () => {
    expect(setMember('@web')).toBe(true)
    expect(setMember('@web-2')).toBe(true)
    expect(setMember('@Web')).toBe(false)
    expect(setMember('@web-')).toBe(false)
    expect(setMember('@')).toBe(false)
    expect(setMember('192.0.2.7')).toBe(true)
  })
})
