import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const create = vi.fn()
vi.mock('@/api/endpoints', () => ({ api: { jails: { create: (...a: unknown[]) => create(...a) } } }))

import JailCreateForm from './JailCreateForm.vue'

const image = { metadata: { name: 'freebsd-15' }, spec: { aliases: ['stable'] }, status: { generations: [{ number: 4 }] } }
const storage = { metadata: { name: 'default' } }

describe('JailCreateForm', () => {
  it('sends an encrypted volume with its passphrase once and forgets the passphrase', async () => {
    create.mockResolvedValue({ kind: 'sync', data: {} })
    const w = mount(JailCreateForm, { props: { open: false, project: undefined, images: [image as never], storage: [storage as never], networks: [] }, global: { stubs: { Teleport: true } } })
    await w.setProps({ open: true })
    await w.get('#jc-name').setValue('vault2')
    await w.get('#jc-vol').setValue('secrets')
    await w.get('#jc-mount').setValue('/var/db/secrets')
    await w.get('[data-test="jc-encrypted"]').setValue(true)
    const go = () => w.get('[data-test="jail-create-go"]')
    await w.get('#jc-pass').setValue('short')
    await w.get('#jc-pass').trigger('blur')
    expect(w.get('[data-test="jc-vol-error"]').text()).toBe('Use at least 12 characters.')
    expect(go().attributes('aria-disabled')).toBe('true')
    expect(w.get('[data-test="jail-create-why"]').text()).toContain('at least 12 characters')
    await w.get('#jc-pass').setValue('a-long-volume-passphrase')
    await w.get('#jc-pass2').setValue('a-long-volume-passphrasX')
    expect(go().attributes('aria-disabled')).toBe('true')
    await w.get('#jc-pass2').setValue('a-long-volume-passphrase')
    expect(go().attributes('aria-disabled')).toBe('false')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(create).toHaveBeenCalledWith({
      metadata: { name: 'vault2' },
      spec: { image: 'freebsd-15@stable', storage: 'default', volumes: [{ name: 'secrets', mount: '/var/db/secrets', encrypted: true }] },
      volume_passphrase: 'a-long-volume-passphrase',
    })
    expect(w.html()).not.toContain('a-long-volume-passphrase')
    expect(JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage })).not.toContain('a-long-volume-passphrase')
  })

  it('follows the server name rule, names what is missing and focuses the first bad field on submit', async () => {
    create.mockReset()
    const w = mount(JailCreateForm, { props: { open: false, project: undefined, images: [image as never], storage: [storage as never], networks: [] }, global: { stubs: { Teleport: true } }, attachTo: document.body })
    await w.setProps({ open: true })
    expect(w.text()).toContain('at most 63 characters')
    expect(w.get('[data-test="jail-create-why"]').text()).toContain('Name')
    for (const bad of ['web.1', '1web', 'web_1']) {
      await w.get('#jc-name').setValue(bad)
      await w.get('form').trigger('submit')
      await flushPromises()
      expect(create).not.toHaveBeenCalled()
      expect(document.activeElement?.id).toBe('jc-name')
      expect(w.find('#jc-name-err').exists()).toBe(true)
    }
    await w.get('#jc-name').setValue('web-1')
    await w.get('#jc-mount').setValue('relative/path')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(document.activeElement?.id).toBe('jc-vol')
    expect(create).not.toHaveBeenCalled()
    w.unmount()
  })
})
