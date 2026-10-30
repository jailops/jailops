import { describe, expect, it } from 'vitest'
import { blankServer, checkServer, confFileName, handshakeLabel, looksLikeToken, peerRows, reach, serverBody, splitList } from './wireguard'
import * as dw from '@/demo/wireguard'

describe('WireGuard helpers', () => {
  it('flags plain mistakes of the server form and builds the body', () => {
    expect(checkServer({ ...blankServer(), name: 'office', address: '10.99.0.1/24' })).toEqual({})
    const bad = checkServer({ ...blankServer(), name: 'Office', address: '10.99.0.1', port: '70000', endpoint: 'no port', dns: 'x' })
    expect(Object.keys(bad).sort()).toEqual(['address', 'dns', 'endpoint', 'name', 'port'])
    expect(checkServer({ ...blankServer(), name: 'a-very-long-name', address: '10.99.0.1/24' }).name).toContain('12')
    expect(checkServer({ ...blankServer(), name: 'o', address: '10.99.0.1/24, 10.98.0.1/24' }).address).toContain('one IPv4')
    expect(serverBody({ ...blankServer(), name: 'office', address: '10.99.0.1/24, fd99::1/64', networks: ['lan'], dns: '10.20.0.1' })).toEqual({
      metadata: { name: 'office' },
      spec: { mode: 'server', address: ['10.99.0.1/24', 'fd99::1/64'], listen_port: 51820, networks: ['lan'], dns: ['10.20.0.1'] },
    })
    expect(splitList(' a, b  c,')).toEqual(['a', 'b', 'c'])
  })
  it('joins the enrolled peers with what the kernel reports', () => {
    const [office, branch] = dw.tunnels().map(dw.observe)
    const rows = peerRows(office)
    expect(rows.map((r) => r.name)).toEqual(['laptop', 'phone'])
    expect(rows[0].handshake).toBeTruthy()
    expect(rows[1].handshake).toBeUndefined()
    expect(handshakeLabel(rows[1].handshake, () => 'x', 'never')).toBe('never')
    expect(handshakeLabel('0001-01-01T00:00:00Z', () => 'x', 'never')).toBe('never')
    expect(handshakeLabel(rows[0].handshake, () => '1m ago', 'never')).toBe('1m ago')
    expect(peerRows(branch)).toHaveLength(1)
    expect(reach(office)).toBe(':51820 (vpn.example.com:51820)')
    expect(reach(branch)).toBe('203.0.113.20:51820')
  })
  it('recognises a token by its shape and names the downloaded file safely', () => {
    expect(looksLikeToken('jwg1.' + 'a'.repeat(30))).toBe(true)
    expect(looksLikeToken('jot_' + 'a'.repeat(30))).toBe(false)
    expect(looksLikeToken('jwg1.short')).toBe(false)
    expect(confFileName('laptop')).toBe('laptop.conf')
    expect(confFileName('../x')).toBe('___x.conf')
  })
  it('the demo enrolls a peer with the next free address and a token that joins', () => {
    const [office] = dw.tunnels()
    const r = dw.enroll(office, 'tablet')
    expect(r.peer.allowed_ips).toEqual(['10.99.0.4/32', 'fd99::4/128'])
    expect(looksLikeToken(r.token)).toBe(true)
    expect(r.config).toContain('[Peer]')
    const c = dw.fromToken(r.token, 'home')
    expect(c?.spec).toMatchObject({ mode: 'client', interface: 'wg-home', address: ['10.99.0.4/32', 'fd99::4/128'], endpoint: 'vpn.example.com:51820' })
    expect(dw.fromToken('jwg1.!!!')).toBeNull()
    // demo keys say so and have the shape of a key
    expect(r.peer.public_key).toMatch(/^demo\d{4}A+=$/)
    expect(r.peer.public_key).toHaveLength(44)
  })
})
