// The notes of an object (spec.notes) are plain text that may use a small part of Markdown: headings, paragraphs,
// lists, fenced and inline code, bold, italics and links. The text is parsed into a tree of plain strings and the
// component builds elements from it: nothing becomes HTML, so markup in the text stays text. A link is a link only for
// http, https and mailto; images are not loaded (the text of one stays as it was written).

export type Inline =
  | { kind: 'text'; text: string }
  | { kind: 'code'; text: string }
  | { kind: 'strong'; children: Inline[] }
  | { kind: 'em'; children: Inline[] }
  | { kind: 'link'; href: string; children: Inline[] }
  | { kind: 'br' }

export type Block =
  | { kind: 'heading'; level: 1 | 2 | 3; children: Inline[] }
  | { kind: 'para'; children: Inline[] }
  | { kind: 'list'; ordered: boolean; items: Inline[][] }
  | { kind: 'code'; text: string }

/** The server's limit of spec.notes, in bytes (api.MaxNotes). */
export const MAX_NOTES = 64 * 1024

/** The size of a text as the server counts it: UTF-8 bytes. */
export const notesBytes = (s: string): number => new TextEncoder().encode(s).length

/** A link target the page may follow: an absolute http, https or mailto address; anything else is '' (shown as text). */
export function safeHref(raw: string): string {
  const t = raw.trim()
  // a control character or a space inside the address is not a link (it could hide what the address is)
  if (!t || /[\s\u0000-\u001f\u007f]/.test(t)) return ''
  let u: URL
  try {
    u = new URL(t)
  } catch {
    return ''
  }
  return u.protocol === 'http:' || u.protocol === 'https:' || u.protocol === 'mailto:' ? u.href : ''
}

// a bare address ends before trailing punctuation that belongs to the sentence
const trimURL = (s: string): string => s.replace(/[.,;:!?)\]'"]+$/, '')

/** The inline parts of one paragraph or item: code first (nothing inside it is read), then links, bold, italics, bare addresses. */
export function parseInline(text: string): Inline[] {
  const out: Inline[] = []
  let buf = ''
  const flush = () => {
    if (buf) out.push({ kind: 'text', text: buf })
    buf = ''
  }
  let i = 0
  while (i < text.length) {
    const rest = text.slice(i)
    const c = text[i]
    if (c === '\n') {
      flush()
      out.push({ kind: 'br' })
      i++
      continue
    }
    if (c === '`') {
      const end = text.indexOf('`', i + 1)
      if (end > i + 1) {
        flush()
        out.push({ kind: 'code', text: text.slice(i + 1, end) })
        i = end + 1
        continue
      }
    }
    if (c === '!' && text[i + 1] === '[') {
      // an image stays text: the console loads nothing a note names
      const m = /^!\[[^\]\n]*\]\([^)\n]*\)/.exec(rest)
      if (m) {
        buf += m[0]
        i += m[0].length
        continue
      }
    }
    if (c === '[') {
      const m = /^\[([^\]\n]+)\]\(([^)\s]+)\)/.exec(rest)
      if (m) {
        const href = safeHref(m[2])
        flush()
        if (href) out.push({ kind: 'link', href, children: parseInline(m[1]) })
        else out.push({ kind: 'text', text: m[0] })
        i += m[0].length
        continue
      }
    }
    if (rest.startsWith('**')) {
      const end = text.indexOf('**', i + 2)
      if (end > i + 2) {
        flush()
        out.push({ kind: 'strong', children: parseInline(text.slice(i + 2, end)) })
        i = end + 2
        continue
      }
    }
    if ((c === '*' || c === '_') && text[i + 1] !== c && text[i + 1] !== ' ' && (i === 0 || !/\w/.test(text[i - 1]))) {
      const end = text.indexOf(c, i + 1)
      if (end > i + 1 && text[end - 1] !== ' ' && !/\w/.test(text[end + 1] ?? '')) {
        flush()
        out.push({ kind: 'em', children: parseInline(text.slice(i + 1, end)) })
        i = end + 1
        continue
      }
    }
    if ((c === 'h' || c === 'H') && (i === 0 || !/\w/.test(text[i - 1]))) {
      const m = /^https?:\/\/[^\s<>]+/i.exec(rest)
      if (m) {
        const url = trimURL(m[0])
        const href = safeHref(url)
        if (href) {
          flush()
          out.push({ kind: 'link', href, children: [{ kind: 'text', text: url }] })
          i += url.length
          continue
        }
      }
    }
    buf += c
    i++
  }
  flush()
  return out
}

const reFence = /^\s{0,3}(```|~~~)/
const reHeading = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/
const reBullet = /^\s{0,3}[-*+]\s+(.*)$/
const reNumber = /^\s{0,3}\d{1,9}[.)]\s+(.*)$/

/** The blocks of a text. Lines keep their breaks inside a paragraph: notes are written line by line. */
export function parseNotes(text: string | undefined): Block[] {
  const lines = (text ?? '').replace(/\r\n?/g, '\n').split('\n')
  const out: Block[] = []
  let para: string[] = []
  let list = null as { ordered: boolean; items: string[] } | null
  const endPara = () => {
    if (para.length) out.push({ kind: 'para', children: parseInline(para.join('\n')) })
    para = []
  }
  const endList = () => {
    if (list) out.push({ kind: 'list', ordered: list.ordered, items: list.items.map((x) => parseInline(x)) })
    list = null
  }
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    const fence = reFence.exec(line)
    if (fence) {
      endPara()
      endList()
      const body: string[] = []
      let j = i + 1
      while (j < lines.length && !lines[j].trimStart().startsWith(fence[1])) body.push(lines[j++])
      out.push({ kind: 'code', text: body.join('\n') })
      i = j
      continue
    }
    if (!line.trim()) {
      endPara()
      endList()
      continue
    }
    const h = reHeading.exec(line)
    if (h) {
      endPara()
      endList()
      out.push({ kind: 'heading', level: Math.min(h[1].length, 3) as 1 | 2 | 3, children: parseInline(h[2]) })
      continue
    }
    const b = reBullet.exec(line)
    const n = b ? null : reNumber.exec(line)
    if (b || n) {
      endPara()
      const ordered = !!n
      if (list && list.ordered !== ordered) endList()
      if (!list) list = { ordered, items: [] }
      list.items.push((b ?? n)![1])
      continue
    }
    if (list && /^\s+\S/.test(line)) {
      // an indented line continues the item above it
      list.items[list.items.length - 1] += '\n' + line.trim()
      continue
    }
    endList()
    para.push(line)
  }
  endPara()
  endList()
  return out
}
