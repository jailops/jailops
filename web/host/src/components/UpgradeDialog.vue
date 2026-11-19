<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { runOp } from '@jailops/shared/composables/ops'
import type { Image, Jail } from '@jailops/shared/api/types'
import type { UpgradePreview } from '@/types'
import { imageOf, moveImages, readPreview, releaseChange } from '@/lib/moves'
import Modal from '@jailops/shared/components/Modal.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'

const props = defineProps<{ open: boolean; jail: Jail }>()
const emit = defineEmits<{ close: []; done: [] }>()

const images = ref<Image[]>([])
const target = ref('')
const image = ref('') // the image to move to; empty keeps the jail's image (a new generation of it)
const preview = ref<UpgradePreview | null>(null)
const previewFor = ref<string | null>(null)
const busy = ref(false)
const error = ref<ApiError | null>(null)

const imageName = computed(() => imageOf(props.jail.spec.image))
const targetImage = computed(() => image.value || imageName.value)
const gens = computed(() => images.value.find((i) => i.metadata.name === targetImage.value)?.status.generations ?? [])
const moves = computed(() => moveImages(images.value, props.jail))
const key = computed(() => `${image.value}|${target.value}`)
const fresh = computed(() => !!preview.value && previewFor.value === key.value)
const change = computed(() => (preview.value ? releaseChange(preview.value) : ''))
const release = (name: string): string => images.value.find((i) => i.metadata.name === name)?.spec.release ?? ''

watch(image, () => { target.value = '' })

watch(() => props.open, async (o) => {
  if (!o) return
  target.value = ''
  image.value = ''
  preview.value = null
  previewFor.value = null
  error.value = null
  try {
    images.value = await api.images.list()
  } catch (e) {
    error.value = e instanceof ApiError ? e : null
  }
})

async function dry(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    const r = await runOp(s.upgrade.title(props.jail.metadata.name), () => api.jails.upgrade(props.jail.metadata.name, { generation: target.value || undefined, image: image.value || undefined, dry_run: true }), { silent: true })
    preview.value = readPreview(r.data ?? r.op?.metadata, target.value)
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
    await runOp(s.upgrade.title(props.jail.metadata.name), () => api.jails.upgrade(props.jail.metadata.name, { generation: target.value || undefined, image: image.value || undefined }))
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
  <Modal :open="open" :title="s.upgrade.title(jail.metadata.name)" wide @close="emit('close')">
    <div v-if="moves.length" class="field">
      <label for="up-image">{{ s.upgrade.image }}</label>
      <select id="up-image" v-model="image" data-test="up-image">
        <option value="">{{ s.upgrade.stay(imageName, release(imageName)) }}</option>
        <option v-for="i in moves" :key="i.metadata.name" :value="i.metadata.name">{{ i.metadata.name }} ({{ i.spec.release }})</option>
      </select>
      <span class="hint">{{ s.upgrade.imageHint }}</span>
    </div>
    <div class="field">
      <label for="up-target">{{ s.upgrade.target }}</label>
      <select id="up-target" v-model="target">
        <option value="">{{ image ? s.upgrade.newestOf(image) : s.upgrade.newest }}</option>
        <option v-for="g in gens" :key="g.number" :value="g.name">{{ g.name }}</option>
      </select>
    </div>
    <div class="row"><button class="btn" type="button" :disabled="busy" data-test="preview" @click="dry">{{ busy && !fresh ? s.upgrade.previewing : s.upgrade.preview }}</button></div>
    <div v-if="preview" class="stack" style="margin-top: 12px" data-test="preview-result">
      <p v-if="!fresh" class="muted">{{ s.upgrade.stale }}</p>
      <p v-if="preview.from_generation || preview.to_generation"><strong class="mono">{{ s.upgrade.fromTo(preview.from_generation || '?', preview.to_generation || '?') }}</strong></p>
      <p v-if="change" data-test="preview-release">{{ s.upgrade.release(change) }}</p>
      <p v-if="preview.kept?.length" class="muted" data-test="preview-kept">{{ s.upgrade.kept(preview.kept.join(', ')) }}</p>
      <p v-if="preview.snapshot" class="muted">{{ s.upgrade.rollbackPoint(preview.snapshot) }}</p>
      <div v-if="preview.steps.length">
        <h3>{{ s.upgrade.plan }}</h3>
        <ol class="steps"><li v-for="st in preview.steps" :key="st">{{ st }}</li></ol>
      </div>
      <p v-else class="muted">{{ s.upgrade.noPreview }}</p>
      <Callout v-if="preview.etc_conflicts?.length" tone="danger">
        <strong>{{ s.upgrade.conflicts }}</strong>
        <ul><li v-for="c in preview.etc_conflicts" :key="c" class="mono">{{ c }}</li></ul>
      </Callout>
      <Callout v-if="preview.warnings?.length" tone="warn">
        <strong>{{ s.upgrade.warnings }}</strong>
        <ul><li v-for="c in preview.warnings" :key="c">{{ c }}</li></ul>
      </Callout>
    </div>
    <p v-else class="muted" style="margin-top: 12px">{{ s.upgrade.needPreview }}</p>
    <ErrorPanel v-if="error" :error="error" style="margin-top: 12px" />
    <template #footer>
      <span v-if="!fresh" class="why-disabled">{{ s.upgrade.needPreview }}</span>
      <button class="btn" type="button" @click="emit('close')">{{ s.app.cancel }}</button>
      <button class="btn btn-primary" type="button" :disabled="!fresh || busy" data-test="upgrade-go" @click="go">{{ s.upgrade.run }}</button>
    </template>
  </Modal>
</template>

<style>
.steps { margin: 4px 0 0; padding-left: 20px; }
</style>
