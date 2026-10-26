import { describe, expect, it } from 'vitest'
import { Frame, FrameDecoder, encodeFrame, resizePayload, exitStatus, MAX_FRAME } from './frames'
import { configChange, sourcesOf } from './configKeys'
import { updatePlan } from './updates'
import type { JailSpec } from '@jailops/shared/api/types'

describe('exec frame codec', () => {
  it('round-trips frames split across chunks', () => {
    const a = encodeFrame(Frame.Stdout, new TextEncoder().encode('hello'))
    const b = encodeFrame(Frame.Resize, resizePayload(24, 80))
    const all = new Uint8Array([...a, ...b])
    const d = new FrameDecoder()
    const got = [...d.push(all.slice(0, 3)), ...d.push(all.slice(3, 9)), ...d.push(all.slice(9))]
    expect(got.map((f) => f.type)).toEqual([Frame.Stdout, Frame.Resize])
    expect(new TextDecoder().decode(got[0].payload)).toBe('hello')
    expect(Array.from(got[1].payload)).toEqual([0, 24, 0, 80])
  })
  it('is big endian and decodes exit status', () => {
    const f = encodeFrame(Frame.Exit, new Uint8Array([0xff, 0xff, 0xff, 0xff]))
    expect(Array.from(f.slice(0, 5))).toEqual([6, 0, 0, 0, 4])
    expect(exitStatus(new Uint8Array([0xff, 0xff, 0xff, 0xff]))).toBe(-1)
  })
  it('refuses oversized and unknown frames', () => {
    expect(() => encodeFrame(Frame.Stdin, new Uint8Array(MAX_FRAME + 1))).toThrow()
    expect(() => new FrameDecoder().push(new Uint8Array([9, 0, 0, 0, 0]))).toThrow(/unknown/)
    expect(() => new FrameDecoder().push(new Uint8Array([1, 0, 1, 0, 1]))).toThrow(/exceeds/)
  })
})

describe('config change', () => {
  const a: JailSpec = { image: 'x', resources: { cpu: 2, memory: '2G' }, boot: { autostart: true } }
  it('builds set and unset from two specs', () => {
    const b: JailSpec = { image: 'x', resources: { cpu: 4 }, boot: { autostart: true, priority: 5 } }
    expect(configChange(a, b)).toEqual({ set: { 'limits.cpu': '4', 'boot.priority': '5' }, unset: ['limits.memory'] })
    expect(configChange(a, a)).toEqual({ set: {}, unset: [] })
  })
  it('maps the server sources', () => {
    const src = sourcesOf({ jail: { spec: a } as never, sources: { 'limits.cpu': 'jail', 'limits.memory': 'profile:web-limits' } })
    expect(src['limits.memory']).toEqual({ value: '2G', source: 'web-limits' })
    expect(src['limits.cpu'].source).toBe('jail')
  })
})

describe('update plan', () => {
  const t = { planSnapshot: 'snap', planPackages: (n: number) => `pkg ${n}`, planOS: (f: string, to: string) => `os ${f} ${to}`, planHealth: 'health', planRollback: 'rollback' }
  it('lists only what the check found, around the snapshot and health check', () => {
    expect(updatePlan({ jail: 'a', project: 'p', available: true, checked_at: '', packages: [{ name: 'x' }], os: { from: 'g1', to: 'g2' } }, t)).toEqual(['snap', 'pkg 1', 'os g1 g2', 'health', 'rollback'])
    expect(updatePlan({ jail: 'a', project: 'p', available: true, checked_at: '', packages: [{ name: 'x' }] }, t)).toEqual(['snap', 'pkg 1', 'health', 'rollback'])
  })
})
