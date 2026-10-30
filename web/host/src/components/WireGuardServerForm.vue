<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { runOp } from '@jailops/shared/composables/ops'
import Modal from '@jailops/shared/components/Modal.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import { blankServer, checkServer, serverBody } from '@/lib/wireguard'

const props = defineProps<{ open: boolean; networks: string[] }>()
const emit = defineEmits<{ close: []; done: [] }>()
const f = reactive(blankServer())
const busy = ref(false)
const error = ref<ApiError | null>(null)
const submitted = ref(false)
const problems = computed(() => checkServer(f))
const msg = (k: string): string => {
  if (submitted.value && ((k === 'name' && !f.name) || (k === 'address' && !f.address))) return s.wireguard.required
  return problems.value[k] ?? ''
}
const ready = computed(() => !!f.name && !!f.address && !Object.keys(problems.value).length)

watch(() => props.open, (o) => {
  if (!o) return
  Object.assign(f, blankServer())
  submitted.value = false
  error.value = null
})

async function save(): Promise<void> {
  submitted.value = true
  if (!ready.value) return
  busy.value = true
  error.value = null
  try {
    await runOp(s.wireguard.createTitle + ': ' + f.name, () => api.wireguard.create(serverBody(f)))
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
  <Modal :open="open" :title="s.wireguard.createTitle" wide @close="emit('close')">
    <form id="wgf" novalidate @submit.prevent="save">
      <div class="grid-2">
        <div class="field">
          <label for="wgf-name">{{ s.wireguard.name }}</label>
          <input id="wgf-name" v-model="f.name" class="mono" type="text" autocomplete="off" required :aria-invalid="!!msg('name')" aria-describedby="wgf-name-h wgf-name-e" />
          <span id="wgf-name-h" class="hint">{{ s.wireguard.nameHint }}</span>
          <span id="wgf-name-e" class="field-error" role="alert">{{ msg('name') }}</span>
        </div>
        <div class="field">
          <label for="wgf-port">{{ s.wireguard.listenPort }}</label>
          <input id="wgf-port" v-model="f.port" class="mono" type="text" inputmode="numeric" autocomplete="off" :aria-invalid="!!msg('port')" aria-describedby="wgf-port-e" />
          <span id="wgf-port-e" class="field-error" role="alert">{{ msg('port') }}</span>
        </div>
      </div>
      <div class="field">
        <label for="wgf-addr">{{ s.wireguard.address }}</label>
        <input id="wgf-addr" v-model="f.address" class="mono" type="text" placeholder="10.99.0.1/24" autocomplete="off" required :aria-invalid="!!msg('address')" aria-describedby="wgf-addr-h wgf-addr-e" />
        <span id="wgf-addr-h" class="hint">{{ s.wireguard.addressHint }}</span>
        <span id="wgf-addr-e" class="field-error" role="alert">{{ msg('address') }}</span>
      </div>
      <div class="field">
        <label for="wgf-ep">{{ s.wireguard.endpoint }}</label>
        <input id="wgf-ep" v-model="f.endpoint" class="mono" type="text" placeholder="vpn.example.com:51820" autocomplete="off" :aria-invalid="!!msg('endpoint')" aria-describedby="wgf-ep-h wgf-ep-e" />
        <span id="wgf-ep-h" class="hint">{{ s.wireguard.endpointHint }}</span>
        <span id="wgf-ep-e" class="field-error" role="alert">{{ msg('endpoint') }}</span>
      </div>
      <fieldset v-if="networks.length" class="wgf-set">
        <legend>{{ s.wireguard.networks }}</legend>
        <label v-for="n in networks" :key="n" class="check mono"><input v-model="f.networks" type="checkbox" :value="n" :data-wg-net="n" />{{ n }}</label>
        <p class="hint">{{ s.wireguard.networksHint }}</p>
      </fieldset>
      <div class="field">
        <label for="wgf-dns">{{ s.wireguard.dns }}</label>
        <input id="wgf-dns" v-model="f.dns" class="mono" type="text" autocomplete="off" :aria-invalid="!!msg('dns')" aria-describedby="wgf-dns-h wgf-dns-e" />
        <span id="wgf-dns-h" class="hint">{{ s.wireguard.dnsHint }}</span>
        <span id="wgf-dns-e" class="field-error" role="alert">{{ msg('dns') }}</span>
      </div>
      <ErrorPanel v-if="error" :error="error" style="margin-top: 12px" />
    </form>
    <template #footer>
      <button class="btn" type="button" @click="emit('close')">{{ s.app.cancel }}</button>
      <button class="btn btn-primary" type="submit" form="wgf" :disabled="busy" data-test="wg-create-go">{{ s.wireguard.createGo }}</button>
    </template>
  </Modal>
</template>

<style scoped>
.wgf-set { border: 1px solid var(--border); border-radius: var(--radius); padding: 8px 12px; margin: 0 0 12px; display: flex; flex-wrap: wrap; gap: 4px 16px; }
.wgf-set legend { padding: 0 4px; color: var(--text-2); }
.wgf-set .hint { flex-basis: 100%; margin: 4px 0 0; }
</style>
