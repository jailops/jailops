import { afterEach, describe, expect, it } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import WireGuardPeerDialog from './WireGuardPeerDialog.vue'
import { Client, getClient, setClient } from '@jailops/shared/api/client'
import { demoTransport } from '@/demo/transport'

describe('WireGuardPeerDialog', () => {
  const before = getClient()
  afterEach(() => setClient(before))
  it('shows the token once, keeps it out of storage and forgets it on close', async () => {
    setClient(new Client(demoTransport({ speed: 1000 })))
    localStorage.clear()
    sessionStorage.clear()
    const w = mount(WireGuardPeerDialog, { props: { open: true, server: 'office' }, global: { stubs: { Teleport: true } } })
    await w.vm.$nextTick()
    await w.get('#wgp-name').setValue('tablet')
    await w.get('form').trigger('submit')
    await flushPromises()
    const field = w.get('input[aria-label="Join token"]')
    const token = (field.element as HTMLInputElement).value
    expect(token).toMatch(/^jwg1\./)
    expect(w.get('[data-test="wg-token-warning"]').text()).toContain('shown once')
    const stored = JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage })
    expect(stored).not.toContain(token)
    await w.get('[data-test="wg-token-done"]').trigger('click')
    expect(w.emitted('close')).toBeTruthy()
    expect(w.html()).not.toContain(token)
  })
})
