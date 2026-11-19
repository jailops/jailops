// The ONLY module that touches the network. Everything else goes through a Transport, so the
// demo mode swaps the transport and the rest of the UI cannot tell the difference.
import type { MutationResult, Operation, ApiEvent, ApiErrorBody } from './types'

const BASE = '/api/v1'

/** An API failure: the server's api.Error plus the HTTP status. */
export class ApiError extends Error {
  readonly code: string
  readonly hint: string
  readonly detail: Record<string, string>
  readonly status: number
  constructor(body: Partial<ApiErrorBody> & { message: string }, status = 0) {
    super(body.message)
    this.name = 'ApiError'
    this.code = body.code ?? 'internal'
    this.hint = body.hint ?? ''
    this.detail = body.detail ?? {}
    this.status = status
  }
}

export interface RawRequest {
  method: string
  path: string
  body?: unknown
  /** A raw body (an upload): sent as it is, with its length, instead of JSON. */
  blob?: Blob
  /** The answer is bytes (a download): a 2xx reply carries them in `blob` instead of an envelope. */
  download?: boolean
  headers?: Record<string, string>
  signal?: AbortSignal
}
export interface RawResponse {
  status: number
  etag?: string
  /** The bytes of a download. */
  blob?: Blob
  /** The decoded api.Response envelope. */
  envelope: {
    type: 'sync' | 'async' | 'error'
    status?: string
    status_code?: number
    operation?: string
    error?: ApiErrorBody
    metadata?: unknown
  }
}
export interface EventStream {
  close(): void
}
export interface StreamHandlers {
  onEvent(e: ApiEvent): void
  onOpen?(): void
  onError?(): void
  /** Server asked for a reconnect delay (ms). */
  onRetry?(ms: number): void
}

/** What a transport must do. HttpTransport talks to /api/v1; the demo transport is in-process. */
export interface Transport {
  request(req: RawRequest): Promise<RawResponse>
  stream(path: string, handlers: StreamHandlers): EventStream
  /** A binary reply read as it arrives (a jail export file): the body of a GET. Absent in a transport that has none. */
  download?(path: string, signal?: AbortSignal): Promise<ReadableStream<Uint8Array>>
  /** A binary request body sent as it is read (a jail export file): the reply is an ordinary envelope. */
  upload?(path: string, body: Blob, contentType: string, signal?: AbortSignal): Promise<RawResponse>
}

const TOKEN_KEY = 'jailops.token'

export function storedToken(): string {
  try {
    return sessionStorage.getItem(TOKEN_KEY) ?? ''
  } catch {
    return ''
  }
}
export function storeToken(t: string): void {
  try {
    if (t) sessionStorage.setItem(TOKEN_KEY, t)
    else sessionStorage.removeItem(TOKEN_KEY)
  } catch {
    /* private window: the token then lives only in memory */
  }
  memToken = t
}
let memToken = ''
function currentToken(): string {
  return memToken || storedToken()
}

const CSRF_KEY = 'jailops.csrf'
let memCsrf = ''

/** The CSRF token of the browser session, from the login or session reply. */
export function storeCsrf(t: string): void {
  memCsrf = t
  try {
    if (t) sessionStorage.setItem(CSRF_KEY, t)
    else sessionStorage.removeItem(CSRF_KEY)
  } catch {
    /* kept in memory only */
  }
}

/** The token from the login reply, else the readable jailops_csrf cookie the host daemon sets. */
function csrfToken(): string {
  if (memCsrf) return memCsrf
  try {
    const v = sessionStorage.getItem(CSRF_KEY)
    if (v) return v
  } catch {
    /* ignore */
  }
  const m = /(?:^|;\s*)jailops_csrf=([^;]+)/.exec(document.cookie)
  return m ? decodeURIComponent(m[1]) : ''
}

export class HttpTransport implements Transport {
  private headers(method: string, extra: Record<string, string> = {}): { headers: Record<string, string>; tok: string } {
    const headers: Record<string, string> = { Accept: 'application/json', ...extra }
    const tok = currentToken()
    if (tok) headers['Authorization'] = `Bearer ${tok}`
    if (method !== 'GET' && method !== 'HEAD') {
      const c = csrfToken()
      if (c) headers['X-CSRF-Token'] = c
    }
    return { headers, tok }
  }

  private async fetch(path: string, init: RequestInit): Promise<Response> {
    try {
      return await fetch(BASE + path, { ...init, credentials: 'same-origin' })
    } catch (e) {
      if ((e as Error).name === 'AbortError') throw e
      throw new ApiError({ code: 'unreachable' as never, message: 'The server did not answer.', hint: 'Check that jailopsd is running and reachable.' })
    }
  }

  private async envelope(res: Response, tok: string): Promise<RawResponse> {
    // a token the server no longer accepts is forgotten, not kept for the next page load
    if (res.status === 401 && tok) storeToken('')
    let envelope: RawResponse['envelope']
    try {
      envelope = await res.json()
    } catch {
      envelope = { type: 'error', error: { code: codeFromStatus(res.status) as ApiErrorBody['code'], message: `Unexpected reply (HTTP ${res.status}).` } }
    }
    return { status: res.status, etag: res.headers.get('ETag') ?? undefined, envelope }
  }

