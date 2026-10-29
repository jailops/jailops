// Canned data for `?demo`. Never imported by the production entry path except through a dynamic
// import, and only used by the in-process demo transport.
import type {
  Jail, JailSnapshot, Image, Network, Lease, FirewallPolicy, AddressSet, BackupTarget, BackupPolicy, Backup,
  Remote, Token, User, Certificate, AuditEntry, Operation, ServerInfo, PoolUsage, PowerState,
} from '@jailops/shared/api/types'
import { knownExtensions } from '@jailops/shared/api/types'
import { appHash } from './apps'

const now = Date.now()
const ago = (sec: number) => new Date(now - sec * 1000).toISOString()
const GiB = 1024 ** 3
const MiB = 1024 ** 2

const meta = (name: string, createdAgo = 86400 * 30, extra: Record<string, unknown> = {}) => ({
  name, uuid: `0d3f${name.length}a1c-0000-4000-8000-${(name + '000000000000').slice(0, 12).replace(/[^a-z0-9]/g, '0')}`, scope: 'host' as const, generation: 3,
  created_at: ago(createdAgo), updated_at: ago(createdAgo / 2), ...extra,
})

export function server(): ServerInfo {
  return {
    api_version: '1.0', api_extensions: [...knownExtensions], version: '0.1.0-demo', host_uuid: '6a1f0c52-2f0e-4f0b-8a5c-0123456789ab',
    hostname: 'bsd01.example.net', role: 'host', hub: true, auth: 'trusted',
    environment: { os: 'FreeBSD', kernel_version: '15.1-RELEASE', arch: 'amd64', zfs_version: '2.3.0', pool: 'tank', pf: true, linuxulator: true },
  }
}

export function pool(): PoolUsage {
  return { name: 'tank', size: 1800 * GiB, allocated: 1290 * GiB, free: 510 * GiB, health: 'ONLINE' }
}

function jail(name: string, power: PowerState, o: { image?: string; gen?: string; ipv4?: string; ipv6?: string; net?: string; policy?: string; drift?: string[]; cond?: Jail['status']['conditions']; autostart?: boolean; vols?: Jail['spec']['volumes']; pinned?: boolean; labels?: Record<string, string> } = {}): Jail {
  const net = o.net ?? 'lan'
  return {
    metadata: { ...meta(name, 86400 * 40, { labels: o.labels ?? { role: name } }), project: 'web' },
    spec: {
      image: o.image ?? 'freebsd-15@stable', storage: 'default', layout: 'thin', hostname: `${name}.example.net`,
      update: { strategy: 'rebase', window: 'sun 03:00', healthcheck: '/usr/local/bin/check' },
      resources: { cpu: 2, memory: '2G', disk: '20G', pids: 2048 },
      network: [{ network: net, ipv4: o.ipv4 ?? 'auto', ipv6: o.ipv6 ?? 'none', mtu: 1500 }],
      firewall: { policy: o.policy ?? 'web-default', tags: ['managed'] },
      volumes: o.vols ?? [],
      boot: { autostart: o.autostart ?? true, priority: 10 },
      packages: ['nginx'], encrypted: false,
    },
    status: {
      power, jid: power === 'running' ? 12 + name.length : undefined, generation: o.gen ?? 'freebsd-15#g4',
      addresses: power === 'running' ? [(o.ipv4 && o.ipv4 !== 'auto' ? o.ipv4 : '10.20.0.' + (10 + name.length)) + '/24'] : [],
      started_at: power === 'running' ? ago(86400 * 3 + name.length * 600) : undefined, drift: o.drift ?? [], conditions: o.cond ?? [{ type: 'Reconciled', status: 'True', since: ago(3600) }],
    },
  }
}

