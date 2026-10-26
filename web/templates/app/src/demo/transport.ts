// An in-process Transport for `?demo`. Add routes as the console grows; keep canned data in data.ts.
import type { EventStream, RawRequest, RawResponse, StreamHandlers, Transport } from '@jailops/shared/api/client'
import { storedToken } from '@jailops/shared/api/client'
import { server } from './data'

export interface DemoOptions { signedOut?: boolean }

export function demoTransport(opts: DemoOptions = {}): Transport {
  let authed = !opts.signedOut
  const ok = (metadata: unknown): RawResponse => ({ status: 200, envelope: { type: 'sync', status: 'Success', status_code: 200, metadata } })
  const fail = (status: number, code: string, message: string): RawResponse => ({ status, envelope: { type: 'error', error: { code: code as never, message } } })
  return {
    async request(req: RawRequest): Promise<RawResponse> {
      if (!authed && storedToken()) authed = true
      const path = new URL(req.path, 'http://x').pathname.replace(/\/$/, '')
      if (path === '') return ok({ ...server(), auth: authed ? 'trusted' : 'untrusted' })
      if (path === '/auth/login') { authed = true; return ok({}) }
      if (path === '/auth/logout') { authed = false; return ok({}) }
      if (!authed) return fail(401, 'unauthorized', 'Authentication is required.')
      return fail(404, 'not_found', `No such route: ${req.method} ${path}`)
    },
    stream(_path: string, h: StreamHandlers): EventStream {
      const t = setTimeout(() => h.onOpen?.(), 0)
      return { close: () => clearTimeout(t) }
    },
  }
}
