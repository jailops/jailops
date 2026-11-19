// Moving a jail to another image: a new release of the same OS. The server decides; these helpers only offer the
// images that can be a target and read what its dry run answers.
import type { Image, Jail, UpgradePreview } from '@/types'

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

/** The name of the image a jail's spec.image refers to (without the generation). */
export function imageOf(ref: string): string {
  return ref.split(/[@#]/)[0]
}

/**
 * The images a thin FreeBSD jail can move to: another image of the same OS and architecture, a release that is not older,
 * with a generation to move to. Thick jails and jails of another OS move nowhere.
 */
export function moveImages(images: Image[], jail: Pick<Jail, 'spec'>): Image[] {
  if (jail.spec.layout === 'thick' || jail.spec.os === 'linux') return []
  const cur = images.find((i) => i.metadata.name === imageOf(jail.spec.image))
  if (!cur) return []
  return images.filter((i) => i.metadata.name !== cur.metadata.name && i.spec.os === cur.spec.os && i.spec.arch === cur.spec.arch
    && !releaseOlder(i.spec.release, cur.spec.release) && (i.status.generations ?? []).length > 0)
}

/** The images every one of the jails can move to, for the bulk move. */
export function commonMoveImages(images: Image[], jails: Array<Pick<Jail, 'spec'>>): Image[] {
  if (!jails.length) return []
  const per = jails.map((j) => new Set(moveImages(images, j).map((i) => i.metadata.name)))
  return moveImages(images, jails[0]).filter((i) => per.every((s) => s.has(i.metadata.name)))
}

/** What the server answers to a dry-run upgrade: the generations, the releases, what is kept, the steps and the warnings. */
export function readPreview(raw: unknown, target: string): UpgradePreview {
  const r = (raw ?? {}) as Record<string, any>
  const u = (r.upgrade ?? {}) as Record<string, any>
  return {
    from_generation: String(r.from_generation ?? u.from ?? ''),
    to_generation: String(r.to_generation ?? u.to ?? target),
    steps: (r.steps ?? r.plan ?? []) as string[],
    etc_conflicts: r.etc_conflicts as string[] | undefined,
    warnings: r.warnings as string[] | undefined,
    from_release: u.from_release as string | undefined,
    to_release: u.to_release as string | undefined,
    image: u.image as string | undefined,
    snapshot: u.snapshot as string | undefined,
    kept: u.kept as string[] | undefined,
  }
}

/** "15.1-RELEASE to 15.2-RELEASE", or the release alone when it does not change; empty when the answer has none. */
export function releaseChange(p: Pick<UpgradePreview, 'from_release' | 'to_release'>): string {
  if (!p.from_release || !p.to_release) return ''
  return p.from_release === p.to_release ? p.to_release : `${p.from_release} to ${p.to_release}`
}