  async request(req: RawRequest): Promise<RawResponse> {
    const { headers, tok } = this.headers(req.method, req.headers)
    let body: BodyInit | undefined
    if (req.blob) {
      headers['Content-Type'] = 'application/octet-stream'
      body = req.blob
    } else if (req.body !== undefined) {
      headers['Content-Type'] = 'application/json'
      body = JSON.stringify(req.body)
    }
    const res = await this.fetch(req.path, { method: req.method, headers, body, signal: req.signal })
    if (req.download && res.ok) {
      return { status: res.status, blob: await res.blob(), envelope: { type: 'sync', status_code: res.status } }
    }
    return this.envelope(res, tok)
  }

  async download(path: string, signal?: AbortSignal): Promise<ReadableStream<Uint8Array>> {
    const { headers, tok } = this.headers('GET')
    const res = await this.fetch(path, { method: 'GET', headers, signal })
    if (!res.ok || !res.body) {
      const r = await this.envelope(res, tok)
      throw new ApiError((r.envelope.error ?? { code: codeFromStatus(res.status), message: `HTTP ${res.status}` }) as ApiErrorBody, res.status)
    }
    return res.body
  }

  async upload(path: string, body: Blob, contentType: string, signal?: AbortSignal): Promise<RawResponse> {
    const { headers, tok } = this.headers('POST', { 'Content-Type': contentType })
    const res = await this.fetch(path, { method: 'POST', headers, body, signal })
    return this.envelope(res, tok)
  }

  /**
   * Server-sent events over fetch, so the bearer token travels in the Authorization header and
   * never in a URL. Honors `id:` (sent back as Last-Event-ID after a reconnect) and `retry:`.
   */
  stream(path: string, h: StreamHandlers): EventStream {
    const ctl = new AbortController()
    void (async () => {
      try {
        const headers: Record<string, string> = { Accept: 'text/event-stream' }
        const tok = currentToken()
        if (tok) headers['Authorization'] = `Bearer ${tok}`
        const resume = lastEventIds.get(path)
        if (resume) headers['Last-Event-ID'] = resume
        const res = await fetch(BASE + path, { headers, signal: ctl.signal, credentials: 'same-origin' })
        if (!res.ok || !res.body) throw new Error(`HTTP ${res.status}`)
        h.onOpen?.()
        const reader = res.body.pipeThrough(new TextDecoderStream()).getReader()
        let buf = ''
        for (;;) {
          const { value, done } = await reader.read()
          if (done) break
          buf += value
          let i: number
          while ((i = buf.search(/\r?\n\r?\n/)) >= 0) {
            const frame = buf.slice(0, i)
            buf = buf.slice(i).replace(/^\r?\n\r?\n/, '')
            handleFrame(frame, h, (id) => lastEventIds.set(path, id))
          }
        }
        throw new Error('stream closed')
      } catch {
        if (!ctl.signal.aborted) h.onError?.()
      }
    })()
    return { close: () => ctl.abort() }
  }
}

// The id of the last event of each stream, by path: a reconnect of /events resumes after its own last event, and the
// output of a console never inherits the sequence of another stream.
const lastEventIds = new Map<string, string>()

/** Parse one SSE frame (exported for tests). */
export function handleFrame(frame: string, h: StreamHandlers, setId?: (id: string) => void): void {
  let data = ''
  for (const line of frame.split(/\r?\n/)) {
    if (!line || line.startsWith(':')) continue
    const c = line.indexOf(':')
    const field = c < 0 ? line : line.slice(0, c)
    const val = c < 0 ? '' : line.slice(c + 1).replace(/^ /, '')
    if (field === 'data') data += (data ? '\n' : '') + val
    else if (field === 'id') setId?.(val)
    else if (field === 'retry') h.onRetry?.(Number(val))
  }
  if (!data) return
  try {
    h.onEvent(JSON.parse(data) as ApiEvent)
  } catch {
    /* ignore a malformed frame */
  }
}

/** Map an HTTP status to an error code, as the protocol specifies for non-JSON bodies. */
export function codeFromStatus(status: number): string {
  switch (status) {
    case 400: return 'invalid'
    case 401: return 'unauthorized'
    case 403: return 'forbidden'
    case 404: return 'not_found'
    case 405: case 501: return 'unsupported'
    case 409: return 'already_exists'
    case 412: return 'precondition_failed'
    case 429: case 502: case 503: case 504: return 'busy'
  }
  return status >= 400 && status < 500 ? 'invalid' : 'internal'
}

type Listener = () => void

/** The server holds GET /operations/<id>/wait this long (seconds) before answering. */
export let waitSeconds = 5
export function setWaitSeconds(n: number): void {
  waitSeconds = n
}

/** The typed client. Views never call fetch. */
export class Client {
  private unauthorized: Listener[] = []
  private filter: ((path: string) => string) | null = null
  constructor(public transport: Transport) {}

  /** Rewrites every request path before it is sent (the host console adds `?project=` here). */
  setRequestFilter(fn: ((path: string) => string) | null): void {
    this.filter = fn
  }

