// The browser renders a recipe as internal/recipe does: every case of the shared vectors, read from the Go package's
// file (no copy), and the cases that only the browser has (YAML typing, the files it reads, a value never in a message).
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { declarations, parseVarsFile, render, RecipeError, type Values } from './render'

interface Vector {
  name: string
  recipe: string
  files?: Record<string, string>
  set?: Record<string, string>
  set_file?: Record<string, string>
  vars?: Record<string, string>
  want?: unknown
  sensitive?: boolean
  error?: string
}
const vectors = JSON.parse(readFileSync(join(import.meta.dirname, '../../../../../internal/recipe/testdata/render_vectors.json'), 'utf8')) as Vector[]

function values(v: Vector): Values {
  return { set: v.set, setFile: v.set_file, vars: v.vars }
}
function fail(f: () => unknown): RecipeError {
  try {
    f()
  } catch (e) {
    expect(e).toBeInstanceOf(RecipeError)
    return e as RecipeError
  }
  throw new Error('rendered without an error')
}

describe('shared vectors', () => {
  it('has the whole file', () => {
    expect(vectors.length).toBeGreaterThanOrEqual(77)
  })
  for (const v of vectors) {
    it(v.name, () => {
      // a recipe without files has the directory of a recipe and nothing else, as the Go test's map of files
      const files = v.files ?? {}
      if (v.error) {
        const e = fail(() => render(v.recipe, values(v), files))
        expect(e.message).toContain(v.error)
        // an error names the step and the field, never a value
        for (const m of [v.set, v.set_file, v.vars]) for (const val of Object.values(m ?? {})) if (val.length >= 8) expect(e.message).not.toContain(val)
        return
      }
      const r = render(v.recipe, values(v), files)
      expect(r.recipe).toEqual(v.want)
      expect(r.sensitive).toBe(!!v.sensitive)
    })
  }
})

describe('typed YAML', () => {
  const run = (def: string) => render(`vars:\n  x: {default: ${def}}\nsteps:\n  - run: [echo, "\${x}"]\n`).recipe.steps[0].run
  it('keeps the text of a default', () => {
    expect(run('22')).toEqual(['echo', '22'])
    expect(run('1.0')).toEqual(['echo', '1.0'])
    expect(run('0x1F')).toEqual(['echo', '0x1F'])
    expect(run('True')).toEqual(['echo', 'True'])
    expect(run('"null"')).toEqual(['echo', 'null'])
    expect(run('~')).toEqual(['echo', ''])
    expect(run('')).toEqual(['echo', ''])
  })
  it('lists the declarations in order, a default as text', () => {
    const d = declarations('vars:\n  b: {default: 22}\n  a: {required: true}\n  s: {secret: true}\n  p: {split: true}\n  n:\nsteps: []\n')
    expect(d.map((x) => x.name)).toEqual(['b', 'a', 's', 'p', 'n'])
    expect(d[0].default).toBe('22')
    expect(d[1]).toMatchObject({ required: true })
    expect(d[2]).toMatchObject({ secret: true })
    expect(d[3]).toMatchObject({ split: true })
    expect(d[4].default).toBeUndefined()
  })
  it('refuses a number or a boolean where text is wanted, saying where', () => {
    for (const [yaml, where] of [['mode: 0644', 'steps[0].file.mode'], ['mode: yes\n      owner: 5', 'steps[0].file.owner']] as const) {
      const e = fail(() => render(`steps:\n  - file:\n      path: /a\n      content: x\n      ${yaml}\n`))
      expect(e.message).toContain(where)
    }
    expect(fail(() => render('steps:\n  - run: ls\n')).message).toContain('steps[0].run')
    expect(fail(() => render('steps:\n  - service: {name: a, enabled: "yes"}\n')).message).toContain('steps[0].service.enabled')
  })
  it('keeps true and false of a service', () => {
    expect(render('steps:\n  - service: {name: sshd, enabled: true, running: false}\n').recipe.steps[0].service).toEqual({ name: 'sshd', enabled: true, running: false })
  })
  it('refuses a top-level key that is not vars or steps, and an unknown key of a file', () => {
    expect(fail(() => render('name: x\nsteps: []\n')).message).toContain('unknown field')
    expect(fail(() => render('steps:\n  - file: {path: /a, content: x, perms: "0600"}\n')).message).toContain('unknown field')
  })
  it('refuses duplicate keys, an empty file, a list as the recipe and bad steps', () => {
    expect(fail(() => render('steps: []\nsteps: []\n')).message).toContain('invalid YAML')
    expect(fail(() => render('')).message).toBe('the recipe is empty')
    expect(fail(() => render('- a\n')).message).toContain('a map with steps')
    expect(fail(() => render('steps: x\n')).message).toBe('steps: a list of steps')
    expect(fail(() => render('steps:\n  - x\n')).message).toContain('steps[0]')
  })
  it('renders a recipe with no vars: and no steps', () => {
    expect(render('steps:\n').recipe).toEqual({ steps: [] })
  })
})

