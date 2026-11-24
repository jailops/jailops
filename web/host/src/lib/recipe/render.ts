// The rendering of a recipe in the browser: the same rules as internal/recipe (docs/provisioning.md, "Variables"), made
// to give the same answer for every case of internal/recipe/testdata/render_vectors.json (render.test.ts reads that
// file). It renders only: the check of the finished recipe is the validate endpoint (the Go validator is the only one).
//
// The rules in short: `vars:` declares NAME: {default, required, secret, split}; ${name} is replaced in every string of
// a step and, with template: true, in the body of a file; $${ is a literal ${ and $$ stays as it is; the value comes
// from the default, then a vars file, then the form; a secret has no default and goes only into the content of a
// template file; the files beside the recipe are read into `content`.
//
// An error names the step and the field (steps[2].run[1]) and the variable, never a value or the content of a file:
// no message here is made from a value, and the text of a YAML error is cut to its first line (the library prints the
// line of the source after it).
import { isAlias, isMap, isScalar, isSeq, parseAllDocuments, type Document, type Node, type Pair, type Scalar, type YAMLMap } from 'yaml'

export class RecipeError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'RecipeError'
  }
}

/** One variable of the `vars:` section, in the order of the file: a form is made from the list. */
export interface VarDecl {
  name: string
  /** The default as the file writes it (22 is "22", true is "true"); undefined when there is none. */
  default?: string
  required: boolean
  secret: boolean
  split: boolean
}

/** What the form gives: set is --set, setFile is --set-file (a secret, or a value of several lines), vars is a vars file. */
export interface Values {
  set?: Record<string, string>
  setFile?: Record<string, string>
  vars?: Record<string, string>
}

export interface RenderedStep {
  package?: string[]
  file?: { path: string; source?: string; content?: string; mode?: string; owner?: string; group?: string }
  dir?: { path: string; mode?: string; owner?: string; group?: string }
  sysrc?: Record<string, string>
  in?: string
  service?: { name: string; enabled?: boolean; running?: boolean }
  run?: string[]
  creates?: string
  unless?: string[]
}

export interface Rendered {
  /** The recipe as it is sent: variables replaced, files read into content, vars: removed. */
  recipe: { steps: RenderedStep[] }
  decls: VarDecl[]
  /** True when a secret went into the recipe: the request must say so (sensitive: true). */
  sensitive: boolean
}

/** The files beside the recipe by their path below its directory; null is a file that is not text. */
export type SideFiles = Record<string, string | null> | ReadonlyMap<string, string | null>

/** Bytes of one file of a recipe (validate.RecipeFileMax). */
export const FILE_MAX = 1 << 20

const NAME = /^[A-Za-z_][A-Za-z0-9_]*$/
// the white space of Go's strings.Fields
const SPACE = new RegExp('[' + [0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x20, 0x85, 0xa0, 0x1680, 0x2000, 0x2001, 0x2002, 0x2003, 0x2004, 0x2005, 0x2006, 0x2007, 0x2008, 0x2009, 0x200a, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000].map((c) => '\\u' + c.toString(16).padStart(4, '0')).join('') + ']+')
const bytesOf = (s: string) => new TextEncoder().encode(s).length

type Obj = Record<string, unknown>
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v)

// ---- parsing ----

function yamlText(e: { message: string; linePos?: Array<{ line: number }> }): string {
  const first = e.message.split('\n')[0].replace(/ at line \d+, column \d+:?$/, '')
  const line = e.linePos?.[0]?.line
  return `invalid YAML: ${first}${line ? ` (line ${line})` : ''}`
}

interface Doc {
  doc: Document.Parsed
  vars?: Node | null
  steps?: Node | null
  others: Obj
}

function parseDoc(text: string): Doc {
  const docs = parseAllDocuments(text, { version: '1.2', merge: true, strict: true })
  const list = Array.isArray(docs) ? docs : [docs]
  for (const d of list) if (d.errors.length) throw new RecipeError(yamlText(d.errors[0]))
  const filled = list.filter((d) => d.contents !== null)
  if (filled.length === 0) throw new RecipeError('the recipe is empty')
  if (filled.length > 1) throw new RecipeError('a recipe is one document')
  const doc = filled[0] as Document.Parsed
  const root = doc.contents
  if (!isMap(root)) throw new RecipeError('a recipe is a map with steps:')
  const out: Doc = { doc, others: {} }
  for (const p of root.items) {
    const key = text0(p.key)
    if (key === 'vars') out.vars = (p.value as Node | null) ?? null
    else if (key === 'steps') out.steps = (p.value as Node | null) ?? null
    else out.others[key] = isScalar(p.value) || isMap(p.value) || isSeq(p.value) ? (p.value as Node).toJS(doc) : null
  }
  return out
}

