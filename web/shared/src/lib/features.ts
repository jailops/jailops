// Capability negotiation: a UI feature is shown only when the server lists its extension.

export type Feature =
  | 'jails' | 'snapshots' | 'rollback_scopes' | 'images' | 'generations' | 'image_aliases' | 'networks' | 'firewall'
  | 'backups' | 'restore' | 'audit' | 'events' | 'operations' | 'resources' | 'doctor'
  | 'projects' | 'project_quotas' | 'storage' | 'trust_tokens' | 'sessions' | 'fleet_projects' | 'exec' | 'profiles' | 'config_keys' | 'devices'

const need: Record<Feature, string> = {
  jails: 'jails',
  snapshots: 'jail_snapshots',
  rollback_scopes: 'jail_rollback_scopes',
  images: 'images_pkgbase',
  generations: 'image_generations',
  image_aliases: 'image_aliases',
  networks: 'networks_vnet',
  firewall: 'firewall_anchors',
  backups: 'backup_policies',
  restore: 'restore_preflight',
  audit: 'audit_log',
  events: 'events',
  operations: 'operations',
  resources: 'resources',
  doctor: 'doctor',
  projects: 'projects',
  project_quotas: 'project_quotas',
  storage: 'storage',
  trust_tokens: 'trust_tokens',
  sessions: 'sessions',
  fleet_projects: 'fleet_projects',
  exec: 'exec',
  profiles: 'profiles',
  config_keys: 'config_keys',
  devices: 'devices',
}

export function has(extensions: readonly string[] | undefined, f: Feature): boolean {
  return !!extensions && extensions.includes(need[f])
}
