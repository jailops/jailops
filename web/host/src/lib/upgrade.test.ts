import { describe, expect, it } from 'vitest'
import { readPreview, releaseChange, releaseKey, releaseOlder } from './upgrade'

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
})

describe('the answer of a dry run', () => {
  it('reads the plan, the warnings and the upgrade info', () => {
    const p = readPreview({ plan: ['snapshot', 'pkg -r'], warnings: ['w'], upgrade: { from_release: '15.1-RELEASE', to_release: '15.2-RELEASE', snapshot: 'jo-pre', base_changes: ['FreeBSD-runtime 15.1p6 -> 15.2p0'], ports_packages: ['nginx'], health_check: 'service -e' } })
    expect(p.steps).toEqual(['snapshot', 'pkg -r'])
    expect(p.warnings).toEqual(['w'])
    expect(p.base_changes).toHaveLength(1)
    expect(p.ports_packages).toEqual(['nginx'])
    expect(p.nothing).toBe(false)
    expect(releaseChange(p)).toBe('15.1-RELEASE to 15.2-RELEASE')
  })
  it('reads nothing to upgrade and an empty answer', () => {
    const n = readPreview({ upgrade: { nothing_to_upgrade: true, installed: '15.1-RELEASE-p6' } })
    expect(n.nothing).toBe(true)
    expect(n.installed).toBe('15.1-RELEASE-p6')
    const e = readPreview(undefined)
    expect(e.steps).toEqual([])
    expect(releaseChange(e)).toBe('')
    expect(releaseChange({ from_release: '15.1-RELEASE', to_release: '15.1-RELEASE' })).toBe('15.1-RELEASE')
  })
})