export function jails(): Jail[] {
  return [
    jail('web', 'running', { ipv4: '10.20.0.11', vols: [{ name: 'content', mount: '/usr/local/www', quota: '10G' }], labels: { role: 'web', tier: 'frontend', 'jailops.app': 'nginx', 'jailops.app.version': '1.30.6,3', 'jailops.app.hash': appHash('nginx') } }),
    jail('db', 'running', { ipv4: '10.20.0.12', policy: 'db-internal', net: 'backend', vols: [{ name: 'pgdata', mount: '/var/db/postgres', quota: '200G' }, { name: 'wal', mount: '/var/db/wal', quota: '40G', read_only: false }], labels: { role: 'db', 'jailops.app': 'postgresql', 'jailops.app.version': '18.5', 'jailops.app.hash': appHash('postgresql-previous') } }),
    jail('mail', 'error', { image: 'freebsd-15#g3', gen: 'freebsd-15#g3', drift: ['rctl: memoryuse limit 2G rendered, live 512M', 'pf anchor jailops/mail is not loaded'], cond: [{ type: 'Reconciled', status: 'False', reason: 'ApplyFailed', message: 'pfctl: anchor load failed: syntax error at line 14', since: ago(900) }], policy: 'mail-edge', vols: [{ name: 'spool', mount: '/var/spool', quota: '50G' }] }),
    jail('build', 'stopped', { autostart: false, labels: { role: 'ci' } }),
    jail('proxy', 'running', { net: 'edge', ipv4: '192.0.2.10', ipv6: 'auto', labels: { role: 'proxy' } }),
    jail('cache', 'starting', { ipv4: '10.20.0.14' }),
    jail('alpine-app', 'running', { image: 'alpine-3.22@stable', gen: 'alpine-3.22#g1', ipv4: 'auto', drift: ['address 10.20.0.30 configured, spec says auto (allocation pending write-back)'] }),
  ]
}

export function snapshots(): Record<string, JailSnapshot[]> {
  const mk = (jailName: string, items: Array<[string, number, string, number]>): JailSnapshot[] =>
    items.map(([name, age, comment, written]) => ({
      metadata: { name, scope: 'host', created_at: ago(age) }, spec: { jail: jailName, comment }, status: { written },
    }))
  return {
    web: mk('web', [['pre-upgrade-g4', 86400 * 2, 'before upgrade to g4', 840 * MiB], ['daily-0', 3600 * 9, 'scheduled', 96 * MiB], ['daily-1', 86400 + 3600 * 9, 'scheduled', 2.4 * GiB], ['daily-2', 2 * 86400 + 3600 * 9, 'scheduled', 5.1 * GiB]]),
    db: mk('db', [['hourly-0', 1800, 'scheduled', 310 * MiB], ['pre-migration', 86400 * 4, 'before schema migration', 48 * GiB]]),
    mail: mk('mail', [['daily-0', 3600 * 8, 'scheduled', 12 * MiB]]),
  }
}

export function images(): Image[] {
  const gen = (n: number, parent: number, pk: number, ageDays: number, refs: string[]) => ({
    number: n, name: `freebsd-15#g${n}`, parent: parent || undefined, manifest: `b3f0${n}c9d2e17a4455e0e1f${n}`.padEnd(40, 'a'), packages: pk, dataset: `tank/jailops/images/freebsd-15/g${n}`, created_at: ago(86400 * ageDays), refs,
  })
  return [
    {
      metadata: { ...meta('freebsd-15'), scope: 'fleet' },
      spec: { os: 'freebsd', release: '15.1-RELEASE', arch: 'amd64', source: { type: 'pkgbase', repo: 'https://pkg.freebsd.org/FreeBSD:15:amd64/base_release_1' }, aliases: ['stable'], retention: { keep_last: 3, keep_days: 14 } },
      status: { generations: [gen(1, 0, 212, 60, []), gen(2, 1, 214, 45, []), gen(3, 2, 219, 20, ['mail']), gen(4, 3, 221, 3, ['web', 'db', 'proxy', 'build', 'cache'])], aliases: { stable: 4, latest: 4 } },
    },
    {
      metadata: { ...meta('alpine-3.22'), scope: 'fleet' },
      spec: { os: 'linux', release: '3.22', arch: 'amd64', source: { type: 'minirootfs', repo: 'https://dl-cdn.alpinelinux.org/alpine/v3.22/releases/x86_64' }, aliases: ['stable'], retention: { keep_last: 2 } },
      status: { generations: [{ number: 1, name: 'alpine-3.22#g1', manifest: 'a9c1d0'.padEnd(40, '7'), packages: 18, created_at: ago(86400 * 9), refs: ['alpine-app'] }], aliases: { stable: 1 } },
    },
  ]
}

