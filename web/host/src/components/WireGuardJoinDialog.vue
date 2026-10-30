<script setup lang="ts">
// Makes a client tunnel from a token. The token is only in this form while it is open and is cleared on close.
import { computed, ref, watch } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { runOp } from '@jailops/shared/composables/ops'
import Modal from '@jailops/shared/components/Modal.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import { looksLikeToken } from '@/lib/wireguard'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ close: []; done: [] }>()
const token = ref('')
const name = ref('')
const submitted = ref(false)
const busy = ref(false)
const error = ref<ApiError | null>(null)
const tokenError = computed(() => (submitted.value && !token.value.trim() ? s.wireguard.required : token.value.trim() && !looksLikeToken(token.value) ? s.wireguard.tokenInvalid : ''))

function reset(): void {
  token.value = ''
  name.value = ''
  submitted.value = false
  error.value = null
}
watch(() => props.open, reset)

async function save(): Promise<void> {
  submitted.value = true
  if (!token.value.trim() || tokenError.value) return
  busy.value = true
  error.value = null
  try {
    await runOp(s.wireguard.joinTitle, () => api.wireguard.join({ token: token.value.trim(), ...(name.value.trim() ? { name: name.value.trim() } : {}) }))
    emit('done')
    close()
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
function close(): void {
  reset()
  emit('close')
}
</script>

<template>
  <Modal :open="open" :title="s.wireguard.joinTitle" wide @close="close">
    <form id="wgj" novalidate @submit.prevent="save">
      <div class="field">
        <label for="wgj-token">{{ s.wireguard.token }}</label>
        <textarea id="wgj-token" v-model="token" class="mono" rows="4" autocomplete="off" spellcheck="false" required :aria-invalid="!!tokenError" aria-describedby="wgj-token-h wgj-token-e"></textarea>
        <span id="wgj-token-h" class="hint">{{ s.wireguard.tokenHint }}</span>
        <span id="wgj-token-e" class="field-error" role="alert">{{ tokenError }}</span>
      </div>
      <div class="field">
        <label for="wgj-name">{{ s.wireguard.joinName }}</label>
        <input id="wgj-name" v-model="name" class="mono" type="text" autocomplete="off" aria-describedby="wgj-name-h" />
        <span id="wgj-name-h" class="hint">{{ s.wireguard.joinNameHint }}</span>
      </div>
      <ErrorPanel v-if="error" :error="error" style="margin-top: 12px" />
    </form>
    <template #footer>
      <button class="btn" type="button" @click="close">{{ s.app.cancel }}</button>
      <button class="btn btn-primary" type="submit" form="wgj" :disabled="busy" data-test="wg-join-go">{{ s.wireguard.joinGo }}</button>
    </template>
  </Modal>
</template>
