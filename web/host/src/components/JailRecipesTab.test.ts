import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'

// the requests the tab makes, recorded on their way to the demo transport
const seen = vi.hoisted(() => ({ validate: [] as unknown[], apply: [] as unknown[] }))
vi.mock('@/api/endpoints', async (orig) => {
  const real = await orig<typeof import('@/api/endpoints')>()
  return {
    api: {
      ...real.api,
      recipes: {
        validate: (b: unknown) => { seen.validate.push(structuredClone(b)); return real.api.recipes.validate(b as never) },
        apply: (n: string, b: unknown) => { seen.apply.push(structuredClone(b)); return real.api.recipes.apply(n, b as never) },
      },
    },
  }
})

import JailRecipesTab from './JailRecipesTab.vue'
import { Client, getClient, setClient } from '@jailops/shared/api/client'
import { POLL_MS } from '@jailops/shared/composables/ops'
import { session } from '@jailops/shared/composables/session'
import { demoTransport } from '@/demo/transport'
import { server } from '@/demo/data'
import { projectState } from '@/composables/project'
import { withProject } from '@/lib/projectScope'
import { api } from '@/api/endpoints'

const jail = (over: Record<string, unknown> = {}, power = 'running', labels: Record<string, string> = {}) =>
  ({ metadata: { name: 'web', project: 'web', labels }, spec: { os: 'freebsd', ...over }, status: { power } }) as never
const SECRET = 'Zq9-correct-horse-battery'
const recipe = 'steps:\n  - package: [nginx]\n  - file:\n      path: /usr/local/etc/app.conf\n      content: hello\n      mode: "0644"\n'
const withVars = `vars:
  site: {default: example.test}
  port: {default: 22}
  pw: {secret: true, required: true}
steps:
  - sysrc: {sshd_port: "\${port}"}
  - file:
      path: /usr/local/etc/app.conf
      template: true
      mode: "0600"
      content: "site=\${site}\\npassword=\${pw}\\n"
`