const res = (doc: Document, n: unknown): unknown => (isAlias(n) ? n.resolve(doc) : n)

/** The text of a scalar as written: a number or a bool keeps its spelling, a string is its content. */
function text0(n: unknown): string {
  if (!isScalar(n)) return String(n)
  const v = (n as Scalar).value
  if (typeof v === 'string') return v
  const src = (n as Scalar).source
  return typeof src === 'string' && src !== '' ? src : String(v)
}

const isNull = (n: unknown): boolean => n === null || n === undefined || (isScalar(n) && (n as Scalar).value === null)

function parseDecls(doc: Document, node: Node | null | undefined): VarDecl[] {
  node = res(doc, node) as Node | null | undefined
  if (isNull(node)) return []
  if (!isMap(node)) throw new RecipeError('vars: a map of NAME: {default, required, secret, split}')
  const out: VarDecl[] = []
  const seen = new Set<string>()
  for (const p of (node as YAMLMap).items as Array<Pair<unknown, unknown>>) {
    const name = text0(p.key)
    const where = `vars.${name}`
    if (!NAME.test(name)) throw new RecipeError(`vars: ${JSON.stringify(name)} is not a variable name (letters, digits and _, not starting with a digit)`)
    if (seen.has(name)) throw new RecipeError(`${where}: declared twice`)
    seen.add(name)
    const d: VarDecl = { name, required: false, secret: false, split: false }
    const body = res(doc, p.value)
    if (isNull(body)) {
      // a declaration without a body: no default
    } else if (!isMap(body)) {
      throw new RecipeError(`${where}: a map with default, required, secret and split`)
    } else {
      for (const q of (body as YAMLMap).items as Array<Pair<unknown, unknown>>) {
        const key = text0(q.key)
        const val = res(doc, q.value)
        const flag = (): boolean => {
          if (!isScalar(val) || typeof (val as Scalar).value !== 'boolean') throw new RecipeError(`${where}.${key}: true or false`)
          return (val as Scalar).value as boolean
        }
        switch (key) {
          case 'default':
            if (!isScalar(val) && !isNull(val)) throw new RecipeError(`${where}.default: one value`)
            if (!isNull(val)) d.default = text0(val)
            break
          case 'required': d.required = flag(); break
          case 'secret': d.secret = flag(); break
          case 'split': d.split = flag(); break
          default: throw new RecipeError(`${where}: unknown key ${JSON.stringify(key)} (default, required, secret, split)`)
        }
      }
    }
    if (d.secret && d.default !== undefined) throw new RecipeError(`${where}: a secret has no default`)
    if (d.secret && d.split) throw new RecipeError(`${where}: a secret is not split`)
    if (d.required && d.default !== undefined) throw new RecipeError(`${where}: a variable is required or has a default, not both`)
    out.push(d)
  }
  return out
}

/** The declared variables of a recipe, for the form. Throws a RecipeError when the file is not usable. */
export function declarations(text: string): VarDecl[] {
  const d = parseDoc(text)
  return parseDecls(d.doc, d.vars)
}

/** A vars file: lines NAME=VALUE, # comments, optional quotes (the same reading as `--vars FILE`). */
export function parseVarsFile(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  const lines = text.split('\n')
  for (let i = 0; i < lines.length; i++) {
    let line = lines[i].replace(/\r$/, '').trim()
    if (line === '' || line[0] === '#') continue
    if (line.startsWith('export ')) line = line.slice(7)
    const eq = line.indexOf('=')
    const k = eq < 0 ? '' : line.slice(0, eq).trim()
    if (eq < 0 || !NAME.test(k)) throw new RecipeError(`line ${i + 1}: want NAME=VALUE`)
    let val = line.slice(eq + 1).replace(/^[ \t]+/, '')
    if (val !== '' && (val[0] === '"' || val[0] === "'")) {
      const end = val.indexOf(val[0], 1)
      if (end < 0) throw new RecipeError(`line ${i + 1}: ${k} has an opening quote and no closing one`)
      const rest = val.slice(end + 1).trim()
      if (rest !== '' && rest[0] !== '#') throw new RecipeError(`line ${i + 1}: ${k} has text after the closing quote`)
      val = val.slice(1, end)
    } else {
      const j = val.search(/[ \t]/)
      if (j >= 0 && val.slice(j).replace(/^[ \t]+/, '').startsWith('#')) val = val.slice(0, j)
      val = val.replace(/[ \t]+$/, '')
    }
    out[k] = val
  }
  return out
}

