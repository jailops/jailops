import type { Repository, Group, Host } from '@/types'
import { s } from '@/i18n'

export interface Attention { key: string; tone: 'danger' | 'warn'; text: string; to?: string }

const DAY = 86400_000

export interface Input { repo?: Repository; hosts?: Host[]; groups?: Group[] }

/** Danger first: the operator scans the list top-down. */
export function attention(i: Input, now = Date.now()): Attention[] {
  const out: Attention[] = []
  const r = i.repo
  if (r && r.mode !== 'online') out.push({ key: 'mode', tone: 'warn', text: s.overview.modeAlert(r.mode), to: '/maintenance' })
  if (r && r.quarantined > 0) out.push({ key: 'quar', tone: 'danger', text: s.overview.quarantineAlert(r.quarantined), to: '/maintenance' })
  if (r && r.failed_snapshots > 0) out.push({ key: 'failed', tone: 'danger', text: s.overview.failedAlert(r.failed_snapshots), to: '/snapshots' })
  if (r && r.pending_deletions > 0) out.push({ key: 'pend', tone: 'warn', text: s.overview.pendingAlert(r.pending_deletions), to: '/deletions' })
  if (r?.last_verify && !r.last_verify.startsWith('0001')) {
    const d = Math.floor((now - Date.parse(r.last_verify)) / DAY)
    if (d >= 14) out.push({ key: 'verify', tone: 'warn', text: s.overview.verifyOld(d), to: '/maintenance' })
  } else if (r) {
    out.push({ key: 'verify-never', tone: 'warn', text: s.overview.verifyNever, to: '/maintenance' })
  }
  for (const h of i.hosts ?? []) {
    if (h.revoked_at) continue
    const t = h.last_seen_at ? Date.parse(h.last_seen_at) : 0
    if (t && now - t > 2 * DAY) out.push({ key: 'stale:' + h.name, tone: 'warn', text: s.overview.stale(h.name), to: '/hosts' })
  }
  return out.sort((a, b) => (a.tone === b.tone ? 0 : a.tone === 'danger' ? -1 : 1))
}
