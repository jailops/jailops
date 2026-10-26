<script setup lang="ts">
import { computed, ref } from 'vue'
import { common as s } from '../i18n/common'
import { shortAddr } from '../lib/format'
import Icon from './Icon.vue'

// An address or range in its shortest form with a copy button. The copy button copies the shown text (CIDR prefix
// included), the tooltip shows the original when it was shortened. Placeholders such as "auto" and "none" get no button.
const props = defineProps<{ value: string | undefined | null }>()
const done = ref(false)
const text = computed(() => shortAddr(props.value))
const copyable = computed(() => !!props.value && /[0-9.:]/.test(props.value) && props.value !== 'auto' && props.value !== 'none')

async function copy(): Promise<void> {
  try {
    await navigator.clipboard.writeText(text.value)
    done.value = true
    setTimeout(() => (done.value = false), 1500)
  } catch {
    /* clipboard unavailable (insecure origin): the text stays selectable */
  }
}
</script>

<template>
  <span v-if="!value" class="faint">-</span>
  <span v-else class="addr">
    <span class="mono" :title="text !== value ? value : undefined">{{ text }}</span>
    <button v-if="copyable" class="btn btn-ghost btn-icon addr-copy" type="button" :aria-label="`${s.app.copy} ${text}`" :title="s.app.copy" @click="copy"><Icon :name="done ? 'check' : 'copy'" /></button>
    <span class="sr-only" role="status">{{ done ? s.app.copied : '' }}</span>
  </span>
</template>

<style>
.addr { display: inline-flex; align-items: center; gap: 2px; flex-wrap: nowrap; }
.addr-copy { padding: 2px 4px; min-height: 0; opacity: 0.7; }
.addr-copy:hover, .addr-copy:focus-visible { opacity: 1; }
.addr-copy svg { width: 14px; height: 14px; }
</style>
