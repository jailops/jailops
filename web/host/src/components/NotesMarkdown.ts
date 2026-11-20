import { computed, defineComponent, h, type VNode } from 'vue'
import { parseNotes, type Block, type Inline } from '@/lib/notes'

// The notes of an object as the reader sees them. Every element is made here from the parsed tree (lib/notes): the
// text only ever becomes the text of an element, never markup.

function inline(parts: Inline[]): Array<VNode | string> {
  return parts.map((p) => {
    switch (p.kind) {
      case 'text': return p.text
      case 'br': return h('br')
      case 'code': return h('code', { class: 'notes-code' }, p.text)
      case 'strong': return h('strong', inline(p.children))
      case 'em': return h('em', inline(p.children))
      case 'link': return h('a', { href: p.href, rel: 'noopener noreferrer nofollow', target: '_blank' }, inline(p.children))
    }
  })
}

function block(b: Block): VNode {
  switch (b.kind) {
    // the page has its h1 and the panel its h2: the headings of a note come below them
    case 'heading': return h(`h${b.level + 2}`, { class: 'notes-h' }, inline(b.children))
    case 'para': return h('p', inline(b.children))
    case 'list': return h(b.ordered ? 'ol' : 'ul', b.items.map((it) => h('li', inline(it))))
    case 'code': return h('pre', { class: 'notes-pre', tabindex: 0 }, h('code', b.text))
  }
}

export default defineComponent({
  name: 'NotesMarkdown',
  props: { text: { type: String, default: '' } },
  setup(props) {
    const blocks = computed(() => parseNotes(props.text))
    return () => h('div', { class: 'notes-md' }, blocks.value.map(block))
  },
})
