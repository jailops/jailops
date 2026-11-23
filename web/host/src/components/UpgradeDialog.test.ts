import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import UpgradeDialog from './UpgradeDialog.vue'
import { Client, getClient, setClient, type RawRequest, type Transport } from '@jailops/shared/api/client'
import { POLL_MS } from '@jailops/shared/composables/ops'
import { session } from '@jailops/shared/composables/session'
import { demoTransport } from '@/demo/transport'
import { jails, server } from '@/demo/data'
import { api } from '@/api/endpoints'
import { withProject } from '@/lib/projectScope'

describe('the in-place upgrade of a jail', () => {
  const before = getClient()
  const poll = POLL_MS.value
  beforeEach(() => { POLL_MS.value = 1; session.info = server() })
  afterEach(() => { setClient(before); POLL_MS.value = poll; api.setProjectFilter(null) })

  it('previews the package changes, then upgrades with the same body and no image or generation', async () => {
    const inner = demoTransport({ speed: 1000 })
    const sent: RawRequest[] = []
    const t: Transport = { request: (r) => { sent.push(r); return inner.request(r) }, stream: (p, h) => inner.stream(p, h) }
    setClient(new Client(t))
    const j = jails().find((x) => x.status.power === 'running' && x.spec.os !== 'linux')!
    api.setProjectFilter((p) => withProject(p, j.metadata.project ?? 'main'))
    const w = mount(UpgradeDialog, { props: { open: false, jail: j }, global: { stubs: { Teleport: true } } })
    await w.setProps({ open: true })
    expect(w.get('[data-test="upgrade-go"]').attributes('disabled')).toBeDefined()
    await w.get('[data-test="preview"]').trigger('click')
    await vi.waitFor(() => expect(w.find('[data-test="preview-base"]').exists()).toBe(true), { timeout: 3000 })
    expect(w.get('[data-test="preview-base"]').text()).toContain('FreeBSD-runtime')
    expect(w.get('[data-test="upgrade-go"]').attributes('disabled')).toBeUndefined()
    await w.get('[data-test="upgrade-go"]').trigger('click')
    await vi.waitFor(() => expect(w.emitted('done')).toHaveLength(1), { timeout: 5000 })
    await flushPromises()
    const posts = sent.filter((r) => r.method === 'POST' && r.path.includes(`/jails/${j.metadata.name}/upgrade`))
    expect(posts).toHaveLength(2)
    expect(posts[0].body).toEqual({ dry_run: true })
    expect(posts[1].body).toEqual({})
  })
})
