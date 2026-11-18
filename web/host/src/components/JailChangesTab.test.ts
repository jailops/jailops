import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import JailChangesTab from './JailChangesTab.vue'
import { Client, getClient, setClient } from '@jailops/shared/api/client'
import { session } from '@jailops/shared/composables/session'
import { demoTransport } from '@/demo/transport'
import { server } from '@/demo/data'
import { projectState } from '@/composables/project'
import { withProject } from '@/lib/projectScope'
import { api } from '@/api/endpoints'

describe('JailChangesTab (demo server)', () => {
  const before = getClient()
  let w: VueWrapper | null = null
  beforeEach(async () => {
    localStorage.clear()
    setClient(new Client(demoTransport({ speed: 1000 })))
    Object.assign(session, { authed: true, info: server(), role: 'admin', global: true, user: 'alice', projects: [] })
    projectState.current = 'web'
    api.setProjectFilter((p) => withProject(p, 'web'))
    const jail = (await api.jails.list())[0]
    w = mount(JailChangesTab, { props: { jail } })
    await flushPromises()
    await flushPromises()
  })
  afterEach(() => {
    w?.unmount()
    w = null
    api.setProjectFilter(null)
    setClient(before)
  })

  it('lists each dataset with what changed since its base', () => {
    const t = w!.text()
    expect(t).toContain('since freebsd-15-1#g4')
    expect(t).toContain('since the jail was made')
    expect(w!.find('[data-kind="modified"]').text()).toContain('/boot/loader.conf')
    expect(w!.find('[data-kind="renamed"]').text()).toContain('/etc/old.conf → /etc/site.conf')
    expect(w!.find('[data-dataset="var-log"]').exists()).toBe(false)
  })

  it('says how many are not listed and lists them all on request', async () => {
    expect(w!.get('[data-test="diff-more"]').text()).toContain('2 more not listed')
    await w!.get('[data-test="diff-all"]').trigger('click')
    await flushPromises()
    expect(w!.find('[data-test="diff-more"]').exists()).toBe(false)
    expect(w!.text()).toContain('/obj/file6.o')
  })

  it('adds the packages when asked', async () => {
    expect(w!.find('[data-test="diff-package-list"]').exists()).toBe(false)
    await w!.get('[data-test="diff-packages"]').setValue(true)
    await flushPromises()
    expect(w!.get('[data-test="diff-package-list"]').text()).toContain('nginx-1.30.6,3')
  })
})