  onUnauthorized(fn: Listener): void {
    this.unauthorized.push(fn)
  }

  /** A server-sent event stream whose path goes through the request filter (the project) like any request. */
  stream(path: string, h: StreamHandlers): EventStream {
    return this.transport.stream(this.filter ? this.filter(path) : path, h)
  }

  private async send(method: string, path: string, body?: unknown, opts: { etag?: string; signal?: AbortSignal; blob?: Blob; download?: boolean } = {}): Promise<RawResponse> {
    const headers: Record<string, string> = {}
    if (opts.etag) headers['If-Match'] = opts.etag
    const r = await this.transport.request({ method, path: this.filter ? this.filter(path) : path, body, blob: opts.blob, download: opts.download, headers, signal: opts.signal })
    if (r.envelope.type === 'error' || r.status >= 400) {
      const e = r.envelope.error ?? { code: 'internal', message: `HTTP ${r.status}` }
      const err = new ApiError(e as ApiErrorBody, r.status)
      if (err.code === 'unauthorized' || r.status === 401) this.unauthorized.forEach((f) => f())
      throw err
    }
    return r
  }

  /** The bytes of a file of a jail (GET), read whole. */
  async downloadFile(path: string, signal?: AbortSignal): Promise<Blob> {
    return (await this.send('GET', path, undefined, { signal, download: true })).blob ?? new Blob()
  }

  /** PUT bytes as the body of a file of a jail, with their length. */
  async uploadFile<T>(path: string, blob: Blob, signal?: AbortSignal): Promise<T> {
    return (await this.send('PUT', path, undefined, { signal, blob })).envelope.metadata as T
  }

  /** GET a sync resource. Nil lists arrive as null and are normalised by the caller's type. */
  async get<T>(path: string, signal?: AbortSignal): Promise<T> {
    return (await this.send('GET', path, undefined, { signal })).envelope.metadata as T
  }
  async getWithEtag<T>(path: string): Promise<{ data: T; etag?: string }> {
    const r = await this.send('GET', path)
    return { data: r.envelope.metadata as T, etag: r.etag }
  }
  async list<T>(path: string, signal?: AbortSignal): Promise<T[]> {
    const m = await this.get<T[] | null>(path, signal)
    return Array.isArray(m) ? m : []
  }

  /** POST, PUT, PATCH or DELETE. An async reply carries the started operation. */
  async mutate<T = unknown>(method: 'POST' | 'PUT' | 'PATCH' | 'DELETE', path: string, body?: unknown, opts: { etag?: string } = {}): Promise<MutationResult<T>> {
    const r = await this.send(method, path, body, opts)
    if (r.envelope.type === 'async') {
      const op = r.envelope.metadata as Operation | undefined
      if (op && op.id) return { kind: 'async', op }
      const id = (r.envelope.operation ?? '').split('/').pop() ?? ''
      return { kind: 'async', op: await this.get<Operation>(`/operations/${id}`) }
    }
    return { kind: 'sync', data: r.envelope.metadata as T, etag: r.etag }
  }

  /** A binary GET read as it arrives; the reply of an error is thrown as an ApiError. */
  download(path: string, signal?: AbortSignal): Promise<ReadableStream<Uint8Array>> {
    if (!this.transport.download) throw new ApiError({ code: 'unsupported' as never, message: 'This transport cannot download files.' })
    return this.transport.download(this.filter ? this.filter(path) : path, signal)
  }

  /** A binary POST (a file, sent as the browser reads it); the reply is the finished operation: its error, or its metadata. */
  async upload<T>(path: string, body: Blob, contentType: string, signal?: AbortSignal): Promise<T> {
    if (!this.transport.upload) throw new ApiError({ code: 'unsupported' as never, message: 'This transport cannot upload files.' })
    const r = await this.transport.upload(this.filter ? this.filter(path) : path, body, contentType, signal)
    if (r.envelope.type === 'error' || r.status >= 400) {
      const e = r.envelope.error ?? { code: 'internal', message: `HTTP ${r.status}` }
      const err = new ApiError(e as ApiErrorBody, r.status)
      if (err.code === 'unauthorized' || r.status === 401) this.unauthorized.forEach((f) => f())
      throw err
    }
    if (r.envelope.type === 'async') {
      const op = r.envelope.metadata as Operation
      if (op.error) throw new ApiError(op.error as ApiErrorBody, r.status)
      return op.metadata as T
    }
    return r.envelope.metadata as T
  }

  getOperation(id: string): Promise<Operation> {
    return this.get<Operation>(`/operations/${encodeURIComponent(id)}/wait?timeout=${waitSeconds}`)
  }
  async cancelOperation(id: string): Promise<void> {
    await this.mutate('DELETE', `/operations/${encodeURIComponent(id)}`)
  }

  events(h: StreamHandlers): EventStream {
    return this.transport.stream(this.filter ? this.filter('/events') : '/events', h)
  }
}

let client = new Client(new HttpTransport())
export function getClient(): Client {
  return client
}
export function setClient(c: Client): void {
  client = c
}
