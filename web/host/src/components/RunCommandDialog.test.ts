import { afterEach, describe, expect, it } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import RunCommandDialog from './RunCommandDialog.vue'
import JailFilesTab from './JailFilesTab.vue'
import { Client, getClient, setClient } from '@jailops/shared/api/client'
import { demoTransport } from '@/demo/transport'
import { runMany } from '@/composables/runMany'
import type { Jail } from '@jailops/shared/api/types'

const jail = (name: string, power: string) => ({ metadata: { name, project: 'web' }, spec: {}, status: { power } }) as unknown as Jail

async function settle(until: () => boolean): Promise<void> {
  const deadline = Date.now() + 20000
  while (!until() && Date.now() < deadline) {
    await flushPromises()
    await new Promise((r) => setTimeout(r, 5))
  }
}

describe('run a command on many jails', () => {
  const before = getClient()
  afterEach(() => setClient(before))
  const demo = () => {
    const c = new Client(demoTransport({ speed: 1000 }))
    c.setRequestFilter((p) => p + (p.includes('?') ? '&' : '?') + 'project=web')
    setClient(c)
  }

  it('runs the line in every jail and keeps the output and status of each apart', async () => {
    demo()
    const seen: number[] = []
    const r = await runMany(['web', 'db', 'proxy'], 'hostname', (x) => seen.push(x.filter((y) => y.exit !== undefined).length), 2)
    expect(r.map((x) => x.jail)).toEqual(['web', 'db', 'proxy'])
    for (const x of r) {
      expect(x.exit).toBe(0)
      expect(x.output).toBe(`${x.jail}.example.net\n`)
    }
  })

  it('shows a status per jail, names the stopped jails and a summary', async () => {
    demo()
    const w = mount(RunCommandDialog, { props: { open: true, jails: [jail('web', 'running'), jail('db', 'running'), jail('old', 'stopped')] }, global: { stubs: { Teleport: true } } })
    await w.vm.$nextTick()
    expect(w.get('[data-test="rc-skipped"]').text()).toContain('old')
    expect(w.get('[data-test="rc-run"]').attributes('disabled')).toBeDefined()
    await w.get('[data-test="rc-command"]').setValue('false')
    await w.get('[data-test="rc-run"]').trigger('submit')
    await w.get('[data-test="run-cmd"]').trigger('submit')
    await settle(() => w.find('[data-test="rc-summary"]').exists())
    const rows = w.findAll('[data-test="rc-result"]')
    expect(rows).toHaveLength(2)
    expect(rows.map((x) => x.get('[data-test="rc-status"]').text())).toEqual(['exit 1', 'exit 1'])
    expect(w.get('[data-test="rc-summary"]').text()).toBe('0 succeeded, 2 failed, 1 skipped')
  })
})

describe('files of a jail', () => {
  const before = getClient()
  afterEach(() => setClient(before))

  it('sends a file with its length and reads it back', async () => {
    const c = new Client(demoTransport({ speed: 1000 }))
    c.setRequestFilter((p) => p + (p.includes('?') ? '&' : '?') + 'project=web')
    setClient(c)
    const { api } = await import('@/api/endpoints')
    const st = await api.files.stat('web', '/etc/rc.conf')
    expect(st.type).toBe('file')
    expect((await api.files.stat('web', '/tmp/new.txt')).type).toBe('none')
    const res = await api.files.upload('web', '/tmp/new.txt', new Blob(['hello']))
    expect(res.bytes).toBe(5)
    expect(await (await api.files.download('web', '/tmp/new.txt')).text()).toBe('hello')
    await expect(api.files.upload('db', '/tmp/x', new Blob(['x']))).resolves.toBeTruthy()
  })

  it('does not offer a transfer to a stopped jail', async () => {
    const w = mount(JailFilesTab, { props: { jail: jail('old', 'stopped') } })
    expect(w.text()).toContain('not running')
    expect(w.get('[data-test="f-download"]').attributes('disabled')).toBeDefined()
  })
})
