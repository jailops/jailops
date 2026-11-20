import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import NotesPanel from './NotesPanel.vue'
import { Client, getClient, setClient, type RawRequest, type Transport } from '@jailops/shared/api/client'
import { session } from '@jailops/shared/composables/session'
import { demoTransport } from '@/demo/transport'
import { server } from '@/demo/data'
import { projectState } from '@/composables/project'
import { withProject } from '@/lib/projectScope'
import { api } from '@/api/endpoints'
import type { Jail } from '@jailops/shared/api/types'

async function until(ok: () => boolean): Promise<void> {
  for (let i = 0; i < 200 && !ok(); i++) await flushPromises()
  expect(ok()).toBe(true)
}

describe('the notes panel', () => {
  const before = getClient()
  let sent: RawRequest[] = []
  let w: VueWrapper | null = null
  const patches = () => sent.filter((r) => r.method === 'PATCH')

  beforeEach(() => {
    const inner = demoTransport({ speed: 1000 })
    sent = []
    setClient(new Client({ request: (r) => { sent.push(r); return inner.request(r) }, stream: (p, hd) => inner.stream(p, hd) } as Transport))
    Object.assign(session, { authed: true, info: server(), role: 'admin', global: true, user: 'alice', projects: [] })
    projectState.current = 'web'
    api.setProjectFilter((p) => withProject(p, 'web'))
  })
  afterEach(() => {
    w?.unmount()
    w = null
    api.setProjectFilter(null)
    setClient(before)
  })

  async function open(generation?: number): Promise<{ jail: Jail; w: VueWrapper }> {
    const jail = await api.jails.get('web')
    w = mount(NotesPanel, { props: { kind: 'jail', name: 'web', notes: jail.spec.notes, generation: generation ?? jail.metadata.generation, editable: true, empty: 'Add notes' }, attachTo: document.body })
    return { jail, w }
  }

  it('shows the notes, saves an edit with Ctrl+Enter under If-Match and says so', async () => {
    const { jail, w } = await open()
    expect(w.find('[data-test="notes-view"] h3').text()).toBe('Web front end')
    await w.find('[data-test="notes-edit"]').trigger('click')
    const area = w.find('textarea')
    expect(document.activeElement).toBe(area.element)
    expect((area.element as HTMLTextAreaElement).value).toBe(jail.spec.notes)
    await area.setValue('# Web\n\nCall ops.\r\n\n')
    await area.trigger('keydown', { key: 'Enter', ctrlKey: true })
    await until(() => !w.find('textarea').exists())
    const p = patches()
    expect(p).toHaveLength(1)
    expect(p[0].body).toEqual({ spec: { notes: '# Web\n\nCall ops.' } })
    expect(p[0].headers?.['If-Match']).toBe(`"${jail.metadata.generation}"`)
    expect(w.find('[data-test="notes-view"]').text()).toContain('Call ops.')
    expect(w.find('[data-test="notes-status"]').text()).toBe('Notes saved')
    expect(document.activeElement).toBe(w.find('[data-test="notes-edit"]').element)
    expect(w.emitted('saved')).toHaveLength(1)
  })

  it('cancels with Escape and sends nothing for an unchanged text', async () => {
    const { w } = await open()
    await w.find('[data-test="notes-edit"]').trigger('click')
    await w.find('textarea').setValue('something else')
    await w.find('textarea').trigger('keydown', { key: 'Escape' })
    expect(w.find('textarea').exists()).toBe(false)
    expect(w.find('[data-test="notes-view"]').text()).toContain('Web front end')
    await w.find('[data-test="notes-edit"]').trigger('click')
    await w.find('[data-test="notes-save"]').trigger('submit')
    await flushPromises()
    expect(w.find('textarea').exists()).toBe(false)
    expect(patches()).toHaveLength(0)
  })

  it('keeps the typed text when someone changed the object meanwhile', async () => {
    const { w } = await open(1)
    await w.find('[data-test="notes-edit"]').trigger('click')
    await w.find('textarea').setValue('mine')
    await w.find('form').trigger('submit')
    await until(() => w.find('[data-test="error-panel"]').exists())
    expect(w.find('textarea').exists()).toBe(true)
    expect((w.find('textarea').element as HTMLTextAreaElement).value).toBe('mine')
    expect(w.find('[data-test="error-code"]').text()).toBe('precondition_failed')
    expect(patches()[0].headers?.['If-Match']).toBe('"1"')
  })

  it('refuses control characters and too long a text before sending', async () => {
    const { w } = await open()
    await w.find('[data-test="notes-edit"]').trigger('click')
    await w.find('textarea').setValue('bell \u0007')
    expect(w.find('.field-error').text()).toContain('control characters')
    await w.find('form').trigger('submit')
    await w.find('textarea').setValue('x'.repeat(64 * 1024 + 1))
    expect(w.find('.field-error').text()).toContain('at most')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(patches()).toHaveLength(0)
  })

  it('invites writing when empty and offers no editor to a reader', async () => {
    w = mount(NotesPanel, { props: { kind: 'jail', name: 'db', notes: '', editable: true, empty: 'Add notes: what this jail does' } })
    expect(w.find('[data-test="notes-empty"]').text()).toBe('Add notes: what this jail does')
    w.unmount()
    w = mount(NotesPanel, { props: { kind: 'jail', name: 'db', notes: 'x', editable: false, empty: '' } })
    expect(w.find('[data-test="notes-edit"]').exists()).toBe(false)
  })
})
