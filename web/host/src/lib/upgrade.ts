// The in-place upgrade of a jail: the server decides; these helpers read what its dry run answers.
import type { UpgradePreview } from '@/types'

/** "15.1-RELEASE" as [15, 1]; null for a release that is not MAJOR.MINOR (the server decides then). */
export function releaseKey(r: string): [number, number] | null {
  const m = /^(\d+)\.(\d+)(?:-|$)/.exec(r)
  return m ? [Number(m[1]), Number(m[2])] : null
}

/** Whether release a is older than release b; releases that do not read as MAJOR.MINOR are never older. */
export function releaseOlder(a: string, b: string): boolean {
  const x = releaseKey(a)
  const y = releaseKey(b)
  return !!x && !!y && (x[0] < y[0] || (x[0] === y[0] && x[1] < y[1]))
}

const list = (v: unknown): string[] | undefined => (Array.isArray(v) && v.length ? v.map(String) : undefined)

/** What the server answers to a dry-run upgrade: the plan, the releases, the package changes, the checks and the warnings. */
export function readPreview(raw: unknown): UpgradePreview {
  const r = (raw ?? {}) as Record<string, any>
  const u = (r.upgrade ?? {}) as Record<string, any>
  return {
    nothing: !!u.nothing_to_upgrade,
    installed: u.installed as string | undefined,
    from_release: u.from_release as string | undefined,
    to_release: u.to_release as string | undefined,
    snapshot: u.snapshot as string | undefined,
    steps: list(r.plan) ?? [],
    base_changes: list(u.base_changes),
    ports_packages: list(u.ports_packages),
    checks: list(u.checks),
    failed_checks: list(u.failed_checks),
    health_check: u.health_check as string | undefined,
    restart_pending: list(u.restart_pending),
    warnings: list(r.warnings),
  }
}

/** "15.1-RELEASE to 15.2-RELEASE", or the release alone when it does not change; empty when the answer has none. */
export function releaseChange(p: Pick<UpgradePreview, 'from_release' | 'to_release'>): string {
  if (!p.from_release || !p.to_release) return ''
  return p.from_release === p.to_release ? p.to_release : `${p.from_release} to ${p.to_release}`
}
