// The demo's recipes: the validate endpoint (the checks of the real validator, in short, naming step and field and never
// a value) and the apply of a recipe in a demo jail. A jail keeps what was applied to it, so a second apply says
// `unchanged` as the real one does, and a dry run changes nothing. A command that is `false` fails, with its exit
// status; a requirement whose check is `false` is not met (and stops the apply before any step); a request that carries a secret (sensitive) gets the status only, never the output. Only loaded for `?demo`.
import type { ApplyRequirement, ApplyResult, ApplyStep, Recipe, RecipeStep } from '@jailops/shared/api/types'

export const MAX_STEPS = 200
const KINDS = ['package', 'file', 'dir', 'sysrc', 'service', 'run'] as const
type Kind = (typeof KINDS)[number]

const kindOf = (st: RecipeStep): Kind[] => KINDS.filter((k) => (st as Record<string, unknown>)[k] !== undefined && (st as Record<string, unknown>)[k] !== null)

/** The text of the first problem of a recipe, or '' when it is valid. */
export function recipeProblem(r: Recipe | undefined): string {
  const steps = r?.steps
  if (!Array.isArray(steps) || steps.length < 1 || steps.length > MAX_STEPS) return `a recipe has between 1 and ${MAX_STEPS} steps`
  const reqs = r?.requirements ?? []
  for (let i = 0; i < reqs.length; i++) {
    const q = reqs[i]
    if (!q.check?.length || q.check.some((x) => !x)) return `requirements[${i}].check: a command is a non-empty argv`
    if (!q.message?.trim()) return `requirements[${i}].message: one line of text that tells what is missing`
  }
  for (let i = 0; i < steps.length; i++) {
    const w = `steps[${i}]`
    const st = steps[i]
    if (kindOf(st).length !== 1) return `${w}: a step is exactly one of package, file, dir, sysrc, service and run`
    if (st.file) {
      if (st.file.source === undefined && st.file.content === undefined) return `${w}: a file needs a source or content`
      if (!st.file.path?.startsWith('/')) return `${w}.file.path: a path is absolute`
      if (st.file.mode !== undefined && !/^0?[0-7]{3}$/.test(st.file.mode)) return `${w}.file.mode: must be three octal digits such as 0644 (no set-id or sticky bit)`
    }
    if (st.dir && !st.dir.path?.startsWith('/')) return `${w}.dir.path: a path is absolute`
    if (st.package?.some((p) => !/^[A-Za-z0-9][A-Za-z0-9._+-]*$/.test(p))) return `${w}.package: a name is not a package name`
    if (st.service && !st.service.name) return `${w}.service.name: a service has a name`
    if (st.run && (!st.run.length || st.run.some((x) => !x))) return `${w}.run: a command word is not valid`
  }
  return ''
}

/** What one step is called in a result (kind and name, as the server reports them). */
function label(st: RecipeStep): { kind: Kind; name: string } {
  const kind = kindOf(st)[0]
  switch (kind) {
    case 'package': return { kind, name: st.package!.join(' ') }
    case 'file': return { kind, name: st.file!.path }
    case 'dir': return { kind, name: st.dir!.path }
    case 'sysrc': return { kind, name: Object.keys(st.sysrc!).join(' ') }
    case 'service': return { kind, name: st.service!.name }
    default: return { kind: 'run', name: (st.run ?? []).join(' ') }
  }
}

export class DemoRecipes {
  // what was applied to each jail: the steps by their text, so the same recipe again changes nothing
  private done = new Map<string, Set<string>>()

  private key(st: RecipeStep): string {
    // a file counts by its path and the length of its content: never the content
    if (st.file) return `file ${st.file.path} ${(st.file.content ?? st.file.source ?? '').length} ${st.file.mode ?? ''}`
    return JSON.stringify(st)
  }

  /** The report of a recipe in a jail: a dry run reads and changes nothing; an apply stops at the first failed step. */
  run(jail: string, r: Recipe, dry: boolean, sensitive: boolean): { result: ApplyResult } | { failure: { code: 'requirement_failed' | 'step_failed'; message: string; hint: string } } {
    const seen = this.done.get(jail) ?? new Set<string>()
    const steps: ApplyStep[] = []
    const requirements: ApplyRequirement[] = []
    const unmet: string[] = []
    for (const q of r.requirements ?? []) {
      if (dry) requirements.push({ message: q.message, status: 'not checked' })
      else if (q.check[0] === 'false') unmet.push(`${q.message} (the check exited with status 1)`)
      else requirements.push({ message: q.message, status: 'met' })
    }
    if (unmet.length) {
      const message = unmet.length === 1 ? `requirement not met: ${unmet[0]}` : `${unmet.length} requirements are not met: ${unmet.join('; ')}`
      return { failure: { code: 'requirement_failed', message, hint: 'No step ran and nothing was changed. Make the jail meet the requirements (a change of the jail or of the host is for the administrator) and apply the recipe again.' } }
    }
    const total = r.steps.length
    for (let i = 0; i < total; i++) {
      const st = r.steps[i]
      const { kind, name } = label(st)
      const key = this.key(st)
      let status: string
      let detail = ''
      if (kind === 'run') {
        const cmd = st.run![0]
        if (st.creates || st.unless) {
          status = seen.has(key) ? 'skipped' : dry ? 'would check' : 'executed'
          detail = seen.has(key) ? (st.creates ? `${st.creates} exists` : 'the guard succeeded') : ''
        } else status = dry ? 'would execute' : 'executed'
        if (!dry && cmd === 'false') {
          const done = steps.map((x) => `${x.kind} ${x.name} ${x.status}`)
          const why = sensitive ? 'exit status 1 (the output is withheld: the recipe carries a secret)' : 'exit status 1'
          const hint = `${done.length ? `Before it: ${done.join('; ')}. ` : ''}Nothing is undone: fix the recipe and apply it again.`
          this.done.set(jail, seen)
          return { failure: { code: 'step_failed', message: `step ${i + 1} of ${total} failed (run ${name}): ${why}`, hint } }
        }
      } else if (seen.has(key)) {
        status = 'unchanged'
      } else {
        status = dry ? 'would change' : 'changed'
        if (kind === 'package') detail = (st.package ?? []).map((p) => `install ${p}`).join(', ')
        else if (kind === 'file') detail = sensitive ? 'content not shown' : `${(st.file!.content ?? '').length} bytes, mode ${st.file!.mode ?? '0644'}`
        else if (kind === 'sysrc') detail = Object.entries(st.sysrc!).map(([k, v]) => `${k}="${v}"`).join(', ')
        else if (kind === 'service') detail = [st.service!.enabled ? 'enable' : '', st.service!.running ? 'start' : ''].filter(Boolean).join(', ')
        else if (kind === 'dir') detail = `mode ${st.dir!.mode ?? '0755'}`
      }
      if (!dry && kind !== 'run') seen.add(key)
      if (!dry && kind === 'run' && (st.creates || st.unless)) seen.add(key)
      steps.push({ kind, name, status, ...(detail ? { detail } : {}) })
    }
    if (!dry) this.done.set(jail, seen)
    return { result: { jail, ...(dry ? { dry_run: true } : {}), ...(requirements.length ? { requirements } : {}), steps } }
  }
}
