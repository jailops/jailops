<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { runOp } from '@jailops/shared/composables/ops'
import Modal from '@jailops/shared/components/Modal.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import type { Image, Network, Project, Storage } from '@jailops/shared/api/types'

const props = defineProps<{ open: boolean; project: Project | undefined; images: Image[]; storage: Storage[]; networks: Network[] }>()
const emit = defineEmits<{ close: []; done: [] }>()

const f = reactive({ name: '', image: '', storage: '', network: '' })
const busy = ref(false)
const error = ref<ApiError | null>(null)

/** Image references a jail may use: each alias, and the newest generation pinned. */
const imageRefs = computed(() =>
  props.images.flatMap((i) => {
    const gens = i.status.generations ?? []
    const last = gens.length ? Math.max(...gens.map((g) => g.number)) : 0
    return [...(i.spec.aliases ?? []).map((a) => `${i.metadata.name}@${a}`), ...(last ? [`${i.metadata.name}#g${last}`] : [])]
  }),
)
/** The project's grants narrow the choices; a "*" grant or no project data allows everything. */
const stoNames = computed(() => {
  const g = props.project?.spec.storage ?? []
  const all = props.storage.map((x) => x.metadata.name)
  return !g.length || g.some((x) => x.storage === '*') ? all : all.filter((n) => g.some((x) => x.storage === n))
})
const netNames = computed(() => {
  const g = props.project?.spec.networks ?? []
  const all = props.networks.map((x) => x.metadata.name)
  return g.length ? all.filter((n) => g.some((x) => x.network === n)) : all
})
const nameOk = computed(() => /^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$/.test(f.name))
const nameError = computed(() => (f.name && !nameOk.value ? s.jails.nameInvalid : ''))
const missing = computed(() => (!imageRefs.value.length ? s.jails.needImage : !stoNames.value.length ? s.jails.needStorage : ''))

watch(() => props.open, (o) => {
  if (!o) return
  error.value = null
  Object.assign(f, { name: '', image: imageRefs.value[0] ?? '', storage: stoNames.value[0] ?? '', network: netNames.value[0] ?? '' })
})

async function save(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    await runOp(s.jails.createTitle + ': ' + f.name, () =>
      api.jails.create({ metadata: { name: f.name }, spec: { image: f.image, storage: f.storage, ...(f.network ? { network: [{ network: f.network, ipv4: 'auto' }] } : {}) } }),
    )
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
  <Modal :open="open" :title="s.jails.createTitle" @close="emit('close')">
    <p v-if="missing" class="muted" data-test="jail-missing">{{ missing }}</p>
    <form v-else id="jc" @submit.prevent="save">
      <div class="field">
        <label for="jc-name">{{ s.jails.name }}</label>
        <input id="jc-name" v-model="f.name" class="mono" type="text" required autocomplete="off" :aria-invalid="!!nameError" :aria-describedby="nameError ? 'jc-name-err' : undefined" />
        <span v-if="nameError" id="jc-name-err" class="field-error" role="alert">{{ nameError }}</span>
      </div>
      <div class="field"><label for="jc-image">{{ s.jails.image }}</label><select id="jc-image" v-model="f.image"><option v-for="x in imageRefs" :key="x" :value="x">{{ x }}</option></select></div>
      <div class="field"><label for="jc-sto">{{ s.nav.storage }}</label><select id="jc-sto" v-model="f.storage"><option v-for="x in stoNames" :key="x" :value="x">{{ x }}</option></select></div>
      <div class="field">
        <label for="jc-net">{{ s.nav.networks }}</label>
        <select id="jc-net" v-model="f.network"><option value="">{{ s.jails.noNetwork }}</option><option v-for="x in netNames" :key="x" :value="x">{{ x }}</option></select>
        <span class="hint">{{ s.jails.networkHint }}</span>
      </div>
      <ErrorPanel v-if="error" :error="error" />
    </form>
    <template #footer>
      <button class="btn" type="button" @click="emit('close')">{{ s.app.cancel }}</button>
      <button v-if="!missing" class="btn btn-primary" type="submit" form="jc" :disabled="busy || !nameOk || !f.image || !f.storage" data-test="jail-create-go">{{ s.jails.create }}</button>
    </template>
  </Modal>
</template>
