<script setup lang="ts">
import { ref } from 'vue'
import { common as s } from '@jailops/shared/i18n/common'
import Icon from './Icon.vue'

const props = defineProps<{ value: string; label: string }>()
const done = ref(false)

async function copy(): Promise<void> {
  try {
    await navigator.clipboard.writeText(props.value)
  } catch {
    const el = document.getElementById('copy-' + props.label)
    if (el instanceof HTMLInputElement) {
      el.select()
      document.execCommand('copy')
    }
  }
  done.value = true
  setTimeout(() => (done.value = false), 2000)
}
</script>

<template>
  <div class="row" style="flex-wrap: nowrap">
    <input :id="'copy-' + label" class="mono grow" type="text" readonly :value="value" :aria-label="label" @focus="($event.target as HTMLInputElement).select()" />
    <button class="btn" type="button" @click="copy"><Icon :name="done ? 'check' : 'copy'" />{{ done ? s.app.copied : s.app.copy }}</button>
    <span class="sr-only" role="status">{{ done ? s.app.copied : '' }}</span>
  </div>
</template>
