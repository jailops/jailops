<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { runOp } from '@jailops/shared/composables/ops'
import Modal from '@jailops/shared/components/Modal.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import { blankNet, checkNet, netBody, suggestV4 } from '@/lib/netform'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ close: []; done: [] }>()
const f = reactive(blankNet())
const busy = ref(false)
const error = ref<ApiError | null>(null)
const touched = reactive<Record<string, boolean>>({})
const problems = computed(() => checkNet(f))
const submitted = ref(false)
/** Fields that must be filled; they are only called out once the user tried to create. */
const requiredEmpty = computed<Record<string, boolean>>(() => ({
  name: !f.name, v4subnet: f.v4 && !f.v4subnet, v4gateway: f.v4 && !f.v4gateway, v4range: f.v4 && !f.v4range,
  v6subnet: f.mode !== 'none' && !f.v6subnet, v6gateway: f.mode !== 'none' && !f.v6gateway, v6range: f.mode !== 'none' && !f.v6range,
}))
const msg = (k: string) => (touched[k] || submitted.value ? problems.value[k] : '') || (submitted.value && requiredEmpty.value[k] ? s.networks.required : '')
const ready = computed(() => !Object.keys(problems.value).length && !!f.name && (!f.v4 || (f.v4subnet && f.v4gateway && f.v4range)) && (f.mode === 'none' || (f.v6subnet && f.v6gateway && f.v6range)))

watch(() => props.open, (o) => {
  if (!o) return
  Object.assign(f, blankNet())
  Object.keys(touched).forEach((k) => delete touched[k])
  submitted.value = false
  error.value = null
})
// A typed subnet fills the gateway and range once, until the user has written their own.
watch(() => f.v4subnet, (v) => {
  const sug = suggestV4(v)
  if (sug && !touched.v4gateway && !touched.v4range) Object.assign(f, { v4gateway: sug.gateway, v4range: sug.range })
})
watch(() => f.mode, (m) => { if (m === 'proxy' && !f.v6gateway) f.v6gateway = 'fe80::1' })

