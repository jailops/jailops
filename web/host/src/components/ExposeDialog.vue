<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { useFormCheck } from '@jailops/shared/composables/form'
import Modal from '@jailops/shared/components/Modal.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import type { ExposeRequest, ExposeResult, Jail } from '@jailops/shared/api/types'

// Expose ports of a jail to the Internet (jailops expose): IPv6 on the jail's own address, IPv4 through a forward of the host.
// The same request as the command; Preview sends it with dry_run and shows what would be reached.
const props = defineProps<{ open: boolean; jail: Jail }>()
const emit = defineEmits<{ close: []; done: [] }>()

const f = reactive({ ports: '', proto: 'tcp', limited: false, from: '', v4: true, v6: true, hostPort: '', comment: '' })
const busy = ref(false)
const error = ref<ApiError | null>(null)
const result = ref<ExposeResult | null>(null)
const applied = ref(false)

const addresses = computed(() => props.jail.status?.addresses ?? [])
// A jail that does not run has no address yet: its networks say which families it will have.
const wants = (k: 'ipv4' | 'ipv6'): boolean => (props.jail.spec.network ?? []).some((n) => !!n[k] && n[k] !== 'none')
const hasV4 = computed(() => addresses.value.some((a) => !a.includes(':')) || wants('ipv4'))
const hasV6 = computed(() => addresses.value.some((a) => a.includes(':')) || wants('ipv6'))
const portList = computed(() => f.ports.split(',').map((x) => x.trim()).filter(Boolean))
const portOk = (p: string): boolean => {
  const m = /^(\d{1,5})(?:-(\d{1,5}))?$/.exec(p)
  if (!m) return false
  const a = Number(m[1])
  const b = m[2] === undefined ? a : Number(m[2])
  return a >= 1 && b <= 65535 && a <= b
}
const fromList = computed(() => f.from.split(',').map((x) => x.trim()).filter(Boolean))
const hostPortNum = computed(() => (f.hostPort.trim() === '' ? 0 : Number(f.hostPort)))
const check = useFormCheck(() => ({
  'ex-ports': !portList.value.length || !portList.value.every(portOk) ? s.expose.issue.ports : '',
  'ex-from': f.limited && !fromList.value.length ? s.expose.issue.from : '',
  'ex-families': f.v4 || f.v6 ? '' : s.expose.issue.families,
  'ex-hostport': f.hostPort.trim() === '' ? '' : !Number.isInteger(hostPortNum.value) || hostPortNum.value < 1 || hostPortNum.value > 65535 ? s.expose.issue.hostPort : portList.value.length !== 1 || portList.value[0].includes('-') ? s.expose.issue.hostPortMany : '',
}))

watch(() => props.open, (o) => {
  Object.assign(f, { ports: '', proto: 'tcp', limited: false, from: '', v4: hasV4.value, v6: hasV6.value, hostPort: '', comment: '' })
  error.value = null
  result.value = null
  applied.value = false
  check.reset()
  void o
}, { immediate: true })
watch(() => [f.ports, f.proto, f.limited, f.from, f.v4, f.v6, f.hostPort, f.comment], () => { if (!applied.value) result.value = null })

const body = (dry: boolean): ExposeRequest => ({
  ports: portList.value,
  ...(f.proto !== 'tcp' ? { proto: f.proto } : {}),
  ...(f.limited ? { from: fromList.value } : {}),
  ...(f.v4 && f.v6 ? {} : { families: [f.v4 ? '4' : '6'] }),
  ...(f.v4 && hostPortNum.value ? { host_port: hostPortNum.value } : {}),
  ...(f.comment.trim() ? { comment: f.comment.trim() } : {}),
  ...(dry ? { dry_run: true } : {}),
})

