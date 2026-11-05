import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const create = vi.fn()
vi.mock('@/api/endpoints', () => ({ api: { jails: { create: (...a: unknown[]) => create(...a) } } }))

import JailCreateForm from './JailCreateForm.vue'

const image = { metadata: { name: 'freebsd-15-1' }, spec: { os: 'freebsd', release: '15.1-RELEASE' }, status: { generations: [{ number: 4 }] } }
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
      spec: { image: 'freebsd-15-1', storage: 'default', volumes: [{ name: 'secrets', mount: '/var/db/secrets', encrypted: true }] },
      volume_passphrase: 'a-long-volume-passphrase',
    })
    expect(w.html()).not.toContain('a-long-volume-passphrase')
    expect(JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage })).not.toContain('a-long-volume-passphrase')
  })

  it('follows the server name rule, names what is missing and focuses the first bad field on submit', async () => {
    create.mockReset()
    const w = mount(JailCreateForm, { props: { open: false, project: undefined, images: [image as never], storage: [storage as never], networks: [] }, global: { stubs: { Teleport: true } }, attachTo: document.body })
    await w.setProps({ open: true })
    expect(w.text()).toContain('at most 32 characters')
    expect(w.get('[data-test="jail-create-why"]').text()).toContain('Name')
    for (const bad of ['web.1', '1web', 'web_1', 'a'.repeat(33)]) {
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

  it('leaves the shell to the host unless one is chosen, and offers none for a Linux image', async () => {
    create.mockReset()
    create.mockResolvedValue({ kind: 'sync', data: {} })
    const alpine = { metadata: { name: 'alpine-3-22' }, spec: { os: 'linux', release: '3.22.1' }, status: { generations: [{ number: 1 }] } }
    const w = mount(JailCreateForm, { props: { open: false, project: undefined, images: [image as never, alpine as never], storage: [storage as never], networks: [] }, global: { stubs: { Teleport: true } } })
    await w.setProps({ open: true })
    const opts = w.get('#jc-shell').findAll('option').map((o) => [o.attributes('value'), o.text()])
    expect(opts).toEqual([['', 'Host default (bash with a coloured prompt)'], ['bash', 'bash, with a coloured prompt'], ['sh', "sh, FreeBSD's own shell"]])
    expect(w.get('#jc-shell').attributes('aria-describedby')).toBe('jc-shell-hint')
    await w.get('#jc-name').setValue('web')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(create.mock.calls[0][0].spec).not.toHaveProperty('shell')
    await w.setProps({ open: false })
    await w.setProps({ open: true })
    await w.get('#jc-name').setValue('plain')
    await w.get('#jc-shell').setValue('sh')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(create.mock.calls[1][0].spec.shell).toBe('sh')
    await w.setProps({ open: false })
    await w.setProps({ open: true })
    await w.get('#jc-image').setValue('alpine-3-22')
    expect(w.find('#jc-shell').exists()).toBe(false)
  })

  it('says in place that there is no image and offers to create the base image', async () => {
    const w = mount(JailCreateForm, { props: { open: false, project: undefined, images: [], storage: [storage as never], networks: [] }, global: { stubs: { Teleport: true } } })
    await w.setProps({ open: true })
    expect(w.get('[data-test="jail-missing"]').text()).toContain('There is no image to start from')
    expect(w.find('[data-test="jail-create-go"]').exists()).toBe(false)
    expect(w.get('[data-test="jail-make-image"]').text()).toBe('Create the base image')
    // the image appears (the list reloads after the build): the form takes it
    await w.setProps({ images: [image as never] })
    expect(w.find('[data-test="jail-missing"]').exists()).toBe(false)
    expect((w.get('#jc-image').element as HTMLSelectElement).value).toBe('freebsd-15-1')
    w.unmount()
  })
})