async function save(): Promise<void> {
  submitted.value = true
  if (!ready.value) return
  busy.value = true
  error.value = null
  try {
    await runOp(s.networks.createTitle + ': ' + f.name, () => api.networks.create(netBody(f)))
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
  <Modal :open="open" :title="s.networks.createTitle" wide @close="emit('close')">
    <form id="nf" novalidate @submit.prevent="save">
      <div class="grid-2">
        <div class="field">
          <label for="nf-name">{{ s.networks.name }}</label>
          <input id="nf-name" v-model="f.name" class="mono" type="text" autocomplete="off" required :aria-invalid="!!msg('name')" aria-describedby="nf-name-e" @blur="touched.name = true" />
          <span id="nf-name-e" class="field-error" role="alert">{{ msg('name') }}</span>
        </div>
        <div class="field">
          <label for="nf-up">{{ s.networks.uplink }}</label>
          <input id="nf-up" v-model="f.uplink" class="mono" type="text" placeholder="em0" autocomplete="off" :aria-invalid="!!msg('uplink')" aria-describedby="nf-up-e nf-up-h" @blur="touched.uplink = true" />
          <span id="nf-up-h" class="hint">{{ s.networks.uplinkHint }}</span>
          <span id="nf-up-e" class="field-error" role="alert">{{ msg('uplink') }}</span>
        </div>
      </div>
      <p v-if="problems.family" class="field-error" role="alert">{{ problems.family }}</p>

      <fieldset class="nf-set">
        <legend><label class="check"><input v-model="f.v4" type="checkbox" data-test="nf-v4" />{{ s.networks.ipv4 }}</label></legend>
        <template v-if="f.v4">
          <div class="field">
            <label for="nf-v4s">{{ s.networks.subnet }}</label>
            <input id="nf-v4s" v-model="f.v4subnet" class="mono" type="text" placeholder="10.20.0.0/24" autocomplete="off" :aria-invalid="!!msg('v4subnet')" aria-describedby="nf-v4s-e" @blur="touched.v4subnet = true" />
            <span id="nf-v4s-e" class="field-error" role="alert">{{ msg('v4subnet') }}</span>
          </div>
          <div class="grid-2">
            <div class="field">
              <label for="nf-v4g">{{ s.networks.gateway }}</label>
              <input id="nf-v4g" v-model="f.v4gateway" class="mono" type="text" placeholder="10.20.0.1" autocomplete="off" :aria-invalid="!!msg('v4gateway')" aria-describedby="nf-v4g-e" @input="touched.v4gateway = true" />
              <span id="nf-v4g-e" class="field-error" role="alert">{{ msg('v4gateway') }}</span>
            </div>
            <div class="field">
              <label for="nf-v4r">{{ s.networks.range }}</label>
              <input id="nf-v4r" v-model="f.v4range" class="mono" type="text" placeholder="10.20.0.10-10.20.0.200" autocomplete="off" :aria-invalid="!!msg('v4range')" aria-describedby="nf-v4r-e" @input="touched.v4range = true" />
              <span id="nf-v4r-e" class="field-error" role="alert">{{ msg('v4range') }}</span>
            </div>
          </div>
          <label class="check"><input v-model="f.nat" type="checkbox" />{{ s.networks.natHint }}</label>
        </template>
      </fieldset>

      <fieldset class="nf-set">
        <legend>{{ s.networks.ipv6 }}</legend>
        <div class="field">
          <label for="nf-mode">{{ s.networks.mode }}</label>
          <select id="nf-mode" v-model="f.mode" data-test="nf-mode">
            <option value="none">{{ s.networks.modeNone }}</option>
            <option value="routed">{{ s.networks.modeRouted }}</option>
            <option value="proxy">{{ s.networks.modeProxy }}</option>
          </select>
        </div>
        <Callout v-if="f.mode === 'routed'" tone="info" data-test="mode-help">{{ s.networks.routedHelp }}</Callout>
        <Callout v-if="f.mode === 'proxy'" tone="warn" data-test="mode-help">{{ s.networks.proxyHelp }}<br />{{ s.networks.pfNote }}</Callout>
        <template v-if="f.mode !== 'none'">
          <div class="field">
            <label for="nf-v6s">{{ s.networks.subnet }}</label>
            <input id="nf-v6s" v-model="f.v6subnet" class="mono" type="text" placeholder="2001:db8:20:1::/64" autocomplete="off" :aria-invalid="!!msg('v6subnet')" aria-describedby="nf-v6s-e" @blur="touched.v6subnet = true" />
            <span id="nf-v6s-e" class="field-error" role="alert">{{ msg('v6subnet') }}</span>
          </div>
          <div class="grid-2">
            <div class="field">
              <label for="nf-v6g">{{ s.networks.gateway }}</label>
              <input id="nf-v6g" v-model="f.v6gateway" class="mono" type="text" :placeholder="f.mode === 'proxy' ? 'fe80::1' : '2001:db8:20:1::1'" autocomplete="off" :aria-invalid="!!msg('v6gateway')" aria-describedby="nf-v6g-e" @blur="touched.v6gateway = true" />
              <span id="nf-v6g-e" class="field-error" role="alert">{{ msg('v6gateway') }}</span>
            </div>
            <div class="field">
              <label for="nf-v6r">{{ s.networks.range }}</label>
              <input id="nf-v6r" v-model="f.v6range" class="mono" type="text" placeholder="2001:db8:20:1::100-2001:db8:20:1::1ff" autocomplete="off" :aria-invalid="!!msg('v6range')" aria-describedby="nf-v6r-e" @blur="touched.v6range = true" />
              <span id="nf-v6r-e" class="field-error" role="alert">{{ msg('v6range') }}</span>
            </div>
          </div>
        </template>
      </fieldset>
      <p class="hint">{{ s.networks.serverChecks }}</p>
      <ErrorPanel v-if="error" :error="error" />
    </form>
    <template #footer>
      <button class="btn" type="button" @click="emit('close')">{{ s.app.cancel }}</button>
      <button class="btn btn-primary" type="submit" form="nf" :disabled="busy" data-test="net-create-go">{{ s.networks.create }}</button>
    </template>
  </Modal>
</template>

<style>
.nf-set { border: 1px solid var(--border); border-radius: var(--radius); margin: 12px 0; padding: 8px 12px 12px; }
.nf-set legend { padding: 0 6px; font-weight: 600; }
.field-error:empty { display: none; }
</style>
