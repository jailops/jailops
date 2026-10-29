<script setup lang="ts">
import { computed } from 'vue'
import { monogram } from '@/lib/apps'

// The icon of a definition (a data URI the server read from the catalog) or a monogram. Decorative: the
// title is always next to it.
const props = withDefaults(defineProps<{ name: string; title?: string; icon?: string; size?: number }>(), { title: '', icon: '', size: 40 })
const mono = computed(() => monogram(props.name, props.title))
const safe = computed(() => /^data:image\/(png|svg\+xml);base64,[A-Za-z0-9+/=]+$/.test(props.icon) ? props.icon : '')
</script>

<template>
  <span class="app-icon" :style="{ '--s': size + 'px', '--h': mono.hue }" aria-hidden="true">
    <img v-if="safe" :src="safe" alt="" :width="size" :height="size" />
    <span v-else class="app-mono">{{ mono.text }}</span>
  </span>
</template>

<style>
.app-icon { flex: none; display: inline-grid; place-items: center; width: var(--s); height: var(--s); border-radius: calc(var(--s) / 4.5); overflow: hidden; background: var(--surface-2); border: 1px solid var(--border); }
.app-icon img { width: 78%; height: 78%; object-fit: contain; }
.app-mono { width: 100%; height: 100%; display: grid; place-items: center; font-weight: 600; font-size: calc(var(--s) * 0.38); letter-spacing: 0.02em; color: hsl(var(--h) 60% 24%); background-color: hsl(var(--h) 70% 90%); background-image: linear-gradient(135deg, hsl(var(--h) 70% 93%), hsl(calc(var(--h) + 40) 60% 85%)); }
@media (prefers-color-scheme: dark) { :root:not([data-theme='light']) .app-mono { color: hsl(var(--h) 70% 86%); background-color: hsl(var(--h) 45% 20%); background-image: linear-gradient(135deg, hsl(var(--h) 45% 24%), hsl(calc(var(--h) + 40) 40% 18%)); } }
:root[data-theme='dark'] .app-mono { color: hsl(var(--h) 70% 86%); background-color: hsl(var(--h) 45% 20%); background-image: linear-gradient(135deg, hsl(var(--h) 45% 24%), hsl(calc(var(--h) + 40) 40% 18%)); }
</style>
