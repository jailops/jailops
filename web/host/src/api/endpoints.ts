// Typed resource calls of the host console. Paths follow sdk/PROTOCOL.md. All network access
// goes through the shared client.
import { getClient } from '@jailops/shared/api/client'
import { core } from '@jailops/shared/api/core'
import type {
  Jail, JailAction, JailRollback, JailUpgrade, JailSnapshot, Image, ImageBuild, ImageUpgrade, Network, Lease,
  FirewallPolicy, AddressSet, BackupTarget, BackupPolicy, Backup, RestoreRequest, RestorePreflight,
  Certificate, User, UserCreate, UserCreated, PasswordSet, Remote, Operation, AuditEntry, MutationResult, PoolUsage, DoctorReport, Token,
  Profile, JailExpanded, ConfigChange, UpdateReport, UpdateRecord, UpdateCheckRequest, UpdateApplyRequest, ProvisionRequest, ProvisionResult,
  BackupRun, BackupRunRequest, BackupVerifyRequest, BackupKeyStatus, BackupKeyCreate, BackupKeyCreated, BackupKeyEscrow, ExecRequest,
  Project, ProjectAggregate, Storage, TrustTokenRequest, TrustToken, Session, Identity,
  AppSummary, AppDetail, AppInstall, AppInstallResult, JailLogFile, JailLogRead,
  HostIdentity, HostIdentityApply, HostChange, HostSnapshot, HostRestore,
  WireGuard, WireGuardJoin, WireGuardKeys, WireGuardPeerAdd, WireGuardPeerCreated,
  AppAccess, AppAllow, AppAllowResult, AppDeny, AppDenyResult,
} from '@jailops/shared/api/types'
import type { SnapshotBody, ExecSessionInfo, ConfigResult } from '../types'
import { logQuery, type LogQuery } from '@/lib/logs'

const e = encodeURIComponent
const c = () => getClient()

