import { describe, expect, it } from 'vitest'
import { FramingCheck, MAGIC, checking, fileName, readManifest, scanFile } from './exportfile'

type U8 = Uint8Array<ArrayBuffer>
const enc = new TextEncoder()
function rec(type: string, payload: Uint8Array | string): U8 {
  const p = typeof payload === 'string' ? enc.encode(payload) : payload
  const out = new Uint8Array(9 + p.length)
  out[0] = type.charCodeAt(0)
  new DataView(out.buffer).setBigUint64(1, BigInt(p.length))
  out.set(p, 9)
  return out
}
function cat(...a: Uint8Array[]): U8 {
  const out = new Uint8Array(a.reduce((n, x) => n + x.length, 0))
  let o = 0
  for (const x of a) { out.set(x, o); o += x.length }
  return out
}
const manifest = { format: 1, name: 'web', project: 'alpha', layout: 'thin', image: { name: 'fb' }, parts: [], bundle: { spec: {} } }
function file(parts = 1): U8 {
  const body: Uint8Array[] = []
  for (let i = 0; i < parts; i++) body.push(rec('D', '{"dataset":"etc"}'), rec('B', new Uint8Array(100)), rec('E', '{}'))
  return cat(enc.encode(MAGIC), rec('M', JSON.stringify(manifest)), ...body, rec('F', JSON.stringify({ parts })))
}

describe('export file', () => {
  it('reads the manifest from the start of the file', async () => {
    const m = await readManifest(new Blob([file()]))
    expect(m.name).toBe('web')
    expect(m.project).toBe('alpha')
  })
  it('refuses what is not an export file, a newer format and a cut manifest', async () => {
    await expect(readManifest(new Blob(['hello world, this is text']))).rejects.toThrow('not a jailops export file')
    await expect(readManifest(new Blob([cat(enc.encode(MAGIC), rec('M', JSON.stringify({ ...manifest, format: 2 })))]))).rejects.toThrow('format 2')
    await expect(readManifest(new Blob([file().slice(0, 40)]))).rejects.toThrow('truncated')
    await expect(readManifest(new Blob([cat(enc.encode(MAGIC), rec('D', '{}'))]))).rejects.toThrow('manifest is missing')
  })
  it('counts the streams of a whole file, at any piece size', () => {
    const f = file(3)
    for (const size of [1, 7, 9, 64, f.length]) {
      const fc = new FramingCheck()
      for (let i = 0; i < f.length; i += size) fc.push(f.subarray(i, i + size))
      expect(fc.finish()).toBe(3)
    }
  })
  it('says truncated for a file that ends anywhere before the footer', () => {
    const f = file(2)
    for (const cut of [0, 5, 20, 60, 150, f.length - 1]) {
      const fc = new FramingCheck()
      fc.push(f.subarray(0, cut))
      expect(() => fc.finish()).toThrow(/truncated|not a jailops/)
    }
  })
  it('finds a footer that does not match, an unknown record and data after the end', () => {
    const bad = cat(enc.encode(MAGIC), rec('M', JSON.stringify(manifest)), rec('F', '{"parts":2}'))
    expect(() => new FramingCheck().push(bad)).toThrow('footer does not match')
    expect(() => new FramingCheck().push(cat(enc.encode(MAGIC), rec('Z', '')))).toThrow('unexpected record')
    expect(() => new FramingCheck().push(cat(file(), new Uint8Array([1])))).toThrow('after its end')
    expect(() => new FramingCheck().push(enc.encode('JAILOPS-EXPORT 2\nxxxxxxxxx'))).toThrow('not a jailops')
  })
  it('scans a blob and checks a stream that flows through', async () => {
    expect(await scanFile(new Blob([file(2)]))).toBe(2)
    await expect(scanFile(new Blob([file(2).slice(0, 80)]))).rejects.toThrow('truncated')
    const source = (b: Uint8Array) => new ReadableStream<Uint8Array>({ start(c) { for (let i = 0; i < b.length; i += 50) c.enqueue(b.slice(i, i + 50)); c.close() } })
    const read = async (rs: ReadableStream<Uint8Array>) => { let n = 0; const r = rs.getReader(); for (;;) { const x = await r.read(); if (x.done) return n; n += x.value.length } }
    const whole = checking()
    expect(await read(source(file(1)).pipeThrough(whole.stream))).toBe(file(1).length)
    expect(whole.result()).toBe(1)
    const cut = checking()
    await read(source(file(1).slice(0, 150)).pipeThrough(cut.stream))
    expect(() => cut.result()).toThrow('truncated')
  })
  it('names the file after the jail', () => {
    expect(fileName('web')).toBe('web.jailexport')
  })
})