const send = (dry: boolean) => check.submit(async () => {
  busy.value = true
  error.value = null
  try {
    const r = await api.reach.expose(props.jail.metadata.name, body(dry))
    result.value = r.kind === 'sync' ? (r.data as ExposeResult) : null
    if (!dry) {
      applied.value = true
      emit('done')
    }
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
})

const line = (x: ExposeResult['families'][number]): string =>
  x.via === 'uplink' ? s.expose.uplink(x.address, x.ports) : x.via === 'forward' ? s.expose.forward(x.address, (x.forwards ?? []).join(', ') || x.ports) : s.expose.noneVia(x.address, x.note ?? '')
</script>

<template>
  <Modal :open="open" :title="s.expose.title(jail.metadata.name)" @close="emit('close')">
    <form id="expose-form" novalidate @submit.prevent="send(false)">
      <p class="muted">{{ s.expose.lead }}</p>
      <div class="field">
        <label for="ex-ports">{{ s.expose.ports }} <span class="req" aria-hidden="true">*</span></label>
        <input id="ex-ports" v-model="f.ports" class="mono" type="text" placeholder="80,443" autocomplete="off" data-test="expose-ports" aria-describedby="ex-ports-h ex-ports-e" :aria-invalid="!!check.err('ex-ports')" :disabled="applied" @blur="check.touch('ex-ports')" />
        <span id="ex-ports-h" class="hint">{{ s.expose.portsHint }}</span>
        <span id="ex-ports-e" class="field-error" role="alert">{{ check.err('ex-ports') }}</span>
      </div>
      <div class="field">
        <label for="ex-proto">{{ s.expose.proto }}</label>
        <select id="ex-proto" v-model="f.proto" data-test="expose-proto" :disabled="applied">
          <option value="tcp">tcp</option>
          <option value="udp">udp</option>
          <option value="tcp,udp">tcp,udp</option>
        </select>
      </div>
      <fieldset class="field" :disabled="applied">
        <legend>{{ s.expose.families }}</legend>
        <label class="check"><input v-model="f.v6" type="checkbox" data-test="expose-v6" :disabled="!hasV6 || applied" /> {{ s.expose.v6 }}<span v-if="!hasV6" class="hint"> ({{ s.expose.noV6 }})</span></label>
        <label class="check"><input v-model="f.v4" type="checkbox" data-test="expose-v4" :disabled="!hasV4 || applied" /> {{ s.expose.v4 }}<span v-if="!hasV4" class="hint"> ({{ s.expose.noV4 }})</span></label>
        <span id="ex-families" class="field-error" role="alert">{{ check.err('ex-families') }}</span>
      </fieldset>
      <fieldset class="field" :disabled="applied">
        <legend>{{ s.expose.from }}</legend>
        <label class="check"><input v-model="f.limited" type="radio" :value="false" data-test="expose-anyone" /> {{ s.expose.anyone }}</label>
        <label class="check"><input v-model="f.limited" type="radio" :value="true" data-test="expose-limited" /> {{ s.expose.limited }}</label>
        <template v-if="f.limited">
          <input id="ex-from" v-model="f.from" class="mono" type="text" placeholder="set:office, 192.0.2.0/24" autocomplete="off" data-test="expose-from" aria-describedby="ex-from-h ex-from-e" :aria-invalid="!!check.err('ex-from')" :aria-label="s.expose.from" @blur="check.touch('ex-from')" />
          <span id="ex-from-h" class="hint">{{ s.expose.fromHint }}</span>
          <span id="ex-from-e" class="field-error" role="alert">{{ check.err('ex-from') }}</span>
        </template>
      </fieldset>
      <div v-if="f.v4" class="field">
        <label for="ex-hostport">{{ s.expose.hostPort }}</label>
        <input id="ex-hostport" v-model="f.hostPort" class="mono" type="text" inputmode="numeric" placeholder="" autocomplete="off" data-test="expose-hostport" aria-describedby="ex-hostport-h ex-hostport-e" :aria-invalid="!!check.err('ex-hostport')" :disabled="applied" @blur="check.touch('ex-hostport')" />
        <span id="ex-hostport-h" class="hint">{{ s.expose.hostPortHint }}</span>
        <span id="ex-hostport-e" class="field-error" role="alert">{{ check.err('ex-hostport') }}</span>
      </div>
      <div class="field">
        <label for="ex-comment">{{ s.expose.comment }}</label>
        <input id="ex-comment" v-model="f.comment" type="text" autocomplete="off" data-test="expose-comment" :disabled="applied" />
      </div>
      <ErrorPanel v-if="error" :error="error" style="margin-top: 12px" />
      <div v-if="result" class="stack" style="margin-top: 12px" data-test="expose-result">
        <h3>{{ s.expose.result }}</h3>
        <p v-if="!result.changed" class="muted">{{ s.expose.resultUnchanged }}</p>
        <ul class="plain">
          <li v-for="x in result.families" :key="x.family + x.address" class="mono" :data-family="x.family">{{ line(x) }}</li>
        </ul>
        <Callout v-for="w in result.warnings ?? []" :key="w" tone="warn">{{ w }}</Callout>
      </div>
    </form>
    <template #footer>
      <span v-if="!applied && check.reason.value" class="why-disabled">{{ check.reason.value }}</span>
      <button class="btn" type="button" data-test="expose-close" @click="emit('close')">{{ applied ? s.expose.close : s.app.cancel }}</button>
      <template v-if="!applied">
        <button class="btn" type="button" :disabled="busy" :aria-disabled="!check.ok.value" data-test="expose-preview" @click="send(true)">{{ s.expose.preview }}</button>
        <button class="btn btn-primary" type="submit" form="expose-form" :disabled="busy" :aria-disabled="!check.ok.value" data-test="expose-go">{{ s.expose.apply }}</button>
      </template>
    </template>
  </Modal>
</template>