describe('the Recipes tab', () => {
  const before = getClient()
  let w: VueWrapper | null = null
  const mountTab = (j = jail()) => {
    w = mount(JailRecipesTab, { props: { jail: j }, attachTo: document.body })
    return w
  }
  const t = (id: string) => w!.get(`[data-test="${id}"]`)
  const settle = async () => {
    for (let i = 0; i < 12; i++) {
      await new Promise((r) => setTimeout(r, 10))
      await flushPromises()
    }
  }
  const type = async (yaml: string) => { await t('r-text').setValue(yaml); await flushPromises() }

  beforeEach(() => {
    POLL_MS.value = 1
    seen.validate.length = 0
    seen.apply.length = 0
    localStorage.clear()
    setClient(new Client(demoTransport({ speed: 1000 })))
    Object.assign(session, { authed: true, info: server(), role: 'admin', global: true, user: 'alice', projects: [] })
    projectState.current = 'web'
    api.setProjectFilter((p) => withProject(p, 'web'))
  })
  afterEach(() => {
    POLL_MS.value = 150
    w?.unmount()
    w = null
    api.setProjectFilter(null)
    setClient(before)
  })

  it('validates the rendered recipe with the endpoint, no jail needed', async () => {
    mountTab(jail({}, 'stopped'))
    expect(t('r-validate').attributes('disabled')).toBeDefined() // nothing to validate yet
    await type(recipe)
    await t('r-validate').trigger('click')
    await settle()
    expect(t('r-valid').text()).toContain('The recipe is valid: 2 steps')
    expect(seen.validate).toHaveLength(1)
    expect((seen.validate[0] as { recipe: { steps: unknown[] } }).recipe.steps).toHaveLength(2)
  })

  it('shows what the endpoint refuses, naming the step and the field', async () => {
    mountTab()
    await type('steps:\n  - file: {path: /usr/local/etc/a, content: x}\n    run: [echo]\n')
    await t('r-validate').trigger('click')
    await settle()
    expect(t('error-panel').text()).toContain('steps[0]: a step is exactly one of')
    expect(w!.find('[data-test="r-valid"]').exists()).toBe(false)
  })

  it('says what is wrong with the recipe before it asks the server', async () => {
    mountTab()
    await type('steps:\n  - run: [false]\n')
    await t('r-validate').trigger('click')
    await settle()
    expect(t('r-problem').text()).toContain('steps[0].run[0]')
    expect(seen.validate).toHaveLength(0)
  })

  it('dry run changes nothing, apply changes, the second apply says unchanged', async () => {
    mountTab()
    await type(recipe)
    await t('r-dry').trigger('click')
    await settle()
    expect((seen.apply[0] as { dry_run?: boolean }).dry_run).toBe(true)
    expect(t('r-summary').text()).toBe('2 of 2 steps would change.')
    expect(w!.findAll('[data-test="r-step"]').map((x) => x.text())).toEqual([expect.stringContaining('would change'), expect.stringContaining('would change')])
    await t('r-dry').trigger('click')
    await settle()
    expect(t('r-summary').text()).toBe('2 of 2 steps would change.') // a dry run did not change the jail
    await t('r-apply').trigger('click')
    await settle()
    expect((seen.apply[2] as { dry_run?: boolean }).dry_run).toBeUndefined()
    expect(t('r-summary').text()).toBe('2 of 2 steps changed.')
    await t('r-apply').trigger('click')
    await settle()
    expect(t('r-summary').text()).toBe('0 of 2 steps changed.')
    expect(w!.findAll('[data-test="r-step"]').every((x) => x.text().includes('unchanged'))).toBe(true)
  })

  it('shows the requirements, lists them as not checked in a dry run and stops an unmet one before any step', async () => {
    mountTab()
    const req = (check: string) => `requirements:\n  - check: [${check}]\n    message: sshd is not installed in the jail\nsteps:\n  - package: [curl]\n`
    await type(req('test, -x, /usr/sbin/sshd'))
    await t('r-validate').trigger('click')
    await settle()
    expect(t('r-requirement-preview').text()).toContain('sshd is not installed in the jail')
    expect((seen.validate[0] as { recipe: { requirements: unknown[] } }).recipe.requirements).toHaveLength(1)
    await t('r-dry').trigger('click')
    await settle()
    expect(t('r-requirement').text()).toContain('not checked')
    await t('r-apply').trigger('click')
    await settle()
    expect(t('r-requirement').text()).toContain('met')
    // a requirement that is not met: the error names it, and no step ran
    await type(req('"false"'))
    await t('r-apply').trigger('click')
    await settle()
    expect(t('error-panel').text()).toContain('requirement not met: sshd is not installed in the jail')
    expect(t('r-unmet').text()).toContain('No step ran')
    expect(w!.find('[data-test="r-steps"]').exists()).toBe(false)
  })

  it('shows a failed step: which one, the exit status, and that the steps before it stay', async () => {
    mountTab()
    await type('steps:\n  - package: [curl]\n  - run: ["false"]\n')
    await t('r-apply').trigger('click')
    await settle()
    expect(t('error-panel').text()).toContain('step 2 of 2 failed (run false): exit status 1')
    expect(t('error-hint').text()).toContain('Before it: package curl changed')
    expect(t('r-failed').text()).toContain('steps before it stay')
    expect(w!.find('[data-test="r-steps"]').exists()).toBe(false)
  })

  it('builds the form from vars: and renders with the values', async () => {
    mountTab()
    await type(withVars)
    expect(t('r-var-site').attributes('placeholder')).toBe('example.test')
    expect(t('r-var-port').attributes('placeholder')).toBe('22')
    expect(t('r-var-pw').attributes('type')).toBe('password')
    expect(w!.get('[for="r-var-pw"]').text()).toContain('secret')
    // a required variable without a value is named before anything is sent
    await t('r-validate').trigger('click')
    await settle()
    expect(t('r-problem').text()).toContain('pw is required')
    expect(seen.validate).toHaveLength(0)
    await t('r-var-site').setValue('shop.test')
    await t('r-var-pw').setValue(SECRET)
    await t('r-validate').trigger('click')
    await settle()
    expect(t('r-valid').text()).toContain('2 steps')
    const sent = seen.validate[0] as { recipe: { steps: Array<{ sysrc?: Record<string, string>; file?: { content: string } }> }; sensitive?: boolean }
    expect(sent.sensitive).toBe(true)
    expect(sent.recipe.steps[0].sysrc).toEqual({ sshd_port: '22' })
    expect(sent.recipe.steps[1].file?.content).toBe(`site=shop.test\npassword=${SECRET}\n`)
    expect(JSON.stringify(sent)).not.toContain('vars')
  })

  it('keeps a secret in memory only: sensitive on the request, cleared after Apply, never shown or stored', async () => {
    mountTab()
    await type(withVars)
    await t('r-var-pw').setValue(SECRET)
    await t('r-dry').trigger('click')
    await settle()
    expect((t('r-var-pw').element as HTMLInputElement).value).toBe(SECRET) // a dry run keeps it for the apply
    expect((seen.apply[0] as { sensitive?: boolean }).sensitive).toBe(true)
    await t('r-apply').trigger('click')
    await settle()
    expect((seen.apply[1] as { sensitive?: boolean }).sensitive).toBe(true)
    expect((t('r-var-pw').element as HTMLInputElement).value).toBe('')
    expect(w!.text()).not.toContain(SECRET)
    expect(w!.html()).not.toContain(SECRET)
    expect(JSON.stringify(localStorage)).not.toContain(SECRET)
    expect(Object.keys(localStorage).join()).not.toContain('recipe')
    // and a failed apply clears it too
    await type(withVars.replace('steps:\n', 'steps:\n  - run: ["false"]\n'))
    await t('r-var-pw').setValue(SECRET)
    await t('r-apply').trigger('click')
    await settle()
    expect(t('error-panel').text()).toContain('the output is withheld')
    expect(t('error-panel').text()).not.toContain(SECRET)
    expect((t('r-var-pw').element as HTMLInputElement).value).toBe('')
  })

  it('does not log a value', async () => {
    const spies = (['log', 'info', 'warn', 'error', 'debug'] as const).map((k) => vi.spyOn(console, k).mockImplementation(() => undefined))
    mountTab()
    await type(withVars)
    await t('r-var-pw').setValue(SECRET)
    await t('r-apply').trigger('click')
    await settle()
    for (const sp of spies) expect(JSON.stringify(sp.mock.calls)).not.toContain(SECRET)
    spies.forEach((sp) => sp.mockRestore())
  })

  it('does not offer a dry run or an apply for a stopped jail, a Linux jail, or a role that may not', async () => {
    mountTab(jail({}, 'stopped'))
    await type(recipe)
    expect(t('r-dry').attributes('disabled')).toBeDefined()
    expect(t('r-apply').attributes('disabled')).toBeDefined()
    expect(t('recipes-why').text()).toContain('not running')
    expect(t('r-validate').attributes('disabled')).toBeUndefined()
    w!.unmount()
    mountTab(jail({ os: 'linux' }))
    await type(recipe)
    expect(t('r-apply').attributes('disabled')).toBeDefined()
    expect(t('recipes-why').text()).toContain('Linux jail')
    w!.unmount()
    session.role = 'viewer'
    mountTab()
    await type(recipe)
    expect(t('r-apply').attributes('disabled')).toBeDefined()
    expect(t('recipes-why').text()).toContain('operator role')
  })

  it('warns that sync may revert a change on a jail it manages', async () => {
    mountTab(jail({}, 'running', { 'sync.jailops.net/hash': 'abc' }))
    expect(t('recipes-sync').text()).toContain('next sync may revert')
    w!.unmount()
    mountTab()
    expect(w!.find('[data-test="recipes-sync"]').exists()).toBe(false)
  })

  it('announces results in a live region and labels every field', async () => {
    mountTab()
    await type(withVars)
    expect(t('r-results').attributes('aria-live')).toBe('polite')
    expect(t('r-status').attributes('aria-live')).toBe('polite')
    for (const input of w!.findAll('input, textarea')) {
      const id = input.attributes('id')
      expect(id, input.html()).toBeTruthy()
      expect(w!.find(`label[for="${id}"]`).exists(), id).toBe(true)
    }
  })

  it('imports a directory: the recipe, the files beside it and a vars file', async () => {
    mountTab()
    const file = (path: string, body: string) => Object.assign(new File([body], path.split('/').pop()!), { webkitRelativePath: path })
    const list = [
      file('site/recipe.yaml', 'vars:\n  site: {required: true}\nsteps:\n  - file:\n      path: /usr/local/etc/a.conf\n      source: files/a.conf\n      template: true\n'),
      file('site/files/a.conf', 'name=${site}\n'),
      file('site/site.vars', 'site=from-file # comment\n'),
    ]
    const input = t('r-dir')
    Object.defineProperty(input.element, 'files', { value: list, configurable: true })
    await input.trigger('change')
    await settle()
    expect(t('r-note').text()).toBe('recipe.yaml is loaded with 1 file beside it.')
    expect((t('r-text').element as HTMLTextAreaElement).value).toContain('source: files/a.conf')
    expect((t('r-var-site').element as HTMLInputElement).value).toBe('from-file')
    await t('r-validate').trigger('click')
    await settle()
    const sent = seen.validate[0] as { recipe: { steps: Array<{ file?: { content: string; source?: string } }> } }
    expect(sent.recipe.steps[0].file).toEqual({ path: '/usr/local/etc/a.conf', content: 'name=from-file\n' })
  })

  it('imports a single recipe file and says when a choice holds no recipe', async () => {
    mountTab()
    const input = t('r-files')
    Object.defineProperty(input.element, 'files', { value: [new File(['notes'], 'notes.txt')], configurable: true })
    await input.trigger('change')
    await settle()
    expect(t('r-problem').text()).toContain('No recipe.yaml')
    Object.defineProperty(input.element, 'files', { value: [new File([recipe], 'web.yaml')], configurable: true })
    await input.trigger('change')
    await settle()
    expect((t('r-text').element as HTMLTextAreaElement).value).toBe(recipe)
  })
})
