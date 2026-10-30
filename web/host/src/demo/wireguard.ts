// Demo WireGuard tunnels and the routes of /wireguard. Keys are made from a counter and say "demo" so nobody takes
// them for real ones; a peer's token is built in the server's format (jwg1.<base64url(JSON)>).
import type { WireGuard, WireGuardPeerCreated } from '@jailops/shared/api/types'

const now = Date.now()
const ago = (s: number) => new Date(now - s * 1000).toISOString()
const meta = (name: string) => ({ name, scope: 'fleet' as const, generation: 3, created_at: ago(86400 * 9), updated_at: ago(3600) })

let keyN = 0
/** A deterministic, plainly fake key of the WireGuard shape (44 characters). */
export function demoKey(): string {
  keyN++
  return `demo${String(keyN).padStart(4, '0')}`.padEnd(43, 'A') + '='
}

export function tunnels(): WireGuard[] {
  keyN = 0
  return [
    {
      metadata: meta('office'),
      spec: {
        mode: 'server', interface: 'wg-office', address: ['10.99.0.1/24', 'fd99::1/64'], listen_port: 51820, endpoint: 'vpn.example.com:51820',
        allowed_ips: ['10.99.0.0/24', 'fd99::/64'], networks: ['lan'], dns: ['10.20.0.1'],
        peers: [
          { name: 'laptop', public_key: demoKey(), allowed_ips: ['10.99.0.2/32', 'fd99::2/128'], created: ago(86400 * 8) },
          { name: 'phone', public_key: demoKey(), allowed_ips: ['10.99.0.3/32', 'fd99::3/128'], created: ago(86400 * 2) },
        ],
      },
      status: { public_key: demoKey(), interface: 'wg-office', up: true, listen_port: 51820, firewall_rule: 'pass in on $ext_if proto udp to port 51820', peers: [], conditions: [{ type: 'Ready', status: 'True', reason: 'Up' }] },
    },
    {
      metadata: meta('branch'),
      spec: { mode: 'client', interface: 'wg-branch', address: ['10.98.0.2/32'], endpoint: '203.0.113.20:51820', peer_public_key: demoKey(), allowed_ips: ['10.98.0.0/24', '10.40.0.0/24'], keepalive: 25 },
      status: { public_key: demoKey(), interface: 'wg-branch', up: true, listen_port: 40001, peers: [], conditions: [{ type: 'Ready', status: 'True', reason: 'Up' }] },
    },
  ]
}

/** The kernel's view: the first peer of a server and every client have shaken hands. */
export function observe(w: WireGuard): WireGuard {
  const o = structuredClone(w)
  o.status.up = !o.spec.down
  if (!o.status.up) {
    o.status.peers = []
    return o
  }
  if (o.spec.mode === 'client') {
    o.status.peers = [{ public_key: o.spec.peer_public_key ?? '', endpoint: o.spec.endpoint, allowed_ips: o.spec.allowed_ips, latest_handshake: ago(17), rx_bytes: 52_428_800, tx_bytes: 7_340_032, preshared_key: true }]
  } else {
    o.status.peers = (o.spec.peers ?? []).map((p, i) => (i === 0
      ? { name: p.name, public_key: p.public_key, endpoint: '198.51.100.23:51413', allowed_ips: p.allowed_ips, latest_handshake: ago(95), rx_bytes: 1_572_864, tx_bytes: 9_961_472, preshared_key: true }
      : { name: p.name, public_key: p.public_key, allowed_ips: p.allowed_ips, rx_bytes: 0, tx_bytes: 0, preshared_key: true }))
  }
  return o
}

const b64url = (s: string) => btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')

/** The next free host address of each pool of a server. */
function nextAddresses(w: WireGuard): string[] {
  const used = new Set<string>([...w.spec.address, ...(w.spec.peers ?? []).flatMap((p) => p.allowed_ips)].map((a) => a.split('/')[0]))
  const out: string[] = []
  for (const pool of w.spec.allowed_ips ?? []) {
    const [base, bits] = pool.split('/')
    if (base.includes(':')) {
      for (let i = 2; i < 65536; i++) { const a = `${base.replace(/::$/, '')}::${i.toString(16)}`; if (!used.has(a)) { out.push(`${a}/128`); break } }
    } else {
      const p = base.split('.').map(Number)
      const size = 2 ** (32 - Number(bits))
      for (let i = 2; i < size - 1; i++) { const a = `${p[0]}.${p[1]}.${p[2]}.${p[3] + i}`; if (!used.has(a)) { out.push(`${a}/32`); break } }
    }
  }
  return out
}

/** Enrolls a peer: a demo key pair, a token and a configuration file. */
export function enroll(w: WireGuard, name: string, address?: string[]): WireGuardPeerCreated {
  const addrs = address?.length ? address.map((a) => (a.includes('/') ? a : `${a}/${a.includes(':') ? 128 : 32}`)) : nextAddresses(w)
  const priv = demoKey()
  const psk = demoKey()
  const peer = { name, public_key: demoKey(), allowed_ips: addrs, created: new Date().toISOString() }
  w.spec.peers = [...(w.spec.peers ?? []), peer]
  const routes = [...(w.spec.allowed_ips ?? [])]
  const token = 'jwg1.' + b64url(JSON.stringify({ name, endpoint: w.spec.endpoint, server_public_key: w.status.public_key, private_key: priv, preshared_key: psk, address: addrs, allowed_ips: routes, dns: w.spec.dns, keepalive: 25 }))
  const config = `# WireGuard configuration of ${name}, made by JailOps. It holds a private key: keep it secret.\n[Interface]\nPrivateKey = ${priv}\nAddress = ${addrs.join(', ')}\n\n[Peer]\nPublicKey = ${w.status.public_key}\nPresharedKey = ${psk}\nEndpoint = ${w.spec.endpoint}\nAllowedIPs = ${routes.join(', ')}\nPersistentKeepalive = 25\n`
  return { peer, token, config, warning: 'this token holds the peer\'s private key and the preshared key: it is shown once, is not stored on this host, and gives whoever holds it access to the tunnel; pass it on over a channel you trust' }
}

/** A client tunnel from a token in the server's format, or null when it is not one. */
export function fromToken(token: string, name?: string): WireGuard | null {
  const t = token.trim()
  if (!t.startsWith('jwg1.')) return null
  try {
    const j = JSON.parse(atob(t.slice(5).replace(/-/g, '+').replace(/_/g, '/')))
    if (!j.name || !j.private_key || !j.server_public_key) return null
    const n = name || j.name
    return {
      metadata: { name: n, scope: 'fleet', generation: 1, created_at: new Date().toISOString() },
      spec: { mode: 'client', interface: `wg-${n}`, address: j.address ?? [], endpoint: j.endpoint, peer_public_key: j.server_public_key, allowed_ips: j.allowed_ips ?? [], dns: j.dns, keepalive: j.keepalive ?? 25 },
      status: { public_key: demoKey(), interface: `wg-${n}`, up: true, peers: [], conditions: [{ type: 'Ready', status: 'True', reason: 'Up' }] },
    }
  } catch {
    return null
  }
}
