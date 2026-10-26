<script setup lang="ts">
import { common as s } from '@jailops/shared/i18n/common'
import type { ApiError } from '@jailops/shared/api/client'
import ErrorPanel from './ErrorPanel.vue'

defineProps<{ loading: boolean; error: ApiError | null; hasData: boolean }>()
defineEmits<{ retry: [] }>()
</script>

<template>
  <div>
    <div v-if="error" class="stack">
      <ErrorPanel :error="error" />
      <div><button class="btn" type="button" @click="$emit('retry')">{{ s.app.retry }}</button></div>
    </div>
    <p v-if="loading && !hasData" class="muted" role="status">{{ s.app.loading }}</p>
    <slot v-if="hasData" />
  </div>
</template>
