import { describe, expect, it } from 'vitest'
import { helpText, ALL_KEYS } from './configKeys'
import { deleteItems } from '@/composables/jailActions'

describe('config key help', () => {
  it('reads the registry as written: it carries no developer markers', () => {
    expect(helpText(' Rate limit in.  Not enforced yet. ')).toBe('Rate limit in. Not enforced yet.')
    expect(helpText(undefined)).toBe('')
    for (const k of ALL_KEYS) expect(k.description ?? '').not.toMatch(/UNVERIFIED|^Not yet:/)
  })
})

describe('deleting a jail', () => {
  it('lists the root and the snapshots; the volumes it attaches stay', () => {
    const j = { metadata: { name: 'db' }, spec: { image: 'x', volumes: [{ volume: 'pgdata', mount: '/var/db/postgres' }] }, status: { power: 'running' } }
    const items = deleteItems(j as never, [{ status: { written: 1024 } }, { status: { written: 1024 } }] as never)
    expect(items).toEqual(['the root file system of db', '2 snapshots of the jail'])
  })
})