describe('files beside the recipe', () => {
  const rec = 'steps:\n  - file: {path: /a, source: files/a.conf}\n'
  it('are inlined as content and the source goes', () => {
    const r = render(rec, {}, new Map([['files/a.conf', 'hello']]))
    expect(r.recipe.steps[0].file).toEqual({ path: '/a', content: 'hello' })
  })
  it('refuse a file that is not there, not text, too big or outside', () => {
    expect(fail(() => render(rec, {}, {})).message).toContain('files/a.conf is not a regular file')
    expect(fail(() => render(rec, {}, { 'files/a.conf': null })).message).toContain('is not text')
    expect(fail(() => render(rec, {}, { 'files/a.conf': 'x'.repeat(1 << 20) + 'x' })).message).toContain('at most 1 MiB')
    expect(fail(() => render('steps:\n  - file: {path: /a, source: ../a}\n', {}, {})).message).toContain('below the directory')
    expect(fail(() => render('steps:\n  - file: {path: /a, source: /etc/passwd}\n', {}, {})).message).toContain('below the directory')
    expect(fail(() => render(rec)).message).toContain('no files beside it')
  })
})

describe('a secret', () => {
  const SECRET = 'S3cr3t-Value-ABC'
  const rec = 'vars:\n  pw: {secret: true, required: true}\nsteps:\n  - file:\n      path: /a\n      template: true\n      mode: "0600"\n      content: "pw=${pw}"\n'
  it('goes into the template body and marks the request', () => {
    const r = render(rec, { setFile: { pw: SECRET } })
    expect(r.sensitive).toBe(true)
    expect(r.recipe.steps[0].file?.content).toBe(`pw=${SECRET}`)
  })
  it('is in no error, whatever is wrong with the recipe', () => {
    const bad = [
      rec.replace('mode: "0600"', 'mode: 0600'),
      rec.replace('content: "pw=${pw}"', 'content: "pw=${pw} ${nope}"'),
      rec + '  - run: [echo, "${pw}"]\n',
      rec.replace('path: /a', 'path: /a/${pw}'),
      rec.replace('{secret: true, required: true}', '{secret: true, default: x}'),
      rec + '  - bogus: 1\n',
      rec.replace('steps:', 'steps: [\n'),
    ]
    for (const b of bad) {
      const e = fail(() => render(b, { setFile: { pw: SECRET } }, {}))
      expect(e.message, b).not.toContain(SECRET)
    }
    // given the wrong way: refused without the value
    expect(fail(() => render(rec, { set: { pw: SECRET } })).message).not.toContain(SECRET)
    expect(fail(() => render(rec, { vars: { pw: SECRET } })).message).not.toContain(SECRET)
    expect(fail(() => render(rec, { set: { nope: SECRET } })).message).not.toContain(SECRET)
  })
  it('does not leak from a YAML error that holds source text', () => {
    const e = fail(() => render(`steps:\n  - run: [echo, "${SECRET}]\n`))
    expect(e.message).not.toContain(SECRET)
    expect(e.message).toContain('invalid YAML')
  })
  it('is not sensitive when it is declared and given but the request is refused', () => {
    expect(fail(() => render(rec.replace('template: true', 'template: false'), { setFile: { pw: SECRET } })).message).toContain('declared and never used')
  })
})

describe('vars file', () => {
  it('reads lines, comments and quotes', () => {
    expect(parseVarsFile('# c\nA=1\nexport B="two words"  # c\nC=\'x\'\nD=v # c\n\n')).toEqual({ A: '1', B: 'two words', C: 'x', D: 'v' })
    expect(() => parseVarsFile('nope\n')).toThrow('line 1: want NAME=VALUE')
    expect(() => parseVarsFile('A="x\n')).toThrow('closing')
  })
})