export function networks(): Network[] {
  return [
    {
      metadata: meta('lan'), spec: { bridge: 'jo0', uplink: 'em0', ipv4: { subnet: '10.20.0.0/24', gateway: '10.20.0.1', range: '10.20.0.10-10.20.0.60', exclude: ['10.20.0.40-10.20.0.44'], nat: true }, ipv6: { mode: 'routed', subnet: '2001:db8:20:1::/80', gateway: '2001:db8:20:1::1', range: '2001:db8:20:1::100-2001:db8:20:1::1ff' }, dns: ['10.20.0.1'], mtu: 1500 },
      status: { bridge_up: true, members: ['epair0a', 'epair1a', 'epair2a', 'epair4a'], allocated: 8, free: 38, orphans: [], conditions: [] },
    },
    {
      metadata: meta('backend'), spec: { bridge: 'jo1', private: true, ipv4: { subnet: '10.30.0.0/28', gateway: '10.30.0.1', range: '10.30.0.2-10.30.0.13' } },
      status: { bridge_up: true, members: ['epair3a'], allocated: 12, free: 0, orphans: ['epair9a', 'epair9b'], conditions: [] },
    },
    {
      metadata: meta('edge'), spec: { bridge: 'jo2', uplink: 'em1', ipv4: { subnet: '192.0.2.0/27', gateway: '192.0.2.1', range: '192.0.2.10-192.0.2.30' }, ipv6: { mode: 'proxy', subnet: '2001:db8:99::/64', gateway: 'fe80::1', range: '2001:db8:99::100-2001:db8:99::10f' } },
      status: { bridge_up: true, members: ['epair5a'], allocated: 4, free: 17, orphans: [], conditions: [] },
    },
    {
      metadata: meta('shop'), spec: { bridge: 'jo3', ipv4: { subnet: '10.64.0.0/24', gateway: '10.64.0.1', range: '10.64.0.10-10.64.0.250' } },
      status: { bridge_up: true, members: [], allocated: 8, free: 233, orphans: [], conditions: [] },
    },
    {
      metadata: meta('stg'), spec: { bridge: 'jo4', ipv4: { subnet: '10.60.0.0/28', gateway: '10.60.0.1', range: '10.60.0.2-10.60.0.13' } },
      status: { bridge_up: true, members: [], allocated: 1, free: 12, orphans: [], conditions: [] },
    },
  ]
}

export function leases(): Record<string, Lease[]> {
  return {
    lan: [{ address: '10.20.0.11', jail: 'web', network: 'lan' }, { address: '10.20.0.14', jail: 'cache', network: 'lan' }, { address: '10.20.0.17', jail: 'alpine-app', network: 'lan' }],
    backend: [{ address: '10.30.0.12', jail: 'db', network: 'backend' }],
    edge: [{ address: '192.0.2.10', jail: 'proxy', network: 'edge' }, { address: '2001:db8:99::100', jail: 'proxy', network: 'edge' }],
  }
}

export function policies(): FirewallPolicy[] {
  return [
    {
      metadata: meta('web-default'), spec: {
        default_in: 'deny', default_out: 'allow', rules: [
          { name: 'http', action: 'allow', direction: 'in', proto: 'tcp', ports: ['80', '443'] },
          { name: 'ssh from admins', action: 'allow', direction: 'in', proto: 'tcp', from: ['@admins'], ports: ['22'], log: true },
          { name: 'forward 8080', action: 'allow', direction: 'in', proto: 'tcp', ports: ['8080'], redirect: { host_port: 8080, jail_port: 80 } },
        ],
      },
    },
    { metadata: meta('db-internal'), spec: { default_in: 'deny', default_out: 'deny', rules: [{ name: 'postgres from web', action: 'allow', direction: 'in', proto: 'tcp', from: ['tag:web'], ports: ['5432'] }, { name: 'dns', action: 'allow', direction: 'out', proto: 'udp', to: ['10.30.0.1'], ports: ['53'] }] } },
    { metadata: meta('mail-edge'), spec: { default_in: 'deny', default_out: 'allow', rules: [{ name: 'smtp', action: 'allow', direction: 'in', proto: 'tcp', ports: ['25', '465', '587'] }], raw: 'block in quick from <spamhaus>' } },
  ]
}

