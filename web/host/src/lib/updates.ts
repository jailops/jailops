import type { UpdateReport } from '@jailops/shared/api/types'

/** What an apply will do, from the check report and the pipeline of design 8.4. The API has no dry run:
 *  `updates/check` is the read-only step, so the preview is built from its report. */
export function updatePlan(r: UpdateReport | undefined, t: { planSnapshot: string; planPackages: (n: number) => string; planOS: (from: string, to: string) => string; planHealth: string; planRollback: string }): string[] {
  const steps = [t.planSnapshot]
  if (r?.packages?.length) steps.push(t.planPackages(r.packages.length))
  if (r?.os) steps.push(t.planOS(r.os.from, r.os.to))
  steps.push(t.planHealth, t.planRollback)
  return steps
}