// ---- rendering ----

class Renderer {
  vals = new Map<string, string>()
  decls = new Map<string, VarDecl>()
  used = new Set<string>()
  secrets = false

  resolve(decls: VarDecl[], v: Values): void {
    for (const d of decls) this.decls.set(d.name, d)
    for (const [flag, m] of [['--set', v.set], ['--set-file', v.setFile]] as const) {
      for (const k of Object.keys(m ?? {})) if (!this.decls.has(k)) throw new RecipeError(`${flag} ${k}: the recipe declares no such variable`)
    }
    for (const d of decls) {
      const inSet = !!v.set && Object.hasOwn(v.set, d.name)
      const inFile = !!v.setFile && Object.hasOwn(v.setFile, d.name)
      let val = ''
      let have = false
      if (inSet && inFile) throw new RecipeError(`${d.name}: given with --set and --set-file`)
      if (inSet) {
        if (d.secret) throw new RecipeError(`${d.name} is secret: a value on the command line is kept in the shell history and shown by ps; give it with --set-file`)
        val = v.set![d.name]
        have = true
        if (/[\r\n]/.test(val)) throw new RecipeError(`${d.name}: a value of several lines is given with --set-file`)
      } else if (inFile) {
        val = v.setFile![d.name]
        have = true
      } else if (v.vars && Object.hasOwn(v.vars, d.name)) {
        if (d.secret) throw new RecipeError(`${d.name} is secret: give it with --set-file, not in a --vars file`)
        val = v.vars[d.name]
        have = true
        if (/[\r\n]/.test(val)) throw new RecipeError(`${d.name}: a value of several lines is given with --set-file`)
      }
      if (!have) {
        if (d.required && d.secret) throw new RecipeError(`${d.name} is required: give it with --set-file ${d.name}=FILE`)
        if (d.required) throw new RecipeError(`${d.name} is required: give it with --set ${d.name}=VALUE or in a --vars file`)
        // a default may use the variables declared before it
        if (d.default !== undefined) val = this.expand(`vars.${d.name}.default`, d.default, false)
      }
      this.vals.set(d.name, val)
    }
  }

  /** Replace the ${name} of s. where names the place for an error; secrets says whether a secret may go in. */
  expand(where: string, s: string, secrets: boolean): string {
    if (!s.includes('$')) return s
    let b = ''
    for (let i = 0; i < s.length; ) {
      if (s.startsWith('$${', i)) {
        b += '${'
        i += 3
      } else if (s.startsWith('${', i)) {
        const end = s.indexOf('}', i)
        const name = end > 0 ? s.slice(i + 2, end) : ''
        const before = s.slice(0, i)
        const at = before.includes('\n') ? `${where}, line ${1 + before.split('\n').length - 1}` : where
        if (end < 0 || !NAME.test(name)) throw new RecipeError(`${at}: a \${ must hold a variable name and end with } (write $\${ for a literal \${)`)
        const d = this.decls.get(name)
        if (!d) throw new RecipeError(`${at}: \${${name}} is not declared under vars: (write $\${ for a literal \${)`)
        if (d.secret && !secrets) throw new RecipeError(`${at}: ${name} is secret and may be used only in the content of a file step with template: true`)
        this.used.add(name)
        if (d.secret) this.secrets = true
        b += this.vals.get(name) ?? ''
        i = end + 1
      } else {
        b += s[i]
        i++
      }
    }
    return b
  }

  private whole(s: string): string | undefined {
    return s.length > 3 && s.startsWith('${') && s.endsWith('}') && NAME.test(s.slice(2, -1)) ? s.slice(2, -1) : undefined
  }

  /** The words of a list field: a word that is exactly the ${name} of a split variable is several words. */
  list(where: string, input: unknown[]): unknown[] {
    const out: unknown[] = []
    input.forEach((e, i) => {
      const w = `${where}[${i}]`
      if (typeof e !== 'string') {
        out.push(e)
        return
      }
      const name = this.whole(e)
      if (name && this.decls.get(name)?.split) {
        this.expand(w, e, false) // marks the use
        for (const f of (this.vals.get(name) ?? '').split(SPACE).filter(Boolean)) out.push(f)
        return
      }
      out.push(this.expand(w, e, false))
    })
    return out
  }

