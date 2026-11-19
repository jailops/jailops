// The jail export file (docs/export-import.md), as far as a browser needs it: the manifest, to show what an import would do
// before the file is sent, and the framing, to know that a file that was downloaded or chosen is whole. The streams of
// the datasets are not decoded here: the daemon verifies their checksums when it receives them.
import type { ExportManifest } from '@jailops/shared/api/types'

export const MAGIC = 'JAILOPS-EXPORT 1\n'
const HEADER = 9
const MAX_RECORD = 64 * 1024 * 1024

export class ExportFileError extends Error {
  constructor(message: string, readonly truncated = false) {
    super(message)
    this.name = 'ExportFileError'
  }
}

const TRUNCATED = 'The export file is truncated.'
const NOT_EXPORT = 'This is not a jailops export file.'

function recordHeader(b: Uint8Array): { type: string; length: number } {
  const v = new DataView(b.buffer, b.byteOffset, HEADER)
  const length = Number(v.getBigUint64(1))
  if (length > MAX_RECORD) throw new ExportFileError(`The export file is corrupt: a record of ${length} bytes.`)
  return { type: String.fromCharCode(b[0]), length }
}

/** The manifest at the start of a file; only its first bytes are read. */
export async function readManifest(file: Blob): Promise<ExportManifest> {
  const head = new Uint8Array(await file.slice(0, MAGIC.length + HEADER).arrayBuffer())
  if (head.length < MAGIC.length || new TextDecoder().decode(head.slice(0, MAGIC.length)) !== MAGIC) throw new ExportFileError(NOT_EXPORT)
  if (head.length < MAGIC.length + HEADER) throw new ExportFileError(TRUNCATED, true)
  const { type, length } = recordHeader(head.slice(MAGIC.length))
  if (type !== 'M') throw new ExportFileError('The export file is corrupt: the manifest is missing.')
  const start = MAGIC.length + HEADER
  const body = new Uint8Array(await file.slice(start, start + length).arrayBuffer())
  if (body.length < length) throw new ExportFileError(TRUNCATED, true)
  let m: ExportManifest
  try {
    m = JSON.parse(new TextDecoder().decode(body)) as ExportManifest
  } catch {
    throw new ExportFileError('The export file is corrupt: the manifest cannot be read.')
  }
  if (m.format !== 1) throw new ExportFileError(`This export file has format ${m.format}; this console reads format 1.`)
  return m
}

/** Follows the records of a file as its bytes arrive, in pieces of any size, and says at the end whether it was whole. */
export class FramingCheck {
  private pos = -MAGIC.length // < 0: inside the magic line
  private head = new Uint8Array(HEADER)
  private have = 0
  private left = 0
  private type = ''
  private foot: number[] = []
  private parts = 0
  private done = false
  private magic = ''

  push(chunk: Uint8Array): void {
    let i = 0
    while (i < chunk.length) {
      if (this.done) throw new ExportFileError('The export file is corrupt: there is data after its end.')
      if (this.pos < 0) {
        const k = Math.min(-this.pos, chunk.length - i)
        this.magic += new TextDecoder().decode(chunk.subarray(i, i + k))
        this.pos += k
        i += k
        if (this.pos === 0 && this.magic !== MAGIC) throw new ExportFileError(NOT_EXPORT)
        continue
      }
      if (this.left > 0) {
        const k = Math.min(this.left, chunk.length - i)
        if (this.type === 'F') this.foot.push(...chunk.subarray(i, i + k))
        this.left -= k
        i += k
        if (this.left === 0) this.end()
        continue
      }
      const k = Math.min(HEADER - this.have, chunk.length - i)
      this.head.set(chunk.subarray(i, i + k), this.have)
      this.have += k
      i += k
      if (this.have < HEADER) continue
      const r = recordHeader(this.head)
      this.have = 0
      this.type = r.type
      this.left = r.length
      if (r.type === 'D') this.parts++
      else if (!'MBEF'.includes(r.type)) throw new ExportFileError(`The export file is corrupt: unexpected record "${r.type}".`)
      if (r.length === 0) this.end()
    }
  }

  private end(): void {
    if (this.type !== 'F') return
    let f: { parts?: number }
    try {
      f = JSON.parse(new TextDecoder().decode(new Uint8Array(this.foot)))
    } catch {
      throw new ExportFileError('The export file is corrupt: the footer cannot be read.')
    }
    if (f.parts !== this.parts) throw new ExportFileError('The export file is corrupt: the footer does not match the parts.')
    this.done = true
  }

  /** The number of dataset streams; throws when the file ended before its footer. */
  finish(): number {
    if (!this.done) throw new ExportFileError(this.pos < 0 && this.magic === '' ? NOT_EXPORT : TRUNCATED, true)
    return this.parts
  }
}

/** Reads a whole file (in pieces: nothing is held) and returns its number of streams, or throws. */
export async function scanFile(file: Blob): Promise<number> {
  const fc = new FramingCheck()
  for (let o = 0; o < file.size; o += 4 << 20) fc.push(new Uint8Array(await file.slice(o, o + (4 << 20)).arrayBuffer()))
  return fc.finish()
}

/**
 * A pass-through that checks the framing of what flows through it. A stream does not reliably carry an error from its end
 * to its reader, so the verdict is asked for once the pipeline finished: `result()` is the number of streams of a whole
 * file, and throws for one that is cut off or damaged.
 */
export function checking(): { stream: TransformStream<Uint8Array, Uint8Array>; result: () => number } {
  const fc = new FramingCheck()
  const stream = new TransformStream<Uint8Array, Uint8Array>({
    transform(chunk, ctl) {
      fc.push(chunk)
      ctl.enqueue(chunk)
    },
  })
  return { stream, result: () => fc.finish() }
}

/** The name a downloaded file gets. */
export function fileName(jail: string): string {
  return `${jail}.jailexport`
}