export function addressSets(): AddressSet[] {
  return [
    { metadata: { ...meta('admins'), scope: 'fleet' }, spec: { addresses: ['198.51.100.7', '203.0.113.0/28'], includes: ['office'] } },
    { metadata: { ...meta('office'), scope: 'fleet' }, spec: { addresses: ['192.0.2.128/26', '2001:db8:aa::/48'] } },
  ]
}

export function targets(): BackupTarget[] {
  return [
    { metadata: meta('vault'), spec: { kind: 'node', address: 'vault.example.net:8443', fingerprint: 'SHA256:3b:9f:aa:01:c2:77:de:90:11:5e:42:ab:cd:ef:01' } },
    { metadata: meta('local-usb'), spec: { kind: 'directory', path: '/mnt/usb0/jailops' } },
  ]
}

export function backupPolicies(): BackupPolicy[] {
  return [
    { metadata: meta('nightly'), spec: { selector: { tier: 'frontend' }, targets: ['vault'], schedule: '15 2 * * *', retention: { last: 3, daily: 7, weekly: 4, monthly: 6 }, full_every: 14, verify_schedule: '0 5 * * 0', verify_mode: 'extract' } },
    { metadata: meta('db-hourly'), spec: { selector: { role: 'db' }, targets: ['vault', 'local-usb'], schedule: '0 * * * *', retention: { hourly: 24, daily: 14 }, include_state: true, verify_schedule: '0 6 * * 1', verify_mode: 'deep' } },
  ]
}

function bundleSpec(name: string): Jail['spec'] {
  const sp = structuredClone(jails().find((j) => j.metadata.name === name)!.spec)
  // The db jail came from a host that had a network this one lacks: the restore preflight reports it.
  if (name === 'db') sp.network = [{ network: 'dmz-old', ipv4: '10.99.0.5' }]
  return sp
}

export function backups(): Backup[] {
  const mk = (jailName: string, snap: string, age: number, bytes: number, newBytes: number, st: Partial<Backup['status']> = {}, full = false): Backup => ({
    metadata: { name: `${jailName}-${snap}`, scope: 'host', created_at: ago(age) },
    spec: {
      jail: jailName, jail_uuid: '0d3f-' + jailName, host_uuid: '6a1f0c52-2f0e-4f0b-8a5c-0123456789ab', policy: jailName === 'web' ? 'nightly' : 'db-hourly', target: 'vault',
      snapshot: snap, zfs_snapshot: `tank/jailops/jails/${jailName}@jo-${snap.slice(0, 8)}`, key_domain: 'kd-7f3a91', full,
      bundle: { spec: bundleSpec(jailName), image_generation: 'freebsd-15#g4', image_manifest: 'b3f0'.padEnd(40, 'a'), volumes: jailName === 'db' ? ['pgdata', 'wal'] : ['content'], secret_refs: jailName === 'db' ? ['db/replication-key'] : [] },
    },
    status: { bytes, new_bytes: newBytes, chunks: Math.round(newBytes / 1048576) + 3, files: 120000, verified: true, verified_at: ago(Math.max(60, age - 600)), ...st },
  })
  return [
    mk('web', '20261017021500-a91c03de', 86400 * 4, 9.2 * GiB, 9.2 * GiB, { protected: true }, true),
    mk('web', '20261018021500-b27d11f0', 86400 * 3, 9.3 * GiB, 410 * MiB),
    mk('web', '20261019021500-c4410aa2', 86400 * 2, 9.3 * GiB, 388 * MiB),
    mk('web', '20261020021500-d8e2c7b9', 86400, 9.4 * GiB, 402 * MiB, { protected: true }),
    mk('db', '20261021080000-11aa22bb', 3600 * 5, 61 * GiB, 61 * GiB, { protected: true }, true),
    mk('db', '20261021090000-22bb33cc', 3600 * 4, 61.5 * GiB, 2.2 * GiB),
    mk('db', '20261021110000-33cc44dd', 3600 * 2, 62 * GiB, 2.6 * GiB, { verified: false, failed: true }),
    mk('db', '20261021120000-44dd55ee', 3600, 62 * GiB, 1.9 * GiB, { verified: false, pending_deletion: new Date(now + 86400 * 1000 * 2).toISOString() }),
  ]
}