  /** Every string below v (not the keys of a map). */
  value(where: string, v: unknown): unknown {
    if (typeof v === 'string') return this.expand(where, v, false)
    if (Array.isArray(v)) return v.map((e, i) => this.value(`${where}[${i}]`, e))
    if (isObj(v)) return Object.fromEntries(Object.entries(v).map(([k, e]) => [k, this.value(`${where}.${k}`, e)]))
    return v
  }

  step(where: string, m: Obj, files: SideFiles | undefined): Obj {
    const out: Obj = {}
    for (const [k, v] of Object.entries(m)) {
      const w = `${where}.${k}`
      if (k === 'file') {
        if (!isObj(v)) throw new RecipeError(`${w}: a map with path, source or content, mode, owner, group`)
        out[k] = this.file(w, v, files)
      } else if (k === 'package' || k === 'run' || k === 'unless') {
        if (Array.isArray(v)) out[k] = this.list(w, v)
        else if (typeof v === 'string') {
          const name = this.whole(v)
          if (k === 'package' && name && this.decls.get(name)?.split) out[k] = this.list(w, [v])
          else out[k] = this.value(w, v)
        } else out[k] = this.value(w, v)
      } else {
        out[k] = this.value(w, v)
      }
    }
    return out
  }

  /** The body of a file step: the text of content or of the source beside the recipe, with the variables when template is true. */
  file(where: string, f: Obj, files: SideFiles | undefined): Obj {
    let tmpl = false
    if ('template' in f) {
      if (typeof f.template !== 'boolean') throw new RecipeError(`${where}.template: true or false`)
      tmpl = f.template
    }
    const out: Obj = {}
    for (const [k, v] of Object.entries(f)) {
      if (k === 'template' || k === 'content') continue
      out[k] = this.value(`${where}.${k}`, v)
    }
    const hasContent = 'content' in f
    const hasSource = typeof out.source === 'string'
    if ('source' in f && !hasSource) throw new RecipeError(`${where}.source: a file name`)
    if (hasContent && hasSource) throw new RecipeError(`${where}: give a source or content, not both`)
    let body: string
    if (hasSource) {
      body = readSource(files, out.source as string, `${where}.source`)
      delete out.source
    } else if (hasContent) {
      if (typeof f.content !== 'string') throw new RecipeError(`${where}.content: text; put quotes around it`)
      body = f.content
    } else {
      if (tmpl) throw new RecipeError(`${where}: template: true needs a source or content`)
      return out // validate says that a file needs a source or content
    }
    if (tmpl) body = this.expand(`${where} (template)`, body, true)
    out.content = body
    return out
  }
}

/** The clean form of a slash path (path.Clean): no empty, . or .. segments inside it. */
function clean(p: string): string {
  const parts: string[] = []
  const abs = p.startsWith('/')
  for (const seg of p.split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') {
      if (parts.length && parts[parts.length - 1] !== '..') parts.pop()
      else if (!abs) parts.push('..')
    } else parts.push(seg)
  }
  const out = (abs ? '/' : '') + parts.join('/')
  return out === '' ? '.' : out
}

function readSource(files: SideFiles | undefined, rel: string, where: string): string {
  if (!files) throw new RecipeError(`${where}: a recipe on standard input has no files beside it: use content`)
  if (rel.startsWith('/') || clean(rel) !== rel || rel === '..' || rel.startsWith('../')) throw new RecipeError(`${where}: ${rel} must be a path below the directory of the recipe`)
  const data = files instanceof Map ? files.get(rel) : Object.hasOwn(files, rel) ? (files as Record<string, string | null>)[rel] : undefined
  if (data === undefined || (data !== null && bytesOf(data) > FILE_MAX)) {
    throw new RecipeError(`${where}: ${rel} is not a regular file of at most ${FILE_MAX >> 20} MiB below the directory of the recipe (a link that leads out of it is refused)`)
  }
  if (data === null) throw new RecipeError(`${where}: ${rel} is not text; copy other files with 'jailops cp'`)
  return data
}

// ---- the typed recipe ----

