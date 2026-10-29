// The catalog pages' pure parts: search and categories, the install form, the step list of an
// installation, the jails that came from the catalog and the links to their web ports.
import { AppCategoryValues } from '@jailops/shared/api/types'
import type { AppCategory, AppInstall, AppPort, AppResources, AppSummary, Jail, Project } from '@jailops/shared/api/types'

/** Labels the installation sets on the jail (docs/apps.md). */
export const LABEL_APP = 'jailops.app'
export const LABEL_VERSION = 'jailops.app.version'
export const LABEL_HASH = 'jailops.app.hash'
/** The snapshot an installation ends with: the point to roll back to. */
export const INSTALL_SNAPSHOT = 'app-installed'

export function matchesApp(a: AppSummary, q: string): boolean {
  const needle = q.trim().toLowerCase()
  if (!needle) return true
  return [a.name, a.title ?? '', a.summary ?? ''].some((h) => h.toLowerCase().includes(needle))
}

/** The apps that match the search and the category ('' is every category), valid ones first, then by title. */
export function searchApps(list: AppSummary[], q: string, category = ''): AppSummary[] {
  return list
    .filter((a) => matchesApp(a, q) && (!category || a.category === category))
    .sort((a, b) => Number(b.valid) - Number(a.valid) || (a.title || a.name).localeCompare(b.title || b.name))
}

/** Category chips: how many apps of each category match the search, in the catalog's category order, empty ones left out. */
export function categoryCounts(list: AppSummary[], q: string): Array<{ category: AppCategory; count: number }> {
  const hits = list.filter((a) => matchesApp(a, q))
  return AppCategoryValues.map((category) => ({ category, count: hits.filter((a) => a.category === category).length })).filter((c) => c.count > 0)
}

/** A stable monogram for an app without an icon: two letters and a hue derived from the name. */
export function monogram(name: string, title = ''): { text: string; hue: number } {
  const words = (title || name).split(/[\s-]+/).filter(Boolean)
  const text = (words.length > 1 ? words[0][0] + words[1][0] : (words[0] ?? '?').slice(0, 2)).toUpperCase()
  let h = 0
  for (const ch of name) h = (h * 31 + ch.charCodeAt(0)) >>> 0
  return { text, hue: h % 360 }
}

/** Plain text of a definition (description, notes) as paragraphs: blank lines separate them, single line breaks are wrapping. */
export function paragraphs(text: string | undefined): string[] {
  return (text ?? '').split(/\n\s*\n/).map((p) => p.replace(/\s*\n\s*/g, ' ').trim()).filter(Boolean)
}

/** A jail name as the server checks it on an installation. */
export const JAIL_NAME = /^[a-z][a-z0-9-]{0,62}$/
const SIZE = /^\d+(?:\.\d+)?[KMGT]?$/i

export interface InstallForm { jail: string; network: string; storage: string; memory: string; disk: string; cpu: string }
export type InstallIssue = 'nameInvalid' | 'nameTaken' | 'needNetwork' | 'needStorage' | 'sizeInvalid' | 'cpuInvalid'

/** Field -> problem. Empty when the form may be sent. */
export function validateInstall(f: InstallForm, ctx: { networks: string[]; storage: string[]; taken: string[] }): Partial<Record<keyof InstallForm, InstallIssue>> {
  const out: Partial<Record<keyof InstallForm, InstallIssue>> = {}
  if (!JAIL_NAME.test(f.jail)) out.jail = 'nameInvalid'
  else if (ctx.taken.includes(f.jail)) out.jail = 'nameTaken'
  if (ctx.networks.length && !ctx.networks.includes(f.network)) out.network = 'needNetwork'
  if (ctx.storage.length && !ctx.storage.includes(f.storage)) out.storage = 'needStorage'
  if (f.memory && !SIZE.test(f.memory.trim())) out.memory = 'sizeInvalid'
  if (f.disk && !SIZE.test(f.disk.trim())) out.disk = 'sizeInvalid'
  if (f.cpu && !(Number(f.cpu) > 0)) out.cpu = 'cpuInvalid'
  return out
}

/** The form prefilled for an app: its name as the jail, the only grant of each kind, the resource hints. */
export function initialForm(app: string, hints: AppResources | undefined, networks: string[], storage: string[]): InstallForm {
  return {
    jail: app, network: networks.length === 1 ? networks[0] : '', storage: storage.length === 1 ? storage[0] : '',
    memory: hints?.memory ?? '', disk: hints?.disk ?? '', cpu: hints?.cpu ?? '',
  }
}

