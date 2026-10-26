import { describe, expect, it } from 'vitest'
import { routes } from './router'
import { s } from '@/i18n'

describe('__APP__ console', () => {
  it('has a login route and a catch-all', () => {
    expect(routes.some((r) => r.name === 'login')).toBe(true)
    expect(routes.at(-1)?.name).toBe('notfound')
  })
  it('names itself', () => {
    expect(s.app.role.length).toBeGreaterThan(0)
  })
})
