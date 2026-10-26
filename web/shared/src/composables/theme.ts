import { ref } from 'vue'

export type Theme = 'system' | 'light' | 'dark'
const KEY = 'jailops.theme'
export const theme = ref<Theme>('system')

export function applyTheme(t: Theme): void {
  theme.value = t
  const root = document.documentElement
  if (t === 'system') root.removeAttribute('data-theme')
  else root.setAttribute('data-theme', t)
  try {
    if (t === 'system') localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, t)
  } catch {
    /* storage blocked: the choice lasts for this page only */
  }
}

export function initTheme(): void {
  let t: Theme = 'system'
  try {
    const v = localStorage.getItem(KEY)
    if (v === 'light' || v === 'dark') t = v
  } catch {
    /* ignore */
  }
  applyTheme(t)
}

export function cycleTheme(): void {
  applyTheme(theme.value === 'system' ? 'light' : theme.value === 'light' ? 'dark' : 'system')
}
