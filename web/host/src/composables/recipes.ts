// The state of the Recipes tab: the recipe text, the form of its variables, and the three actions (validate, dry run,
// apply). The rendering is local (lib/recipe/render.ts); the endpoints check and run the rendered recipe.
//
// A secret is held here, in memory, and nowhere else: not in the address, not in localStorage, not in a log, not in a
// result. It is cleared when the recipe is applied (whatever the answer) and when the tab goes away.
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch, type ComputedRef, type Ref } from 'vue'
import { api } from '@/api/endpoints'
import { s } from '@/i18n'
import { ApiError } from '@jailops/shared/api/client'
import { runOp } from '@jailops/shared/composables/ops'
import { declarations, parseVarsFile, render, RecipeError, type Rendered, type Values, type VarDecl } from '@/lib/recipe/render'
import { readImport, pick, type Imported } from '@/lib/recipe/import'
import type { ApplyResult } from '@jailops/shared/api/types'

export interface RunResult {
  dry: boolean
  result: ApplyResult
}

const asApiError = (e: unknown): ApiError => (e instanceof ApiError ? e : new ApiError({ code: 'internal', message: e instanceof Error ? e.message : String(e) }))

export function useRecipe(jail: ComputedRef<string> | Ref<string>) {
  const text = ref('')
  const files = ref<Record<string, string | null>>({})
  const form = reactive<Record<string, string>>({})
  const busy = ref<'' | 'validate' | 'dry' | 'apply'>('')
  /** A problem with the recipe or the variables, found while rendering: named by step and field, never by value. */
  const problem = ref('')
  /** The number of steps of the last recipe the server found valid. */
  const valid = ref<number | null>(null)
  const error = ref<ApiError | null>(null)
  const last = ref<RunResult | null>(null)
  const note = ref('')

  const parsed = computed<{ decls: VarDecl[]; error: string }>(() => {
    if (!text.value.trim()) return { decls: [], error: '' }
    try {
      return { decls: declarations(text.value), error: '' }
    } catch (e) {
      return { decls: [], error: e instanceof RecipeError ? e.message : String(e) }
    }
  })
  const decls = computed(() => parsed.value.decls)
  const declError = computed(() => parsed.value.error)

  // a variable that is no longer declared leaves the form; the others keep what was typed
  watch(decls, (list) => {
    const names = new Set(list.map((d) => d.name))
    for (const k of Object.keys(form)) if (!names.has(k)) delete form[k]
    for (const d of list) if (!(d.name in form)) form[d.name] = ''
  }, { immediate: true })
  // an edit makes the last check and the last problem stale
  watch([text, files, () => ({ ...form })], () => {
    valid.value = null
    problem.value = ''
  })

  function clearSecrets(): void {
    for (const d of decls.value) if (d.secret) form[d.name] = ''
  }
  onBeforeUnmount(clearSecrets)

  /** What the form gives: a secret through setFile (never as a plain value), the rest as set; an empty field is not given. */
  function values(): Values {
    const set: Record<string, string> = {}
    const setFile: Record<string, string> = {}
    for (const d of decls.value) {
      const v = form[d.name] ?? ''
      if (v === '') continue
      if (d.secret) setFile[d.name] = v
      else set[d.name] = v
    }
    return { set, setFile }
  }

  /** Render with the form. A missing required value is said by name before the renderer is asked. */
  function build(): Rendered | null {
    problem.value = ''
    if (!text.value.trim()) {
      problem.value = s.recipes.empty
      return null
    }
    for (const d of decls.value) {
      if (d.required && !(form[d.name] ?? '')) {
        problem.value = s.recipes.needValue(d.name)
        return null
      }
    }
    try {
      return render(text.value, values(), files.value)
    } catch (e) {
      problem.value = e instanceof RecipeError ? e.message : String(e)
      return null
    }
  }

  async function validate(): Promise<void> {
    error.value = null
    valid.value = null
    const r = build()
    if (!r) return
    busy.value = 'validate'
    try {
      const out = await api.recipes.validate({ recipe: r.recipe, sensitive: r.sensitive || undefined })
      valid.value = out.kind === 'sync' ? out.data.steps : r.recipe.steps.length
    } catch (e) {
      error.value = asApiError(e)
    } finally {
      busy.value = ''
    }
  }

  async function run(dry: boolean): Promise<void> {
    error.value = null
    const r = build()
    if (!r) return
    busy.value = dry ? 'dry' : 'apply'
    const name = jail.value
    try {
      const out = await runOp(dry ? s.recipes.dryTitle(name) : s.recipes.applyTitle(name), () =>
        api.recipes.apply(name, { recipe: r.recipe, dry_run: dry || undefined, sensitive: r.sensitive || undefined }))
      const res = (out.op?.metadata ?? out.data) as ApplyResult | undefined
      last.value = { dry, result: res ?? { jail: name, dry_run: dry, steps: [] } }
    } catch (e) {
      last.value = null
      error.value = asApiError(e)
    } finally {
      busy.value = ''
      if (!dry) clearSecrets()
    }
  }

  /** Take what was chosen: the recipe, the files beside it, and the values of a vars file into the form. */
  async function importFiles(list: FileList | File[]): Promise<void> {
    note.value = ''
    problem.value = ''
    const got: Imported = await readImport(pick(list))
    if (got.problem) {
      problem.value = got.problem
      return
    }
    if (!got.recipe) return
    text.value = got.text
    files.value = got.files
    last.value = null
    error.value = null
    const names = Object.keys(got.files).length
    note.value = s.recipes.imported(got.recipe, names)
    if (got.vars) {
      try {
        const given = parseVarsFile(got.vars)
        // wait for the declarations of the new text, then fill what the file names (a secret is never read from a file)
        await nextTick()
        for (const d of decls.value) if (!d.secret && d.name in given) form[d.name] = given[d.name]
      } catch (e) {
        problem.value = e instanceof RecipeError ? e.message : String(e)
      }
    }
  }

  return { text, files, form, decls, declError, busy, problem, valid, error, last, note, validate, run, importFiles, clearSecrets, build }
}
