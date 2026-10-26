import { session, can, hasExt } from './composables/session'
import type { Feature } from './lib/features'

declare module 'vue-router' {
  interface RouteMeta {
    public?: boolean
    feature?: Feature
    hub?: boolean
    ext?: string
    title?: string
  }
}

/** Pure route guard, shared by both consoles. Returns a redirect target or undefined. */
export function guard(to: { meta: Record<string, unknown>; fullPath: string }): string | undefined {
  if (!to.meta.public && !session.authed) return '/login?next=' + encodeURIComponent(to.fullPath)
  if (to.meta.public && session.authed) return '/'
  if (to.meta.feature && !can(to.meta.feature as Feature)) return '/'
  if (to.meta.ext && !hasExt(to.meta.ext as string)) return '/'
  if (to.meta.hub && !session.info?.hub) return '/'
  return undefined
}
