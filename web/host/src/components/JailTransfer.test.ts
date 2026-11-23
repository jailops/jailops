import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DOMWrapper, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import JailExportDialog from './JailExportDialog.vue'
import JailImportDialog from './JailImportDialog.vue'
import { Client, getClient, setClient, type RawRequest, type Transport } from '@jailops/shared/api/client'
import { session } from '@jailops/shared/composables/session'
import { demoTransport } from '@/demo/transport'
import { exportFile } from '@/demo/xport'
import * as d from '@/demo/data'
import { withProject } from '@/lib/projectScope'
import type { Jail } from '@jailops/shared/api/types'

function recorded(): { sent: RawRequest[]; uploads: string[]; downloads: string[] } {
  const inner = demoTransport({ speed: 1000 })
  const sent: RawRequest[] = []
  const uploads: string[] = []
  const downloads: string[] = []
  const t: Transport = {
    request: (r) => { sent.push(r); return inner.request(r) },
    stream: (p, h) => inner.stream(p, h),
    download: (p, s) => { downloads.push(p); return inner.download!(p, s) },
    upload: (p, b, ct, s) => { uploads.push(p); return inner.upload!(p, b, ct, s) },
  }
  const c = new Client(t)
  c.setRequestFilter((p) => withProject(p, 'web'))
  setClient(c)
  return { sent, uploads, downloads }
}

async function settle(until?: () => boolean): Promise<void> {
  const deadline = Date.now() + 5000
  for (let i = 0; i < 10 || (until && !until() && Date.now() < deadline); i++) {
    await flushPromises()
    await new Promise((r) => setTimeout(r, 10))
  }
}

const webJail = (): Jail => d.jails().find((j) => j.metadata.project === 'web')!

describe('jail export and import dialogs', () => {
  const before = getClient()
  const info = session.info
  let mounted: VueWrapper | null = null
  beforeEach(() => {
    session.info = d.server()
    URL.createObjectURL = vi.fn(() => 'blob:demo')
    URL.revokeObjectURL = vi.fn()
  })
  afterEach(() => {
    mounted?.unmount()
    mounted = null
    setClient(before)
    session.info = info
  })
  const body = () => new DOMWrapper(document.body)
  const text = (sel: string) => (body().find(sel).exists() ? body().get(sel).text() : '')

  it('exports a jail as a file whose framing was checked on the way', async () => {
    const r = recorded()
    const j = webJail()
    mounted = mount(JailExportDialog, { props: { open: true, jail: j.metadata.name, snapshots: [], hasVolumes: true }, attachTo: document.body })
    await settle()
    await body().get('[data-test="export-volumes"]').setValue(true)
    await body().get('[data-test="export-go"]').trigger('click')
    await settle(() => body().find('[data-test="export-done"]').exists())
    expect(text('[data-test="export-done"]')).toContain(`${j.metadata.name}.jailexport was written`)
    expect(r.downloads).toHaveLength(1)
    expect(r.downloads[0]).toContain(`/jails/${j.metadata.name}/export?with_volumes=true&zstd=true`)
    expect(r.downloads[0]).toContain('project=web')
  })

  it('says a file that ended early is not whole', async () => {
    const inner = demoTransport({ speed: 1000 })
    const cut: Transport = { ...inner, request: (q) => inner.request(q), stream: (p, h) => inner.stream(p, h), download: async (p, s) => {
      const full = await inner.download!(p, s)
      const all = new Uint8Array(await new Response(full).arrayBuffer())
      const short = all.slice(0, all.length - 10)
      return new ReadableStream<Uint8Array>({ start(c) { c.enqueue(short); c.close() } })
    } }
    const c = new Client(cut)
    c.setRequestFilter((p) => withProject(p, 'web'))
    setClient(c)
    mounted = mount(JailExportDialog, { props: { open: true, jail: webJail().metadata.name, snapshots: [] }, attachTo: document.body })
    await settle()
    await body().get('[data-test="export-go"]').trigger('click')
    await settle(() => body().find('[data-test="export-problem"]').exists())
    expect(text('[data-test="export-problem"]')).toContain('ends early')
    expect(URL.createObjectURL).not.toHaveBeenCalled()
  })

  async function choose(file: File): Promise<void> {
    const input = body().get('[data-test="import-file"]')
    Object.defineProperty(input.element, 'files', { value: [file], configurable: true })
    await input.trigger('change')
    await settle()
  }
  const fileOf = (j: Jail, name = 'x.jailexport'): File => new File([exportFile(j, new URLSearchParams())], name)

  it('shows what an import does, names the problems, and makes the jail under another name', async () => {
    const r = recorded()
    const j = webJail()
    mounted = mount(JailImportDialog, { props: { open: true, networks: d.networks() }, attachTo: document.body })
    await settle()
    expect(text('[data-test="import-why"]')).toBe('Choose a file first.')
    // a file that is not an export file
    await choose(new File(['hello, this is not an export'], 'notes.txt'))
    expect(text('[data-test="import-file-error"]')).toBe('This is not a jailops export file.')
    // the file of a jail that exists here: the name is taken
    await choose(fileOf(j))
    expect(text('[data-test="import-summary"]')).toContain(`Jail ${j.metadata.name} of project web`)
    await settle(() => body().find('[data-test="import-errors"]').exists())
    expect(text('[data-test="import-errors"]')).toContain(`A jail named ${j.metadata.name} exists`)
    expect(body().get('[data-test="import-go"]').attributes('aria-disabled')).toBe('true')
    // another name
    await body().get('[data-test="import-name"]').setValue('copy-of-web')
    await settle(() => !body().find('[data-test="import-errors"]').exists() && !body().find('[data-test="import-checking"]').exists())
    expect(body().find('[data-test="import-errors"]').exists()).toBe(false)
    expect(body().get('[data-test="import-go"]').attributes('aria-disabled')).toBe('false')
    await body().get('#im').trigger('submit')
    await settle(() => (mounted!.emitted('done') ?? []).length > 0)
    expect(mounted.emitted('done')![0]).toEqual(['copy-of-web'])
    expect(r.uploads).toHaveLength(1)
    expect(r.uploads[0]).toContain('as=copy-of-web')
    const made = (await getClient().list<Jail>('/jails')).find((x) => x.metadata.name === 'copy-of-web')
    expect(made?.metadata.uuid).toBeTruthy()
    expect(made?.metadata.uuid).not.toBe(j.metadata.uuid)
  })

  it('does not send a file that is cut off', async () => {
    const r = recorded()
    const j = webJail()
    mounted = mount(JailImportDialog, { props: { open: true, networks: d.networks() }, attachTo: document.body })
    await settle()
    const whole = exportFile(j, new URLSearchParams())
    await choose(new File([whole.slice(0, whole.length - 8)], 'cut.jailexport'))
    await body().get('[data-test="import-name"]').setValue('cut-copy')
    await settle(() => !body().find('[data-test="import-checking"]').exists())
    await body().get('#im').trigger('submit')
    await settle()
    expect(text('[data-test="import-file-error"]')).toContain('ends early')
    expect(r.uploads).toHaveLength(0)
  })
})
