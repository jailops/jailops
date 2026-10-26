<script setup lang="ts">
import { computed } from 'vue'
import { s } from '@/i18n'
import Callout from '@jailops/shared/components/Callout.vue'

const props = defineProps<{ names: string[]; projects: string[]; global: boolean }>()
const emit = defineEmits<{ 'update:projects': [string[]]; 'update:global': [boolean] }>()
const ok = computed(() => props.global || props.projects.length > 0)
function toggle(n: string, on: boolean): void {
  emit('update:projects', on ? [...props.projects, n] : props.projects.filter((x) => x !== n))
}
defineExpose({ ok })
</script>

<template>
  <fieldset class="field scope-box" data-test="scope">
    <legend class="label">{{ s.trust.projects }}</legend>
    <label v-for="p in names" :key="p" class="check"><input type="checkbox" :checked="projects.includes(p)" :disabled="global" @change="toggle(p, ($event.target as HTMLInputElement).checked)" /><span class="mono">{{ p }}</span></label>
    <label class="check"><input type="checkbox" :checked="global" data-test="global-toggle" @change="emit('update:global', ($event.target as HTMLInputElement).checked)" />{{ s.trust.global }}</label>
    <Callout v-if="global" tone="warn" data-test="global-warning">{{ s.trust.globalWarning }}</Callout>
    <span v-if="!ok" class="err" data-test="scope-required">{{ s.trust.scopeRequired }}</span>
  </fieldset>
</template>