export const api = {
  ...core,
  pool: () => c().get<PoolUsage>('/resources'),
  doctor: () => c().get<DoctorReport>('/doctor'),
  tokens: {
    list: () => c().list<Token>('/tokens'),
    create: (t: Pick<Token, 'metadata' | 'spec'>) => c().mutate<Token>('POST', '/tokens', t),
    remove: (n: string) => c().mutate('DELETE', `/tokens/${e(n)}`),
  },
  jails: {
    list: () => c().list<Jail>('/jails'),
    get: (n: string) => c().get<Jail>(`/jails/${e(n)}`),
    create: (j: Pick<Jail, 'metadata' | 'spec'>) => c().mutate('POST', '/jails', j),
    action: (n: string, a: JailAction) => c().mutate('PUT', `/jails/${e(n)}/state`, a),
    snapshots: (n: string) => c().list<JailSnapshot>(`/jails/${e(n)}/snapshots`),
    snapshot: (n: string, b: SnapshotBody) => c().mutate('POST', `/jails/${e(n)}/snapshots`, b),
    rollback: (n: string, r: JailRollback) => c().mutate('POST', `/jails/${e(n)}/rollback`, r),
    upgrade: (n: string, r: JailUpgrade) => c().mutate('POST', `/jails/${e(n)}/upgrade`, r),
  },
  apps: {
    /** Extension apps: the catalog is readable by any identity, it belongs to no project. */
    list: () => c().list<AppSummary>('/apps'),
    get: (n: string) => c().get<AppDetail>(`/apps/${e(n)}`),
    /** Extension app_install: async, operator in the project. The project travels in the query like every project request. */
    install: (n: string, b: AppInstall, project: string) => c().mutate<AppInstallResult>('POST', `/apps/${e(n)}/install${project ? `?project=${e(project)}` : ''}`, b),
  },
  host: {
    /** Extension host_identity: the values read from the host and the management state (readable when not managed). */
    identity: () => c().get<HostIdentity>('/host/identity'),
    /** Global admin. With dry_run the reply is the diff and the actions, and nothing changes. */
    apply: (b: HostIdentityApply) => c().mutate<HostChange>('PUT', '/host/identity', b),
    snapshots: () => c().list<HostSnapshot>('/host/snapshots'),
    restore: (name: string, b: HostRestore = {}) => c().mutate<HostChange>('POST', `/host/snapshots/${e(name)}/restore`, b),
  },
  appAccess: {
    /** Extension app_access: who may connect to an installed app (readonly in the project). */
    get: (n: string) => c().get<AppAccess>(`/jails/${e(n)}/access`),
    /** Operator in the project; answered at once, not as an operation: a connection string is in this answer only. */
    allow: (n: string, b: AppAllow) => c().mutate<AppAllowResult>('POST', `/jails/${e(n)}/access`, b),
    deny: (n: string, b: AppDeny) => c().mutate<AppDenyResult>('POST', `/jails/${e(n)}/access/deny`, b),
  },
  logs: {
    /** Extension jail_logs: the files below /var/log of the jail (operator in the project). */
    list: (n: string) => c().list<JailLogFile>(`/jails/${e(n)}/logs`),
    /** A file name may hold one directory ("nginx/access.log"): each segment is encoded on its own. */
    read: (n: string, file: string, q: LogQuery = {}) => c().get<JailLogRead>(`/jails/${e(n)}/logs/${file.split('/').map(e).join('/')}${logQuery(q)}`),
  },
  images: {
    list: () => c().list<Image>('/images'),
    build: (n: string, b: ImageBuild) => c().mutate('POST', `/images/${e(n)}/generations`, b),
    upgrade: (n: string, u: ImageUpgrade) => c().mutate('POST', `/images/${e(n)}/upgrade`, u),
    setAlias: (n: string, alias: string, generation: number) => c().mutate<Image>('PUT', `/images/${e(n)}/aliases/${e(alias)}`, { generation }),
    deleteAlias: (n: string, alias: string) => c().mutate<Image>('DELETE', `/images/${e(n)}/aliases/${e(alias)}`),
  },
  networks: {
    list: () => c().list<Network>('/networks'),
    leases: (n: string) => c().list<Lease>(`/networks/${e(n)}/leases`),
    create: (n: Pick<Network, 'metadata' | 'spec'>) => c().mutate('POST', '/networks', n),
  },
  wireguard: {
    /** Extension wireguard: an operator reads, an admin changes. Keys never come back except in a peer's token, once. */
    list: () => c().list<WireGuard>('/wireguard'),
    create: (w: Pick<WireGuard, 'metadata' | 'spec'>) => c().mutate<WireGuard>('POST', '/wireguard', w),
    remove: (n: string) => c().mutate('DELETE', `/wireguard/${e(n)}`),
    join: (j: WireGuardJoin) => c().mutate<WireGuard>('POST', '/wireguard/join', j),
    addPeer: (n: string, p: WireGuardPeerAdd) => c().mutate<WireGuardPeerCreated>('POST', `/wireguard/${e(n)}/peers`, p),
    removePeer: (n: string, peer: string) => c().mutate<WireGuard>('DELETE', `/wireguard/${e(n)}/peers/${e(peer)}`),
    up: (n: string) => c().mutate<WireGuard>('POST', `/wireguard/${e(n)}/up`),
    down: (n: string) => c().mutate<WireGuard>('POST', `/wireguard/${e(n)}/down`),
    rotateKeys: (n: string) => c().mutate<WireGuardKeys>('POST', `/wireguard/${e(n)}/rotate-keys`),
  },
  firewall: {
    policies: () => c().list<FirewallPolicy>('/firewall/policies'),
    policy: (n: string) => c().getWithEtag<FirewallPolicy>(`/firewall/policies/${e(n)}`),
    addressSets: () => c().list<AddressSet>('/firewall/address-sets'),
    addressSet: (n: string) => c().getWithEtag<AddressSet>(`/firewall/address-sets/${e(n)}`),
    /** PUT replaces the object; If-Match carries the generation so a concurrent edit is a 412. */
    putPolicy: (p: FirewallPolicy, etag?: string) => c().mutate('PUT', `/firewall/policies/${e(p.metadata.name)}`, p, { etag }),
    putAddressSet: (a: AddressSet, etag?: string) => c().mutate('PUT', `/firewall/address-sets/${e(a.metadata.name)}`, a, { etag }),
  },
  backups: {
    targets: () => c().list<BackupTarget>('/backup-targets'),
    policies: () => c().list<BackupPolicy>('/backup-policies'),
    list: () => c().list<Backup>('/backups'),
    /** Extension backup_run (global admin). */
    run: (b: BackupRunRequest) => c().mutate<BackupRun[]>('POST', '/backups/run', b),
    /** Extension backup_verify: depth is deep, extract or full. */
    verify: (id: string, b: BackupVerifyRequest) => c().mutate<BackupRun>('POST', `/backups/${e(id)}/verify`, b),
    /** Extension backup_key (global admin). The recovery key is in the create reply once. */
    keyStatus: () => c().get<BackupKeyStatus>('/backup/key'),
    createKey: (b: BackupKeyCreate) => c().mutate<BackupKeyCreated>('POST', '/backup/key', b),
    escrowKey: (b: BackupKeyEscrow) => c().mutate<BackupKeyStatus>('POST', '/backup/key/escrow', b),
    /** The node applies its delay before anything is deleted. */
    requestDeletion: (id: string) => c().mutate('DELETE', `/backups/${e(id)}`),
    restore: (r: RestoreRequest): Promise<MutationResult<RestorePreflight>> => c().mutate('POST', '/restores', r),
  },
  projects: {
    list: () => c().list<Project>('/projects'),
    get: (n: string) => c().getWithEtag<Project>(`/projects/${e(n)}`),
    /** Hub only: the definition and the state on every host the project lists. */
    aggregate: (n: string) => c().get<ProjectAggregate>(`/projects/${e(n)}?host=all`),
    create: (p: Pick<Project, 'metadata' | 'spec'>) => c().mutate<Project>('POST', '/projects', p),
    put: (p: Project, etag?: string) => c().mutate<Project>('PUT', `/projects/${e(p.metadata.name)}`, p, { etag }),
    remove: (n: string) => c().mutate('DELETE', `/projects/${e(n)}`),
  },
  storage: {
    list: () => c().list<Storage>('/storage'),
    create: (s: Pick<Storage, 'metadata' | 'spec'>) => c().mutate<Storage>('POST', '/storage', s),
    remove: (n: string) => c().mutate('DELETE', `/storage/${e(n)}`),
  },
  trust: {
    createToken: (r: TrustTokenRequest) => c().mutate<TrustToken>('POST', '/trust/tokens', r),
    whoami: () => c().get<Identity>('/auth/session'),
    sessions: () => c().list<Session>('/sessions'),
    revokeSession: (id: string) => c().mutate('DELETE', `/sessions/${e(id)}`),
    putCertificate: (cert: Certificate, etag?: string) => c().mutate<Certificate>('PUT', `/certificates/${e(cert.metadata.name)}`, cert, { etag }),
    getCertificate: (n: string) => c().getWithEtag<Certificate>(`/certificates/${e(n)}`),
  },
  profiles: {
    /** `project=host` selects the host-level profiles every project can use. */
    list: (project?: string) => c().list<Profile>(`/profiles${project ? `?project=${e(project)}` : ''}`),
    put: (p: Profile, etag?: string) => c().mutate<Profile>('PUT', `/profiles/${e(p.metadata.name)}`, p, { etag }),
    create: (p: Profile) => c().mutate<Profile>('POST', '/profiles', p),
    remove: (n: string) => c().mutate('DELETE', `/profiles/${e(n)}`),
  },
  jailConfig: {
    get: (n: string) => c().getWithEtag<Jail>(`/jails/${e(n)}`),
    /** The jail with its profiles merged in and the origin of every key. */
    expanded: (n: string) => c().get<JailExpanded>(`/jails/${e(n)}?expanded=true`),
    /** Never restarts the jail: the result lists the keys that need a restart. `restart` is an explicit request. */
    post: (n: string, change: ConfigChange, restart = false) => c().mutate<ConfigResult>('POST', `/jails/${e(n)}/config${restart ? '?restart=true' : ''}`, change),
  },
  updates: {
    /** The latest check of every visible jail of the project. */
    list: () => c().list<UpdateReport>('/updates'),
    check: (b: UpdateCheckRequest) => c().mutate<UpdateReport[]>('POST', '/updates/check', b),
    apply: (b: UpdateApplyRequest) => c().mutate<UpdateRecord[]>('POST', '/updates/apply', b),
    history: (n: string) => c().list<UpdateRecord>(`/jails/${e(n)}/updates`),
  },
  provision: {
    run: (n: string, b: ProvisionRequest) => c().mutate<ProvisionResult>('POST', `/jails/${e(n)}/provision`, b),
  },
  /** The project filter lives with the client: composables use this and never touch the network. */
  setProjectFilter: (fn: ((path: string) => string) | null) => c().setRequestFilter(fn),
  exec: {
    /** The daemon's exec protocol is an HTTP upgrade, which a browser cannot open: the console is a session (extension jail_console). */
    open: (n: string, o: ExecRequest) => c().mutate<ExecSessionInfo>('POST', `/jails/${e(n)}/console`, o),
    input: (n: string, id: string, data: string) => c().mutate('POST', `/jails/${e(n)}/console/${e(id)}/input`, { data }),
    resize: (n: string, id: string, cols: number, rows: number) => c().mutate('POST', `/jails/${e(n)}/console/${e(id)}/resize`, { cols, rows }),
    close: (n: string, id: string) => c().mutate('DELETE', `/jails/${e(n)}/console/${e(id)}`),
    output: (n: string, id: string, h: import('@jailops/shared/api/client').StreamHandlers) => c().transport.stream(`/jails/${e(n)}/console/${e(id)}/output`, h),
  },
  remotes: { list: () => c().list<Remote>('/remotes') },
  operations: {
    list: () => c().list<Operation>('/operations'),
    cancel: (id: string) => c().cancelOperation(id),
    /** One long poll of an operation, for dialogs that follow its progress themselves. */
    wait: (id: string) => c().getOperation(id),
  },
  audit: { list: (limit = 200) => c().list<AuditEntry>(`/audit?limit=${limit}`) },
  access: {
    users: () => c().list<User>('/users'),
    createUser: (u: UserCreate) => c().mutate<UserCreated>('POST', '/users', u),
    patchUser: (n: string, spec: Partial<User['spec']>) => c().mutate<User>('PATCH', `/users/${e(n)}`, { spec }),
    setPassword: (n: string, password: string) => c().mutate<PasswordSet>('POST', `/users/${e(n)}/password`, password ? { password } : {}),
    deleteUser: (n: string) => c().mutate('DELETE', `/users/${e(n)}`),
    certificates: () => c().list<Certificate>('/certificates'),
    deleteCertificate: (n: string) => c().mutate('DELETE', `/certificates/${e(n)}`),
  },
}
