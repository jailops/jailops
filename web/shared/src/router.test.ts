import { describe, expect, it } from 'vitest'
import { guard } from './router'
import { session } from './composables/session'

const to = (meta: Record<string, unknown>, fullPath = '/x') => ({ meta, fullPath })

describe('route guard', () => {
  it('sends the signed-out to login and remembers the target', () => {
    session.authed = false
    expect(guard(to({}, '/jails'))).toBe('/login?next=%2Fjails')
    expect(guard(to({ public: true }))).toBeUndefined()
  })
  it('hides features the server lacks and hub pages on a non-hub', () => {
    session.authed = true
    session.info = { api_extensions: ['jails'], hub: false } as never
    expect(guard(to({ feature: 'jails' }))).toBeUndefined()
    expect(guard(to({ feature: 'backups' }))).toBe('/')
    expect(guard(to({ hub: true }))).toBe('/')
    session.info = { api_extensions: [], hub: true } as never
    expect(guard(to({ hub: true }))).toBeUndefined()
  })
})