/** The request body. Limits are sent only when they differ from the hints, which the server applies anyway. */
export function installBody(f: InstallForm, hints: AppResources | undefined, dryRun = false): AppInstall {
  const b: AppInstall = { jail: f.jail }
  if (f.network) b.network = f.network
  if (f.storage) b.storage = f.storage
  const changed = (f.memory || '') !== (hints?.memory ?? '') || (f.disk || '') !== (hints?.disk ?? '') || (f.cpu || '') !== (hints?.cpu ?? '')
  if (changed) {
    b.limits = {}
    if (f.memory) b.limits.memory = f.memory.trim()
    if (f.disk) b.limits.disk = f.disk.trim()
    if (f.cpu) b.limits.cpu = Number(f.cpu)
  }
  if (dryRun) b.dry_run = true
  return b
}

/** The networks and storage granted to a project. A storage grant of "*" is every storage of the host. */
export function grants(p: Project | undefined, allStorage: string[]): { networks: string[]; storage: string[] } {
  const networks = (p?.spec.networks ?? []).map((g) => g.network)
  const sto = (p?.spec.storage ?? []).map((g) => g.storage)
  return { networks, storage: sto.includes('*') ? allStorage : sto }
}

/** One step of the plan as the server describes it: "03 install-packages-ports: pkg -r ...". */
export interface Step { n: number; name: string; text: string }
export function parseStep(line: string): Step {
  const m = /^(\d+)\s+([^:\s]+):\s?(.*)$/.exec(line)
  return m ? { n: Number(m[1]), name: m[2], text: m[3] } : { n: 0, name: '', text: line }
}

/**
 * Steps finished, from the operation's progress: the server sets it to (steps done) * 100 / (steps) after every
 * step, capped at 99 until the operation ends, so the count is recovered exactly for plans under 100 steps.
 */
export function stepsDone(progress: number | undefined, total: number, finished = false): number {
  if (finished) return total
  if (!total || !progress) return 0
  return Math.min(total, Math.ceil((progress * total) / 100 - 1e-9))
}

export type StepState = 'done' | 'running' | 'pending' | 'failed'
/** The state of step i. A failure names its step (detail.step); without it the step after the last finished one failed. */
export function stepState(i: number, steps: Step[], done: number, status: 'running' | 'success' | 'failure', failedStep = ''): StepState {
  if (status === 'success') return 'done'
  if (status === 'failure') {
    const at = failedStep ? steps.findIndex((s) => s.name === failedStep) : done
    const f = at < 0 ? done : at
    return i < f ? 'done' : i === f ? 'failed' : 'pending'
  }
  return i < done ? 'done' : i === done ? 'running' : 'pending'
}

/** The volume name the installation gives a data path: /var/db/postgres becomes var-db-postgres. */
export function volumeName(path: string): string {
  return path.split('/').filter(Boolean).join('-')
}

/** The jails of a list that came from the catalog, optionally of one app, by name. */
export function installedJails(jails: Jail[], app = ''): Jail[] {
  return jails
    .filter((j) => {
      const v = j.metadata.labels?.[LABEL_APP]
      return !!v && (!app || v === app)
    })
    .sort((a, b) => a.metadata.name.localeCompare(b.metadata.name))
}

/** Ports that serve a web page. The definition has no marker for it, so the port's name decides: http or https over tcp. */
export function webPorts(ports: AppPort[] | undefined): AppPort[] {
  return (ports ?? []).filter((p) => p.proto === 'tcp' && (p.name === 'http' || p.name === 'https'))
}

/** "Open" links of a running jail: its first address with each exposed web port. */
export function openLinks(jail: Jail, ports: AppPort[] | undefined): Array<{ port: AppPort; url: string }> {
  const first = jail.status.power === 'running' ? (jail.status.addresses ?? [])[0] : undefined
  if (!first) return []
  const ip = first.split('/')[0]
  const host = ip.includes(':') ? `[${ip}]` : ip
  return webPorts(ports)
    .filter((p) => p.expose)
    .map((p) => {
      const tls = p.name === 'https'
      const dflt = tls ? 443 : 80
      return { port: p, url: `${tls ? 'https' : 'http'}://${host}${p.port === dflt ? '' : `:${p.port}`}/` }
    })
}

export type InstallBlock = '' | 'noExtension' | 'noProject' | 'role' | 'scope' | 'noNetwork' | 'noStorage'

/**
 * Why the caller cannot install, or '' when the console should offer it. The server decides: an unknown role
 * (a token without a session) may try, and the grants are checked only when the project is known.
 */
export function installBlock(o: { ext: boolean; needProject: boolean; project: string; role: string; global: boolean; scopes: string[]; known: boolean; networks: string[]; storage: string[] }): InstallBlock {
  if (!o.ext) return 'noExtension'
  if (o.needProject && !o.project) return 'noProject'
  if (o.role && o.role !== 'operator' && o.role !== 'admin') return 'role'
  if (o.role && !o.global && o.project && !o.scopes.includes(o.project)) return 'scope'
  if (o.known && !o.networks.length) return 'noNetwork'
  if (o.known && !o.storage.length) return 'noStorage'
  return ''
}
