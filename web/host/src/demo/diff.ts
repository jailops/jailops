// The demo's answer to GET /jails/NAME/diff: a few changes in the root and the persistent datasets of every jail, with a
// short default limit so the "Show all" button has something to show. Only loaded for `?demo`.
import type { DiffDataset, DiffEntry, Jail, JailDiff } from '@jailops/shared/api/types'

const e = (kind: string, path: string, to?: string): DiffEntry => (to ? { kind, path, to } : { kind, path })

function dataset(name: string, path: string, base: string, all: DiffEntry[], limit: number, note?: string): DiffDataset {
  const counts: Record<string, number> = {}
  for (const x of all) counts[x.kind] = (counts[x.kind] ?? 0) + 1
  const entries = all.slice(0, limit)
  return { name, path, base, note, entries, total: all.length, counts, truncated: entries.length < all.length || undefined }
}

export function demoDiff(jail: Jail, query: URLSearchParams): JailDiff {
  const limit = Number(query.get('limit')) || 5
  const name = jail.metadata.name
  const image = jail.status?.image_generation ?? 'freebsd-15-1#g4'
  const objects = Array.from({ length: 6 }, (_, i) => e('added', `/usr/local/src/${name}/obj/file${i + 1}.o`))
  const out: JailDiff = {
    jail: name,
    image,
    datasets: [
      dataset('root', '/', 'image', [e('modified', '/boot/loader.conf'), e('removed', '/etc/motd.template')], limit),
      dataset('etc', '/etc', 'baseline', [e('modified', '/etc/rc.conf'), e('added', '/etc/ssl/site.pem'), e('renamed', '/etc/old.conf', '/etc/site.conf')], limit),
      dataset('local', '/usr/local', 'baseline', [e('added', '/usr/local/etc/app.conf'), ...objects], limit),
      dataset('var-log', '/var/log', 'baseline', [], limit),
    ],
  }
  if (query.get('packages') === 'true') {
    out.packages = jail.status?.power === 'running'
      ? { packages: ['nginx-1.30.6,3', 'curl-8.11.1'] }
      : { packages: [], note: 'The jail is not running; start it to list its packages.' }
  }
  return out
}
