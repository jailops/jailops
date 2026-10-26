<script setup lang="ts">
import { ref, watch } from 'vue'
import { common as s } from '@jailops/shared/i18n/common'
import { ApiError } from '@jailops/shared/api/client'
import Modal from './Modal.vue'
import ErrorPanel from './ErrorPanel.vue'

const props = defineProps<{ open: boolean; title: string; confirmLabel: string; danger?: boolean; run: () => Promise<unknown> }>()
const emit = defineEmits<{ close: []; done: [] }>()
const busy = ref(false)
const error = ref<ApiError | null>(null)
watch(() => props.open, (o) => o && (error.value = null))

async function go(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    await props.run()
    emit('done')
    emit('close')
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal :open="open" :title="title" @close="emit('close')">
    <slot />
    <ErrorPanel v-if="error" :error="error" style="margin-top: 12px" />
    <template #footer>
      <button class="btn" type="button" @click="emit('close')">{{ s.app.cancel }}</button>
      <button class="btn" :class="danger ? 'btn-danger' : 'btn-primary'" type="button" :disabled="busy" data-test="confirm" @click="go">{{ confirmLabel }}</button>
    </template>
  </Modal>
</template>
