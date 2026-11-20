import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { h } from 'vue'
import NotesMarkdown from '@/components/NotesMarkdown'
import { parseNotes, safeHref } from './notes'

const render = (text: string) => mount({ render: () => h(NotesMarkdown, { text }) })

describe('notes markdown', () => {
  it('renders the subset: headings, lists, code, emphasis and links', () => {
    const w = render('# Web\n\nServes **the site** with *nginx*.\nSecond line.\n\n- one\n- two `x`\n\n1. first\n2. second\n\n```\nservice nginx reload\n```\n\nSee [the wiki](https://wiki.example.org/web) or https://example.org/a.')
    expect(w.find('h3').text()).toBe('Web')
    expect(w.find('strong').text()).toBe('the site')
    expect(w.find('em').text()).toBe('nginx')
    expect(w.findAll('p')[0].find('br').exists()).toBe(true)
    expect(w.findAll('ul li').map((x) => x.text())).toEqual(['one', 'two x'])
    expect(w.find('ul code').text()).toBe('x')
    expect(w.findAll('ol li')).toHaveLength(2)
    expect(w.find('pre').text()).toBe('service nginx reload')
    const links = w.findAll('a')
    expect(links.map((a) => a.attributes('href'))).toEqual(['https://wiki.example.org/web', 'https://example.org/a'])
    expect(links[0].attributes('rel')).toContain('noopener')
    expect(links[0].attributes('target')).toBe('_blank')
  })

  it('keeps markup as text', () => {
    const w = render('<script>alert(1)</script>\n\n<img src="https://evil.example/x.png" onerror="alert(1)">\n\n<b>bold</b>')
    expect(w.find('script').exists()).toBe(false)
    expect(w.find('img').exists()).toBe(false)
    expect(w.find('b').exists()).toBe(false)
    expect(w.text()).toContain('<script>alert(1)</script>')
    expect(w.text()).toContain('<b>bold</b>')
  })

  it('makes links only of http, https and mailto', () => {
    const w = render('[a](javascript:alert(1)) [b](JaVaScRiPt:alert(1)) [c](data:text/html,<p>x</p>) [d](vbscript:x) [e](/relative) [f](mailto:ops@example.org)')
    expect(w.findAll('a').map((a) => a.attributes('href'))).toEqual(['mailto:ops@example.org'])
    expect(w.text()).toContain('[a](javascript:alert(1))')
    expect(safeHref('java\nscript:alert(1)')).toBe('')
    expect(safeHref(' https://example.org ')).toBe('https://example.org/')
  })

  it('loads no image', () => {
    const w = render('![logo](https://evil.example/logo.png)')
    expect(w.find('img').exists()).toBe(false)
    expect(w.find('a').exists()).toBe(false)
    expect(w.text()).toBe('![logo](https://evil.example/logo.png)')
  })

  it('reads an unclosed fence to the end and keeps what is inside it as it is', () => {
    expect(parseNotes('```\n# not a heading\n**x**')).toEqual([{ kind: 'code', text: '# not a heading\n**x**' }])
    expect(parseNotes('')).toEqual([])
  })
})
