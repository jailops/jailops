import type { RestoreRequest } from '@jailops/shared/api/types'

export interface RestoreForm {
  backup: string
  as: string
  host: string
  replace: boolean
  keepIp: boolean
  networkMap: Record<string, string>
}

/** Build the request body; empty fields are left out so the server applies its defaults. */
export function buildRequest(f: RestoreForm, dryRun: boolean): RestoreRequest {
  const map: Record<string, string> = {}
  for (const [k, v] of Object.entries(f.networkMap)) if (v.trim() && v.trim() !== k) map[k] = v.trim()
  return {
    backup: f.backup,
    ...(f.host.trim() ? { host: f.host.trim() } : {}),
    ...(f.as.trim() ? { as: f.as.trim() } : {}),
    ...(f.replace ? { replace: true } : {}),
    ...(Object.keys(map).length ? { network_map: map } : {}),
    ...(f.keepIp ? { keep_ip: true } : {}),
    ...(dryRun ? { dry_run: true } : {}),
  }
}

/** Two requests are the same decision when they differ only in dry_run. */
export function requestKey(f: RestoreForm): string {
  const r = buildRequest(f, false)
  return JSON.stringify(r, Object.keys(r).sort())
}
