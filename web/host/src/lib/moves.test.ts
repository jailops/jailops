import { describe, expect, it } from 'vitest'
import type { Image, Jail } from '@/types'
import { commonMoveImages, imageOf, moveImages, readPreview, releaseChange, releaseKey, releaseOlder } from './moves'

const img = (name: string, release: string, o: { os?: 'freebsd' | 'linux'; arch?: string; gens?: number } = {}): Image => ({
  metadata: { name }, spec: { os: o.os ?? 'freebsd', release, arch: o.arch ?? 'amd64', source: { type: 'pkgbase' } },
  status: { generations: Array.from({ length: o.gens ?? 1 }, (_, i) => ({ number: i + 1, name: `${name}#g${i + 1}`, manifest: 'm' })) },
}) as unknown as Image
const jail = (image: string, o: { layout?: 'thin' | 'thick'; os?: 'freebsd' | 'linux' } = {}): Pick<Jail, 'spec'> => ({ spec: { image, layout: o.layout ?? 'thin', os: o.os ?? 'freebsd' } }) as Pick<Jail, 'spec'>

describe('releases', () => {
  it('reads MAJOR.MINOR and orders them', () => {
    expect(releaseKey('15.1-RELEASE')).toEqual([15, 1])
    expect(releaseKey('3.22')).toEqual([3, 22])
    expect(releaseKey('15.snap2026')).toBeNull()
    expect(releaseOlder('15.0-RELEASE', '15.1-RELEASE')).toBe(true)
    expect(releaseOlder('15.2-RELEASE', '15.1-RELEASE')).toBe(false)
    expect(releaseOlder('15.1-RELEASE', '15.1-RELEASE')).toBe(false)
    expect(releaseOlder('14.9-RELEASE', '15.0-RELEASE')).toBe(true)
    expect(releaseOlder('weird', '15.0-RELEASE')).toBe(false) // the server decides
  })
  it('names the image of a reference', () => {
    expect(imageOf('freebsd-15-1')).toBe('freebsd-15-1')
    expect(imageOf('freebsd-15-1#g3')).toBe('freebsd-15-1')
  })
})

describe('the images a jail can move to', () => {
  const all = [img('freebsd-15-1', '15.1-RELEASE', { gens: 3 }), img('freebsd-15-2', '15.2-RELEASE'), img('old', '15.0-RELEASE'), img('fresh', '15.2-RELEASE', { gens: 0 }),
    img('arm', '15.2-RELEASE', { arch: 'arm64' }), img('alpine', '3.22', { os: 'linux' }), img('same', '15.1-RELEASE')]
  it('keeps the same OS and architecture, a release that is not older, and a generation to move to', () => {
    expect(moveImages(all, jail('freebsd-15-1#g2')).map((i) => i.metadata.name)).toEqual(['freebsd-15-2', 'same'])
  })
  it('offers nothing to a thick jail, a Linux jail or a jail of an unknown image', () => {
    expect(moveImages(all, jail('freebsd-15-1', { layout: 'thick' }))).toEqual([])
    expect(moveImages(all, jail('alpine', { os: 'linux' }))).toEqual([])
    expect(moveImages(all, jail('gone'))).toEqual([])
  })
  it('offers a group of jails the images all of them can move to', () => {
    // a jail of the older image can also move to the 15.1 image: all of them can move to freebsd-15-2 only
    expect(commonMoveImages(all, [jail('freebsd-15-1'), jail('old')]).map((i) => i.metadata.name)).toEqual(['freebsd-15-2', 'same'])
    expect(commonMoveImages(all, [jail('freebsd-15-1'), jail('freebsd-15-2')]).map((i) => i.metadata.name)).toEqual([])
    expect(commonMoveImages(all, [jail('freebsd-15-1'), jail('alpine', { os: 'linux' })])).toEqual([])
    expect(commonMoveImages(all, [])).toEqual([])
  })
})

describe('the answer of a dry run', () => {
  it('reads the real answer: plan, warnings and the upgrade info', () => {
    const p = readPreview({ plan: ['snapshot', 'clone'], warnings: ['w'], upgrade: { from: 'a#g1', to: 'b#g1', from_release: '15.1-RELEASE', to_release: '15.2-RELEASE', image: 'b', snapshot: 'jo-pre', kept: ['etc'] } }, 'x')
    expect(p).toMatchObject({ from_generation: 'a#g1', to_generation: 'b#g1', steps: ['snapshot', 'clone'], warnings: ['w'], from_release: '15.1-RELEASE', image: 'b', snapshot: 'jo-pre', kept: ['etc'] })
    expect(releaseChange(p)).toBe('15.1-RELEASE to 15.2-RELEASE')
  })
  it('reads the demo shape and an empty answer', () => {
    expect(readPreview({ from_generation: 'a#g1', to_generation: 'a#g2', steps: ['s'] }, 'a#g2').steps).toEqual(['s'])
    const e = readPreview(undefined, 'a#g9')
    expect(e).toMatchObject({ from_generation: '', to_generation: 'a#g9', steps: [] })
    expect(releaseChange(e)).toBe('')
    expect(releaseChange({ from_release: '15.1-RELEASE', to_release: '15.1-RELEASE' })).toBe('15.1-RELEASE')
  })
})
