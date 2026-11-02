import { api } from '@/api/endpoints'
import { runOp } from '@jailops/shared/composables/ops'
import { s } from '@/i18n'

export type Lifecycle = 'start' | 'stop' | 'restart'
const label: Record<Lifecycle, string> = { start: s.jails.start, stop: s.jails.stop, restart: s.jails.restart }

export function lifecycle(name: string, action: Lifecycle): Promise<unknown> {
  return runOp(s.jails.actionTitle(label[action], name), () => api.jails.action(name, { action }))
}

/** Run an action on several jails; every jail gets its own toast, failures do not stop the rest. */
export async function lifecycleMany(names: string[], action: Lifecycle): Promise<{ ok: string[]; failed: string[] }> {
  const res = await Promise.allSettled(names.map((n) => lifecycle(n, action)))
  const ok: string[] = []
  const failed: string[] = []
  res.forEach((r, i) => (r.status === 'fulfilled' ? ok : failed).push(names[i]))
  return { ok, failed }
}

export function matches(j: { metadata: { name: string; labels?: Record<string, string> }; spec: { image: string }; status: { addresses?: string[] } }, q: string): boolean {
  const needle = q.trim().toLowerCase()
  if (!needle) return true
  const hay = [j.metadata.name, j.spec.image, ...(j.status.addresses ?? []), ...Object.entries(j.metadata.labels ?? {}).map(([k, v]) => `${k}=${v}`)]
  return hay.some((h) => h.toLowerCase().includes(needle))
}

/** The names of a jail's encrypted volumes (empty for a jail without). */
export function encryptedVolumes(j: { spec: { volumes?: Array<{ name: string; encrypted?: boolean }> } }): string[] {
  return (j.spec.volumes ?? []).filter((v) => v.encrypted).map((v) => v.name)
}

/** A jail whose encrypted volumes wait for their passphrase. */
export function isLocked(j: { status: { power: string; locked?: string[] } }): boolean {
  return j.status.power === 'locked' || (j.status.locked?.length ?? 0) > 0
}

/** Lock: stop the jail and unload the keys of its encrypted volumes. */
export function lockJail(name: string): Promise<unknown> {
  return runOp(s.keys.lockTitle(name), () => api.jails.lock(name))
}
