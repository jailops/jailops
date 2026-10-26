import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import AddrCopy from '@jailops/shared/components/AddrCopy.vue'

describe('AddrCopy', () => {
  it('shows IPv6 compressed with the original as tooltip and a labelled copy button', () => {
    const w = mount(AddrCopy, { props: { value: '2001:0db8:0:0:0:0:0:1/64' } })
    expect(w.find('.mono').text()).toBe('2001:db8::1/64')
    expect(w.find('.mono').attributes('title')).toBe('2001:0db8:0:0:0:0:0:1/64')
    expect(w.find('button').attributes('aria-label')).toBe('Copy 2001:db8::1/64')
  })
  it('has no button for placeholders and shows a dash for nothing', () => {
    expect(mount(AddrCopy, { props: { value: 'auto' } }).find('button').exists()).toBe(false)
    expect(mount(AddrCopy, { props: { value: '' } }).text()).toBe('-')
  })
})
