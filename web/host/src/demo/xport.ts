// The demo's jail export and import: the export file is a real one of the format (the manifest, no dataset streams), so the
// console's own reading and checking of files runs for real; the import makes the jail of the manifest. Only loaded for `?demo`.
import type { ExportManifest, Image, Jail, JailImportRequest, Network, RestorePreflight } from '@jailops/shared/api/types'
import { MAGIC } from '@/lib/exportfile'

export interface XportDb {
  jails: Jail[]
  images: Image[]
  networks: Network[]
}

function record(type: string, payload: string): Uint8Array<ArrayBuffer> {
  const p = new TextEncoder().encode(payload)
  const out = new Uint8Array(9 + p.length)
  out[0] = type.charCodeAt(0)
  new DataView(out.buffer).setBigUint64(1, BigInt(p.length))
  out.set(p, 9)
  return out
}

/** The export file of a jail: the magic line, the manifest, an empty footer. */
export function exportFile(j: Jail, q: URLSearchParams): Uint8Array<ArrayBuffer> {
  const thick = j.spec.layout === 'thick'
  const m: ExportManifest = {
    format: 1, created: new Date().toISOString(), name: j.metadata.name, project: j.metadata.project ?? '', uuid: j.metadata.uuid ?? '',
    layout: j.spec.layout ?? 'thin', image: { name: String(j.spec.image ?? '').split('@')[0] },
    bundle: { spec: j.spec, image_generation: '', image_manifest: '', network_names: [...new Set((j.spec.network ?? []).map((n) => n.network))] },
    parts: [],
    warnings: [
      ...(thick ? [] : ['The jail is thin: the host that imports it needs the image ' + String(j.spec.image ?? '').split('@')[0] + '.']),
      ...(q.get('with_volumes') === 'true' ? ['The demo carries no volumes.'] : []),
    ],
  } as ExportManifest
  const parts = [new TextEncoder().encode(MAGIC), record('M', JSON.stringify(m)), record('F', JSON.stringify({ parts: 0, sha256: '0'.repeat(64) }))]
  const out = new Uint8Array(parts.reduce((n, x) => n + x.length, 0))
  let o = 0
  for (const x of parts) { out.set(x, o); o += x.length }
  return out
}

/** What an import would do here, in the words of the server's preflight. */
export function importPreflight(db: XportDb, project: string, m: ExportManifest, req: JailImportRequest): RestorePreflight {
  const name = req.as || m.name
  const errors: string[] = []
  const missing: string[] = []
  if (db.jails.some((x) => x.metadata.name === name && (x.metadata.project ?? '') === project)) errors.push(`a jail named ${name} exists in project ${project}: choose another name`)
  if (m.layout !== 'thick' && !db.images.some((i) => i.metadata.name === m.image.name)) {
    missing.push(`image ${m.image.name}`)
    errors.push(`the jail is thin and its image ${m.image.name} is not on this host: build the image first`)
  }
  for (const n of m.bundle.network_names ?? []) {
    const to = req.network_map?.[n] ?? n
    if (!db.networks.some((x) => x.metadata.name === to)) {
      missing.push(`network ${to}`)
      errors.push(`network ${to} is not on this host: map ${n} to a network that is (a network is never made by an import)`)
    }
  }
  const changes = ['the new jail gets a new uuid and does not start at boot', req.keep_ip ? 'the addresses of the file are kept when they are free' : 'the jail gets new addresses']
  if (req.start) changes.push('the jail is started when it is made')
  return { ok: !errors.length, missing, changes, errors }
}

/** The jail an import makes (the caller stores it). */
export function importedJail(m: ExportManifest, project: string, req: JailImportRequest): Jail {
  const spec = JSON.parse(JSON.stringify(m.bundle.spec)) as Jail['spec']
  spec.network = (spec.network ?? []).map((n) => ({ ...n, network: req.network_map?.[n.network] ?? n.network, ...(req.keep_ip ? {} : { ipv4: 'auto' }) }))
  return {
    metadata: { name: req.as || m.name, scope: 'host', generation: 1, created_at: new Date().toISOString(), uuid: crypto.randomUUID(), ...(project ? { project } : {}), ...(m.labels ? { labels: m.labels } : {}) },
    spec,
    status: { power: req.start ? 'running' : 'stopped', addresses: [], conditions: [{ type: 'Reconciled', status: 'True' }] },
  } as Jail
}
