import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import JailBasePackages from './JailBasePackages.vue'
import { Client, getClient, setClient, type RawRequest, type Transport } from '@jailops/shared/api/client'
import { POLL_MS } from '@jailops/shared/composables/ops'
import { session } from '@jailops/shared/composables/session'
import { demoTransport } from '@/demo/transport'
import { jails, server } from '@/demo/data'
import { api } from '@/api/endpoints'
import { withProject } from '@/lib/projectScope'
import type { Jail } from '@jailops/shared/api/types'

function recorded(): RawRequest[] {
  const inner = demoTransport({ speed: 1000 })
  const sent: RawRequest[] = []
  const t: Transport = { request: (r) => { sent.push(r); return inner.request(r) }, stream: (p, h) => inner.stream(p, h) }
  setClient(new Client(t))
  return sent
}
async function settle(): Promise<void> {
  for (let i = 0; i < 20; i++) {
    await flushPromises()
    await new Promise((r) => setTimeout(r, 2))
  }
}
const stale = (power: 'running' | 'stopped' = 'stopped'): Jail => {
  const j = structuredClone(jails().find((x) => x.status.power === 'stopped' && x.spec.os !== 'linux')!)
  j.status.power = power
  j.status.conditions = [{ type: 'BasePackages', status: 'False', reason: 'OutOfStep', message: 'the base package records could not be brought in step with the image: no space left; try again with `jailops jail repair-packages web`' }]
  return j
}

describe('the base package records of a jail', () => {
  const before = getClient()
  const poll = POLL_MS.value
  beforeEach(() => { POLL_MS.value = 1; session.info = server() })
  afterEach(() => { setClient(before); POLL_MS.value = poll; api.setProjectFilter(null) })

  it('shows nothing while the records are in step', () => {
    const j = stale()
    j.status.conditions = [{ type: 'Reconciled', status: 'True' }]
    expect(mount(JailBasePackages, { props: { jail: j } }).find('[data-test="base-packages"]').exists()).toBe(false)
  })

  it('asks for a restart while the jail runs', () => {
    const w = mount(JailBasePackages, { props: { jail: stale('running') } })
    expect(w.find('[data-test="repair-packages"]').exists()).toBe(false)
    expect(w.get('[data-test="repair-packages-restart"]').text()).toContain('Restart the jail')
  })

  it('names the failure and repairs it with one request', async () => {
    const sent = recorded()
    const j = stale()
    api.setProjectFilter((p) => withProject(p, j.metadata.project ?? 'main'))
    const w = mount(JailBasePackages, { props: { jail: j } })
    expect(w.get('[data-test="base-packages"]').text()).toContain('no space left')
    await w.get('[data-test="repair-packages"]').trigger('click')
    await settle()
    const post = sent.filter((r) => r.method === 'POST' && r.path.includes(`/jails/${j.metadata.name}/repair-packages`))
    expect(post).toHaveLength(1)
    await vi.waitFor(() => expect(w.emitted('done')).toHaveLength(1), { timeout: 3000 })
  })
})
