import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'

const expose = vi.fn()
vi.mock('@/api/endpoints', async (orig) => {
  const real = await orig<typeof import('@/api/endpoints')>()
  return { api: { ...real.api, reach: { ...real.api.reach, expose: (...a: unknown[]) => expose(...a) } } }
})

import ExposeDialog from './ExposeDialog.vue'
import JailReachTab from './JailReachTab.vue'
import { Client, getClient, setClient } from '@jailops/shared/api/client'
import { session } from '@jailops/shared/composables/session'
import { demoTransport } from '@/demo/transport'
import { server } from '@/demo/data'
import { projectState } from '@/composables/project'
import { withProject } from '@/lib/projectScope'
import { api } from '@/api/endpoints'

const dual = { metadata: { name: 'web' }, spec: { network: [] }, status: { addresses: ['10.20.0.10', '2001:db8:10::10'] } }
const v4only = { metadata: { name: 'web' }, spec: { network: [] }, status: { addresses: ['10.20.0.10'] } }
const stub = { global: { stubs: { Teleport: true } } }
const result = { jail: 'web', changed: true, entries: [], families: [
  { family: '6', address: '2001:db8:10::10', network: 'lan', via: 'uplink', ports: '80, 443' },
  { family: '4', address: '10.20.0.10', network: 'lan', via: 'forward', ports: '80, 443', forwards: ['80->80', '443->443'] },
] }

describe('ExposeDialog', () => {
  beforeEach(() => expose.mockReset())

  it('refuses bad ports and an empty source list before it asks the server', async () => {
    const w = mount(ExposeDialog, { props: { open: true, jail: dual as never }, ...stub })
    await flushPromises()
    await w.get('[data-test="expose-ports"]').setValue('80,99999')
    await w.get('[data-test="expose-limited"]').setValue(true)
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(expose).not.toHaveBeenCalled()
    expect(w.text()).toContain('Name the ports')
    expect(w.text()).toContain('Name at least one address, prefix or address set.')
  })

  it('sends the request of the command and shows what each family reaches', async () => {
    expose.mockResolvedValue({ kind: 'sync', data: result })
    const w = mount(ExposeDialog, { props: { open: true, jail: dual as never }, ...stub })
    await flushPromises()
    await w.get('[data-test="expose-ports"]').setValue('80, 443')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(expose).toHaveBeenCalledWith('web', { ports: ['80', '443'] })
    const r = w.get('[data-test="expose-result"]').text()
    expect(r).toContain('IPv6 2001:db8:10::10: reached directly, ports 80, 443')
    expect(r).toContain('IPv4 10.20.0.10 (behind NAT): the host forwards 80->80, 443->443')
    expect(w.emitted('done')).toBeTruthy()
  })

  it('previews with dry_run, limits the source and keeps IPv6 out of an IPv4-only jail', async () => {
    expose.mockResolvedValue({ kind: 'sync', data: { ...result, families: [result.families[1]] } })
    const w = mount(ExposeDialog, { props: { open: true, jail: v4only as never }, ...stub })
    await flushPromises()
    expect((w.get('[data-test="expose-v6"]').element as HTMLInputElement).disabled).toBe(true)
    expect(w.text()).toContain('the jail has no IPv6 address')
    await w.get('[data-test="expose-ports"]').setValue('8080')
    await w.get('[data-test="expose-hostport"]').setValue('80')
    await w.get('[data-test="expose-limited"]').setValue(true)
    await w.get('[data-test="expose-from"]').setValue('set:office, 192.0.2.0/24')
    await w.get('[data-test="expose-preview"]').trigger('click')
    await flushPromises()
    expect(expose).toHaveBeenCalledWith('web', { ports: ['8080'], from: ['set:office', '192.0.2.0/24'], families: ['4'], host_port: 80, dry_run: true })
    expect(w.emitted('done')).toBeFalsy()
  })
})

describe('the Firewall tab exposes and unexposes a jail (demo server)', () => {
  const before = getClient()
  let w: VueWrapper | null = null
  beforeEach(() => {
    localStorage.clear()
    setClient(new Client(demoTransport({ speed: 1000 })))
    Object.assign(session, { authed: true, info: server(), role: 'admin', global: true, user: 'alice', projects: [] })
    projectState.current = 'web'
    api.setProjectFilter((p) => withProject(p, 'web'))
    expose.mockImplementation((...a: unknown[]) => getClient().mutate('POST', `/jails/${a[0]}/expose`, a[1]))
  })
  afterEach(() => {
    w?.unmount()
    w = null
    api.setProjectFilter(null)
    setClient(before)
  })

  it('adds the ports to the list and to the rows of the jail, and takes them away again', async () => {
    const jail = (await api.jails.list())[0]
    w = mount(JailReachTab, { props: { jail }, ...stub })
    await flushPromises()
    await flushPromises()
    expect(w.find('[data-test="expose-none"]').exists()).toBe(true)
    await w.get('[data-test="expose-open"]').trigger('click')
    await w.get('[data-test="expose-ports"]').setValue('80,443')
    await w.get('form#expose-form').trigger('submit')
    await flushPromises()
    await flushPromises()
    expect(w.find('[data-expose="80,443/tcp"]').exists()).toBe(true)
    expect(w.get('[data-test="reach-rows"]').text()).toContain('expose')
    await w.get('[data-test="unexpose-80,443/tcp"]').trigger('click')
    await flushPromises()
    expect(w.get('[data-test="unexpose-dialog"]').text()).toContain('no longer reaches 80,443/tcp')
    await w.get('[data-test="confirm-dialog"]').trigger('submit')
    await flushPromises()
    await flushPromises()
    expect(w.find('[data-expose="80,443/tcp"]').exists()).toBe(false)
    expect(w.find('[data-test="expose-none"]').exists()).toBe(true)
  })
})
