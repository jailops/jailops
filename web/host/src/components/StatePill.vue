<script setup lang="ts">
import { computed } from 'vue'
import { s } from '@/i18n'
import type { PowerState } from '@jailops/shared/api/types'

const props = defineProps<{ state: PowerState | string }>()
const tone = computed(() => ({ running: 'ok', stopped: '', starting: 'info pulse', stopping: 'warn pulse', error: 'danger' })[props.state] ?? '')
const label = computed(() => (s.state as Record<string, string>)[props.state] ?? props.state)
</script>

<template>
  <span class="pill" :class="tone" :data-state="state">{{ label }}</span>
</template>
