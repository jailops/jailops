<script setup lang="ts">
import { computed } from 'vue'
import { common as s } from '@jailops/shared/i18n/common'
import Callout from './Callout.vue'

const props = defineProps<{ error: { code: string; message: string; hint?: string; detail?: Record<string, string> } }>()
const detail = computed(() => Object.entries(props.error.detail ?? {}))
</script>

<template>
  <Callout tone="danger">
    <div data-test="error-panel" role="alert">
      <p><strong>{{ error.message }}</strong></p>
      <p v-if="error.hint" data-test="error-hint">{{ s.error.hint }}: {{ error.hint }}</p>
      <!-- the stable code is for support and scripts, so it is one click away rather than the headline -->
      <details class="error-tech">
        <summary>{{ s.error.technical }}</summary>
        <dl class="kv">
          <dt>{{ s.error.code }}</dt>
          <dd class="mono" data-test="error-code">{{ error.code }}</dd>
          <template v-for="[k, v] in detail" :key="k">
            <dt>{{ k }}</dt>
            <dd class="mono">{{ v }}</dd>
          </template>
        </dl>
      </details>
    </div>
  </Callout>
</template>

<style>
.error-tech { margin-top: 6px; }
.error-tech summary { cursor: pointer; font-size: 12px; }
</style>
