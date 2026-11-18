import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import ExposedPanel from './ExposedPanel.vue'
import JailReachTab from './JailReachTab.vue'
import { Client, getClient, setClient } from '@jailops/shared/api/client'
import { session } from '@jailops/shared/composables/session'
import { demoTransport } from '@/demo/transport'
import { server } from '@/demo/data'
import { projectState } from '@/composables/project'
import { withProject } from '@/lib/projectScope'
import { api } from '@/api/endpoints'

// What the Internet reaches, as the console draws it: the rows of a jail and the host-wide list.
describe('the reach views', () => {
  const before = getClient()
  let w: VueWrapper | null = null
  beforeEach(() => {
    localStorage.clear()
    setClient(new Client(demoTransport({ speed: 1000 })))
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

  it('lists what the Internet reaches across the jails, with the rule that opens it', async () => {
    const ex = await api.reach.exposed()
    expect(ex.entries.length).toBeGreaterThan(0)
    w = mount(ExposedPanel)
    await flushPromises()
    await flushPromises()
    const rows = w.findAll('[data-test="exposed-rows"] tbody tr')
    expect(rows.length).toBe(ex.entries.length)
    expect(w.text()).toContain('policy ')
  })

  it('shows a jail its rows: what reaches it and what it reaches', async () => {
    const ex = await api.reach.exposed()
    const jail = (await api.jails.list()).find((j) => j.metadata.name === ex.entries[0].jail)!
    w = mount(JailReachTab, { props: { jail } })
    await flushPromises()
    await flushPromises()
    expect(w.find('[data-test="reach-rows"] tr[data-source="internet"]').exists()).toBe(true)
    expect(w.get('[data-test="reach-out"]').text()).toContain('Internet')
  })
})
