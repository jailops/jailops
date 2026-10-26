<script setup lang="ts">
import Icon from './Icon.vue'
import type { IconName } from '@jailops/shared/lib/icons'

const props = withDefaults(defineProps<{ tone?: 'info' | 'warn' | 'danger' | 'ok' | '' }>(), { tone: '' })
const icon: Record<string, IconName> = { info: 'info', warn: 'alertTriangle', danger: 'alertCircle', ok: 'checkCircle', '': 'info' }
</script>

<template>
  <div class="callout" :class="props.tone" :role="props.tone === 'danger' ? 'alert' : undefined">
    <Icon :name="icon[props.tone]" />
    <div class="grow"><slot /></div>
  </div>
</template>
