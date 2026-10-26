// A small accessibility audit that runs in the page (no extra dependency): names, labels, ids, landmarks, headings
// and WCAG AA text contrast from computed styles. It is not axe, but it catches what regressed in practice.
import type { Page } from '@playwright/test'

export interface A11yIssue { rule: string; where: string; detail?: string }

export async function audit(page: Page): Promise<A11yIssue[]> {
  return page.evaluate(() => {
    const out: { rule: string; where: string; detail?: string }[] = []
    const vis = (el: Element) => {
      const r = (el as HTMLElement).getBoundingClientRect()
      const cs = getComputedStyle(el)
      return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none'
    }
    const path = (el: Element) => {
      const t = el.tagName.toLowerCase()
      return t + (el.id ? '#' + el.id : '') + (el.getAttribute('data-test') ? `[data-test=${el.getAttribute('data-test')}]` : '') + ((el.textContent ?? '').trim().slice(0, 24) ? ` "${(el.textContent ?? '').trim().slice(0, 24)}"` : '')
    }
    const nameOf = (el: Element): string => {
      const a = el.getAttribute('aria-label')?.trim()
      if (a) return a
      const lb = el.getAttribute('aria-labelledby')
      if (lb) return lb.split(/\s+/).map((i) => document.getElementById(i)?.textContent ?? '').join(' ').trim()
      if ('labels' in el && (el as HTMLInputElement).labels?.length) return Array.from((el as HTMLInputElement).labels!).map((l) => l.textContent ?? '').join(' ').trim()
      const imgAlt = el.querySelector('img[alt]')?.getAttribute('alt')
      return ((el.textContent ?? '') + (imgAlt ?? '') + (el.getAttribute('title') ?? '')).trim()
    }
    for (const el of document.querySelectorAll('button, a[href], [role=button], [role=tab], [role=link]')) if (vis(el) && !nameOf(el)) out.push({ rule: 'name', where: path(el), detail: 'interactive element without an accessible name' })
    for (const el of document.querySelectorAll('input:not([type=hidden]), select, textarea')) if (vis(el) && !nameOf(el)) out.push({ rule: 'label', where: path(el), detail: 'form control without a label' })
    for (const el of document.querySelectorAll('img')) if (!el.hasAttribute('alt')) out.push({ rule: 'img-alt', where: path(el) })
    const ids = new Map<string, number>()
    for (const el of document.querySelectorAll('[id]')) ids.set(el.id, (ids.get(el.id) ?? 0) + 1)
    for (const [id, n] of ids) if (n > 1) out.push({ rule: 'duplicate-id', where: '#' + id, detail: `${n} elements` })
    for (const el of document.querySelectorAll('[aria-labelledby],[aria-describedby]')) {
      for (const a of ['aria-labelledby', 'aria-describedby']) for (const i of (el.getAttribute(a) ?? '').split(/\s+/).filter(Boolean)) if (!document.getElementById(i)) out.push({ rule: 'aria-ref', where: path(el), detail: `${a} -> #${i} missing` })
    }
    if (document.querySelectorAll('main').length !== 1) out.push({ rule: 'landmark', where: 'main', detail: 'expected exactly one main' })
    if (document.querySelectorAll('h1').length !== 1) out.push({ rule: 'heading', where: 'h1', detail: `found ${document.querySelectorAll('h1').length} h1` })
    if (!document.documentElement.lang) out.push({ rule: 'lang', where: 'html' })
    for (const el of document.querySelectorAll('table')) if (vis(el) && !el.querySelector('th')) out.push({ rule: 'table-header', where: path(el) })

    // Contrast of every visible text node.
    const parse = (c: string): [number, number, number, number] | null => {
      const m = c.match(/rgba?\(([^)]+)\)/) ?? null
      if (m) { const p = m[1].split(/[,/ ]+/).filter(Boolean).map(Number); return [p[0], p[1], p[2], p.length > 3 ? p[3] : 1] }
      const c2 = c.match(/color\(srgb ([^)]+)\)/)
      if (c2) { const p = c2[1].split(/[ /]+/).map(Number); return [p[0] * 255, p[1] * 255, p[2] * 255, p.length > 3 ? p[3] : 1] }
      return null
    }
    const over = (f: number[], b: number[]) => { const a = f[3]; return [f[0] * a + b[0] * (1 - a), f[1] * a + b[1] * (1 - a), f[2] * a + b[2] * (1 - a), 1] }
    const bgOf = (el: Element): number[] => {
      const stack: number[][] = []
      for (let e: Element | null = el; e; e = e.parentElement) { const c = parse(getComputedStyle(e).backgroundColor); if (c && c[3] > 0) { stack.push(c); if (c[3] === 1) break } }
      let base = [255, 255, 255, 1]
      const root = parse(getComputedStyle(document.body).backgroundColor)
      if (root && root[3] === 1) base = root
      for (const c of stack.reverse()) base = over(c, base)
      return base
    }
    const lum = (c: number[]) => { const f = (v: number) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4 }; return 0.2126 * f(c[0]) + 0.7152 * f(c[1]) + 0.0722 * f(c[2]) }
    const seen = new Set<Element>()
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      const el = n.parentElement
      if (!el || seen.has(el) || !(n.textContent ?? '').trim() || !vis(el)) continue
      seen.add(el)
      if (el.closest('[disabled], [aria-disabled=true], .sr-only, script, style')) continue
      const cs = getComputedStyle(el)
      const fg = parse(cs.color)
      if (!fg) continue
      const bg = bgOf(el)
      const f = over(fg, bg)
      const L1 = lum(f), L2 = lum(bg)
      const ratio = (Math.max(L1, L2) + 0.05) / (Math.min(L1, L2) + 0.05)
      const size = parseFloat(cs.fontSize)
      const large = size >= 24 || (size >= 18.66 && Number(cs.fontWeight) >= 700)
      if (ratio < (large ? 3 : 4.5)) out.push({ rule: 'contrast', where: path(el), detail: `${ratio.toFixed(2)}:1 (${cs.color} on rgb(${bg.slice(0, 3).map(Math.round).join(',')}))` })
    }
    return out
  })
}
