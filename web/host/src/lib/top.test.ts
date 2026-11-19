import { describe, it, expect } from 'vitest'
import type { TopJail } from '@jailops/shared/api/types'
import { sortJails, netTotal, cpuText, uptimeText, share } from './top'

const j = (name: string, o: Partial<TopJail> = {}, project = 'web'): TopJail => ({ project, name, ...o })

describe('live usage', () => {
  it('sorts the biggest first and puts the unknown last', () => {
    const l = [j('a', { cpu_percent: 5 }), j('b'), j('c', { cpu_percent: 150 }), j('d', { cpu_percent: 0 })]
    expect(sortJails(l, 'cpu').map((x) => x.name)).toEqual(['c', 'a', 'd', 'b'])
    expect(sortJails(l, 'name').map((x) => x.name)).toEqual(['a', 'b', 'c', 'd'])
  })
  it('sorts by memory, disk and the network together', () => {
    const l = [j('a', { memory_bytes: 10, disk_bytes: 300, net_in_bytes_per_second: 1, net_out_bytes_per_second: 1 }), j('b', { memory_bytes: 20, disk_bytes: 100, net_out_bytes_per_second: 50 }), j('c')]
    expect(sortJails(l, 'mem').map((x) => x.name)).toEqual(['b', 'a', 'c'])
    expect(sortJails(l, 'disk').map((x) => x.name)).toEqual(['a', 'b', 'c'])
    expect(sortJails(l, 'net').map((x) => x.name)).toEqual(['b', 'a', 'c'])
    expect(netTotal(l[2])).toBeUndefined()
    expect(netTotal(l[1])).toBe(50)
  })
  it('keeps the order of the projects among equals', () => {
    const l = [j('z', {}, 'a'), j('a', {}, 'b'), j('b', {}, 'a')]
    expect(sortJails(l, 'name').map((x) => `${x.project}/${x.name}`)).toEqual(['a/b', 'a/z', 'b/a'])
  })
  it('says what it does not know', () => {
    expect(cpuText(j('a'))).toBe('n/a')
    expect(cpuText(j('a', { cpu_percent: 7.25 }))).toBe('7.3%')
    expect(cpuText(j('a', { cpu_percent: 137.4, approximate: true }))).toBe('~137%')
    expect(uptimeText(undefined)).toBe('-')
    expect(uptimeText(45)).toBe('45s')
    expect(uptimeText(7380)).toBe('2h3m')
    expect(uptimeText(86400 * 3 + 3600)).toBe('3d1h')
    expect(share(50, 200)).toBe(25)
    expect(share(500, 200)).toBe(100)
    expect(share(50, 0)).toBeUndefined()
    expect(share(undefined, 100)).toBeUndefined()
  })
})