type Kind = 'string' | 'strings' | 'bool' | 'map' | 'object'
const STEP: Record<string, Kind | Record<string, Kind>> = {
  package: 'strings', in: 'string', run: 'strings', creates: 'string', unless: 'strings', sysrc: 'map',
  file: { path: 'string', source: 'string', content: 'string', mode: 'string', owner: 'string', group: 'string' },
  dir: { path: 'string', mode: 'string', owner: 'string', group: 'string' },
  service: { name: 'string', enabled: 'bool', running: 'bool' },
}

function jsonType(v: unknown): string {
  if (typeof v === 'boolean') return 'bool'
  if (typeof v === 'number') return 'number'
  if (typeof v === 'string') return 'string'
  return Array.isArray(v) ? 'array' : 'object'
}

/** A value of the wrong type: YAML read a word such as false, yes, 5 or 0644 as a boolean or a number where text is wanted. */
function typeError(where: string, v: unknown, want: 'string' | 'other'): RecipeError {
  const t = jsonType(v)
  const got = t === 'bool' ? 'a true/false value' : t === 'number' ? 'a number' : t
  if (want === 'string') return new RecipeError(`${where}: YAML reads this as ${got}, not as text: put quotes around it ("false", "0644", "5")`)
  return new RecipeError(`${where}: wrong type of value (${t})`)
}

function checkString(where: string, v: unknown): string {
  if (typeof v !== 'string') throw typeError(where, v, 'string')
  return v
}

/** The rendered tree as the typed recipe: what the daemon does not know is refused, as the Go decoder refuses it. */
function typed(steps: Obj[], others: Obj): { steps: RenderedStep[] } {
  for (const k of Object.keys(others)) throw new RecipeError(`unknown field ${JSON.stringify(k)}`)
  const out: RenderedStep[] = steps.map((st, i) => {
    const where = `steps[${i}]`
    const o: Obj = {}
    for (const [k, v] of Object.entries(st)) {
      const w = `${where}.${k}`
      const kind = STEP[k]
      if (kind === undefined) throw new RecipeError(`${where}: unknown field ${JSON.stringify(k)}`)
      if (v === null) continue
      if (typeof kind === 'string') {
        if (kind === 'string') o[k] = checkString(w, v)
        else if (kind === 'strings') {
          if (!Array.isArray(v)) throw typeError(w, v, 'other')
          o[k] = v.map((e, j) => (e === null ? '' : checkString(`${w}[${j}]`, e)))
        } else {
          if (!isObj(v)) throw typeError(w, v, 'other')
          o[k] = Object.fromEntries(Object.entries(v).filter(([, e]) => e !== null).map(([key, e]) => [key, checkString(`${w}.${key}`, e)]))
        }
        continue
      }
      if (!isObj(v)) throw typeError(w, v, 'other')
      const sub: Obj = {}
      for (const [fk, fv] of Object.entries(v)) {
        const fkind = kind[fk]
        if (fkind === undefined) throw new RecipeError(`${w}: unknown field ${JSON.stringify(fk)}`)
        if (fv === null) continue
        if (fkind === 'bool') {
          if (typeof fv !== 'boolean') throw typeError(`${w}.${fk}`, fv, 'other')
          sub[fk] = fv
        } else sub[fk] = checkString(`${w}.${fk}`, fv)
      }
      o[k] = sub
    }
    return o as RenderedStep
  })
  return { steps: out }
}

/**
 * Render the recipe text: the variables replaced, the files beside it read in, vars: removed. files are the files below
 * the directory of the recipe by their relative path; leave it out for a recipe that has none (a source is then an error).
 * Throws a RecipeError whose text names the step and the field and holds no value.
 */
export function render(text: string, values: Values = {}, files?: SideFiles): Rendered {
  const d = parseDoc(text)
  const decls = parseDecls(d.doc, d.vars)
  const r = new Renderer()
  r.resolve(decls, values)
  let steps: unknown[] = []
  const sn = res(d.doc, d.steps) as Node | null | undefined
  if (!isNull(sn)) {
    if (!isSeq(sn)) throw new RecipeError('steps: a list of steps')
    steps = (sn as Node).toJS(d.doc) as unknown[]
  }
  const rendered = steps.map((s, i) => {
    if (!isObj(s)) throw new RecipeError(`steps[${i}]: a step is a map with one of package, file, dir, sysrc, service, run`)
    return r.step(`steps[${i}]`, s, files)
  })
  for (const decl of decls) if (!r.used.has(decl.name)) throw new RecipeError(`vars.${decl.name}: declared and never used in the recipe`)
  return { recipe: typed(rendered, d.others), decls, sensitive: r.secrets }
}
