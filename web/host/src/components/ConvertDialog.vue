<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { runOp } from '@jailops/shared/composables/ops'
import type { Image, Jail, JailConvertResult, Layout } from '@jailops/shared/api/types'
import Modal from '@jailops/shared/components/Modal.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'

// Change the layout of a FreeBSD jail's root (extension jail_convert): thin to thick makes the root a full copy of its own
// with the persistent datasets folded in; thick to thin gives the jail a clone of an image generation. The preview is a
// dry run on the server: what the plan keeps and what it replaces. A Linux jail is thick and stays so.
const props = defineProps<{ open: boolean; jail: Jail }>()
const emit = defineEmits<{ close: []; done: [] }>()

const images = ref<Image[]>([])
const image = ref('')
const preview = ref<JailConvertResult | null>(null)
const previewFor = ref('')
const busy = ref(false)
const error = ref<ApiError | null>(null)

const from = computed<Layout>(() => props.jail.spec.layout || 'thin')
const to = computed<Layout>(() => (from.value === 'thin' ? 'thick' : 'thin'))
const imageName = computed(() => props.jail.spec.image.split(/[@#]/)[0])
const gens = computed(() => images.value.find((i) => i.metadata.name === imageName.value)?.status.generations ?? [])
const key = computed(() => `${to.value}/${image.value}`)
const fresh = computed(() => !!preview.value && previewFor.value === key.value)

watch(() => props.open, async (o) => {
  if (!o) return
  image.value = ''
  preview.value = null
  previewFor.value = ''
  error.value = null
  if (from.value === 'thin') return
  try {
    images.value = await api.images.list()
  } catch (e) {
    error.value = e instanceof ApiError ? e : null
  }
}, { immediate: true })

const body = (dry: boolean) => ({ layout: to.value, ...(image.value ? { image: image.value } : {}), ...(dry ? { dry_run: true } : {}) })

async function dry(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    const r = await runOp(s.convert.title(props.jail.metadata.name), () => api.jails.convert(props.jail.metadata.name, body(true)), { silent: true })
    preview.value = (r.data ?? r.op?.metadata) as JailConvertResult
    previewFor.value = key.value
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}

async function go(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    await runOp(s.convert.op(props.jail.metadata.name, to.value), () => api.jails.convert(props.jail.metadata.name, body(false)))
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
  <Modal :open="open" :title="s.convert.title(jail.metadata.name)" wide @close="emit('close')">
    <div class="stack" data-test="convert-form">
      <p class="muted" style="margin: 0" data-test="convert-lead">{{ from === 'thin' ? s.convert.leadThin : s.convert.leadThick }}</p>
      <p style="margin: 0"><strong class="mono" data-test="convert-fromto">{{ s.convert.fromTo(from, to) }}</strong></p>
      <div v-if="from === 'thick'" class="field">
        <label for="cv-image">{{ s.convert.image }}</label>
        <select id="cv-image" v-model="image" data-test="convert-image">
          <option value="">{{ s.convert.imageRecorded }}</option>
          <option v-for="g in gens" :key="g.number" :value="g.name">{{ g.name }}</option>
        </select>
      </div>
      <div class="row"><button class="btn" type="button" :disabled="busy" data-test="convert-preview" @click="dry">{{ busy && !fresh ? s.upgrade.previewing : s.upgrade.preview }}</button></div>
      <div v-if="preview" class="stack" data-test="convert-result">
        <p v-if="!fresh" class="muted">{{ s.convert.stale }}</p>
        <template v-if="preview.changed">
          <p v-if="preview.kept?.length" style="margin: 0"><strong>{{ s.convert.keeps }}</strong> <span class="mono">{{ preview.kept.join(', ') }}</span></p>
          <p v-if="preview.replaced" style="margin: 0"><strong>{{ s.convert.replaces }}</strong> {{ preview.replaced }}</p>
          <div v-if="preview.steps?.length">
            <h3>{{ s.upgrade.plan }}</h3>
            <ol class="steps"><li v-for="st in preview.steps" :key="st">{{ st }}</li></ol>
          </div>
          <Callout v-if="preview.warnings?.length" tone="warn">
            <strong>{{ s.upgrade.warnings }}</strong>
            <ul><li v-for="w in preview.warnings" :key="w">{{ w }}</li></ul>
          </Callout>
        </template>
        <p v-else class="muted">{{ s.convert.nothing(preview.to) }}</p>
      </div>
      <p v-else class="muted">{{ s.upgrade.needPreview }}</p>
      <p v-if="jail.status.power === 'running'" class="muted" data-test="convert-running">{{ s.convert.running }}</p>
      <ErrorPanel v-if="error" :error="error" />
    </div>
    <template #footer>
      <span v-if="!fresh" class="why-disabled">{{ s.upgrade.needPreview }}</span>
      <button class="btn" type="button" @click="emit('close')">{{ s.app.cancel }}</button>
      <button class="btn btn-primary" type="button" :disabled="!fresh || busy || !preview?.changed" data-test="convert-go" @click="go">{{ busy ? s.convert.working : s.convert.go(to) }}</button>
    </template>
  </Modal>
</template>