export function remotes(): Remote[] {
  return [
    { metadata: { ...meta('bsd02'), scope: 'fleet' }, spec: { address: 'bsd02.example.net:8443', fingerprint: 'SHA256:aa:bb', host_uuid: '77aa' }, status: { online: true, last_seen: ago(12), version: '0.1.0', extensions: knownExtensions.length } },
    { metadata: { ...meta('bsd03'), scope: 'fleet' }, spec: { address: 'bsd03.example.net:8443', fingerprint: 'SHA256:cc:dd' }, status: { online: false, last_seen: ago(3600 * 5), version: '0.0.9', extensions: 11 } },
    { metadata: { ...meta('vault'), scope: 'fleet' }, spec: { address: 'vault.example.net:8443', fingerprint: 'SHA256:3b:9f' }, status: { online: true, last_seen: ago(30), version: '0.1.0', extensions: knownExtensions.length } },
  ]
}

export function operations(): Operation[] {
  const o = (id: string, d: string, status: Operation['status'], age: number, extra: Partial<Operation> = {}): Operation => ({ id, description: d, status, resources: { jails: ['/api/v1/jails/web'] }, may_cancel: false, created_at: ago(age), updated_at: ago(age - 5), ...extra })
  return [
    o('op-1001', 'Snapshot web', 'success', 3600 * 9),
    o('op-1002', 'Upgrade db to freebsd-15#g4', 'failure', 3600 * 3, { resources: { jails: ['/api/v1/jails/db'] }, error: { code: 'busy', message: 'Replication is running on jail db.', hint: 'Wait for the send to finish or pause the backup policy db-hourly.' } }),
    o('op-1003', 'Build freebsd-15#g5', 'running', 140, { may_cancel: true, progress: 62, resources: { images: ['/api/v1/images/freebsd-15'] } }),
    o('op-1004', 'Start cache', 'success', 90, { resources: { jails: ['/api/v1/jails/cache'] } }),
  ]
}

export function audit(): AuditEntry[] {
  const a = (i: number, who: string, action: string, res: string, age: number, result = 'ok'): AuditEntry => ({ id: 'a' + i, timestamp: ago(age), requestor: who, action, resource: res, result, source_ip: who === 'ci' ? '198.51.100.20' : '203.0.113.5' })
  return [
    a(1, 'alice', 'jail.start', '/api/v1/jails/cache', 90), a(2, 'alice', 'login', 'session', 400), a(3, 'ci', 'jail.snapshot', '/api/v1/jails/web', 3600 * 9),
    a(4, 'bob', 'POST /api/v1/jails/{name}/upgrade', '/api/v1/jails/db', 3600 * 3, 'error'), a(5, 'alice', 'token.create', '/api/v1/tokens/ci', 86400 * 5), a(6, 'alice', 'firewall.update', '/api/v1/firewall/policies/web-default', 86400 * 6),
  ]
}

export function tokens(): Token[] {
  return [
    { metadata: meta('ci'), spec: { permission: 'operator', expires_at: new Date(now + 86400 * 1000 * 60).toISOString(), allow_from: ['198.51.100.0/24'] } },
    { metadata: meta('backup-node'), spec: { permission: 'backup-pull' } },
    { metadata: meta('old-laptop'), spec: { permission: 'admin', expires_at: ago(86400 * 4) } },
  ]
}
export function users(): User[] {
  return [
    { metadata: { ...meta('alice'), scope: 'fleet' }, spec: { permission: 'admin', totp: true } },
    { metadata: { ...meta('bob'), scope: 'fleet' }, spec: { permission: 'operator', totp: false } },
    { metadata: { ...meta('carol'), scope: 'fleet' }, spec: { permission: 'readonly', disabled: true } },
  ]
}
export function certificates(): Certificate[] {
  return [
    { metadata: { ...meta('hub-client'), scope: 'fleet' }, spec: { fingerprint: 'SHA256:5c:11:90:de:ad:be:ef:00:11:22:33:44', permission: 'admin', kind: 'host' } },
    { metadata: { ...meta('vault-pull'), scope: 'fleet' }, spec: { fingerprint: 'SHA256:3b:9f:aa:01:c2:77:de:90:11:5e:42:ab', permission: 'backup-pull', kind: 'backup' } },
  ]
}
