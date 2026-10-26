// The frame codec of the exec protocol (sdk/api exec.go, PROTOCOL.md Exec): type byte, u32 big-endian length,
// payload. The daemon speaks it over an HTTP upgrade, which a browser cannot open. The codec is here so a
// bridge (a WebSocket or a polled route) only has to move bytes.

export const Frame = { Stdin: 1, Stdout: 2, Stderr: 3, Resize: 4, Signal: 5, Exit: 6 } as const
export const MAX_FRAME = 64 << 10

export function encodeFrame(type: number, payload: Uint8Array = new Uint8Array()): Uint8Array {
  if (payload.length > MAX_FRAME) throw new Error(`frame of ${payload.length} bytes exceeds ${MAX_FRAME}`)
  const out = new Uint8Array(5 + payload.length)
  out[0] = type
  new DataView(out.buffer).setUint32(1, payload.length)
  out.set(payload, 5)
  return out
}
export const resizePayload = (rows: number, cols: number) => {
  const b = new Uint8Array(4)
  const v = new DataView(b.buffer)
  v.setUint16(0, rows)
  v.setUint16(2, cols)
  return b
}

/** Incremental decoder: feed chunks, get whole frames. A frame larger than MAX_FRAME or of an unknown type is an error. */
export class FrameDecoder {
  private buf = new Uint8Array()
  push(chunk: Uint8Array): Array<{ type: number; payload: Uint8Array }> {
    const m = new Uint8Array(this.buf.length + chunk.length)
    m.set(this.buf)
    m.set(chunk, this.buf.length)
    this.buf = m
    const out: Array<{ type: number; payload: Uint8Array }> = []
    while (this.buf.length >= 5) {
      const type = this.buf[0]
      const n = new DataView(this.buf.buffer, this.buf.byteOffset).getUint32(1)
      if (type < Frame.Stdin || type > Frame.Exit) throw new Error(`unknown frame type ${type}`)
      if (n > MAX_FRAME) throw new Error(`frame of ${n} bytes exceeds ${MAX_FRAME}`)
      if (this.buf.length < 5 + n) break
      out.push({ type, payload: this.buf.slice(5, 5 + n) })
      this.buf = this.buf.slice(5 + n)
    }
    return out
  }
}
export const exitStatus = (p: Uint8Array) => new DataView(p.buffer, p.byteOffset, p.byteLength).getInt32(0)
