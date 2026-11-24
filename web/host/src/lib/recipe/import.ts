// Reading what the person chose into a recipe: one YAML file, several files, or a directory (webkitdirectory). Side
// files are read here, in the browser (the daemon reads no path of the host); the directory that was chosen is the
// directory of the recipe, so `source: files/app.conf` finds files/app.conf below it, as `jailops jail apply -f DIR`.
import { FILE_MAX, type SideFiles } from './render'

export interface Picked {
  /** The path below the chosen directory (or the name of a single file). */
  path: string
  file: File
}

export interface Imported {
  /** The recipe text, '' when the choice holds none. */
  text: string
  /** The path of the recipe file inside the choice. */
  recipe: string
  /** The side files by path below the directory of the recipe; null is a file that is not text. */
  files: Record<string, string | null>
  /** A vars file (NAME=VALUE lines) that came with the recipe, if any. */
  vars: string
  problem: string
}

const MAX_FILES = 1000
const isYaml = (p: string) => /\.ya?ml$/i.test(p)
const isVars = (p: string) => /(^|\/)[^/]*\.(vars|env)$/i.test(p) || /(^|\/)vars$/.test(p)

/** What the browser reports for a chosen item: webkitRelativePath for a directory, the name otherwise. */
export function pick(files: FileList | File[]): Picked[] {
  return [...files].map((f) => ({ path: (f as File & { webkitRelativePath?: string }).webkitRelativePath || f.name, file: f }))
}

async function text(f: File): Promise<string | null> {
  try {
    return new TextDecoder('utf-8', { fatal: true }).decode(await f.arrayBuffer())
  } catch {
    return null
  }
}

/** Find the recipe among the chosen files and read the rest beside it. */
export async function readImport(picked: Picked[]): Promise<Imported> {
  const none = (problem: string): Imported => ({ text: '', recipe: '', files: {}, vars: '', problem })
  if (!picked.length) return none('')
  if (picked.length > MAX_FILES) return none(`That is ${picked.length} files; choose the directory of the recipe, not a larger one.`)
  // a chosen directory puts its own name first in every path: it is the directory of the recipe and is dropped
  let paths = picked.map((p) => p.path.replace(/^\/+/, ''))
  const first = paths[0].split('/')[0]
  if (paths.every((p) => p.includes('/') && p.split('/')[0] === first)) paths = paths.map((p) => p.slice(first.length + 1))
  const root = paths.map((p, i) => ({ p, f: picked[i].file })).filter((x) => !x.p.includes('/'))
  const yamls = root.filter((x) => isYaml(x.p))
  const chosen = yamls.find((x) => x.p.toLowerCase() === 'recipe.yaml') ?? yamls.find((x) => x.p.toLowerCase() === 'recipe.yml') ?? (yamls.length === 1 ? yamls[0] : undefined)
  if (!chosen) return none(yamls.length ? 'There are several YAML files and none is named recipe.yaml: choose one recipe at a time.' : 'No recipe.yaml in the choice.')
  const out: Imported = { text: '', recipe: chosen.p, files: {}, vars: '', problem: '' }
  const body = await text(chosen.f)
  if (body === null) return none(`${chosen.p} is not text.`)
  out.text = body
  for (let i = 0; i < paths.length; i++) {
    const p = paths[i]
    if (p === chosen.p) continue
    const f = picked[i].file
    if (isVars(p) && !p.includes('/')) {
      out.vars = (await text(f)) ?? ''
      continue
    }
    if (f.size > FILE_MAX) continue // not a regular file of at most 1 MiB: the render says so if the recipe names it
    out.files[p] = await text(f)
  }
  return out
}

/** The files as the renderer takes them. */
export const asSideFiles = (i: Imported): SideFiles => i.files
