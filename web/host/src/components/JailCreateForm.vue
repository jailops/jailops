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
// one optional volume; an encrypted one needs a passphrase, typed twice, sent once and then forgotten
const vol = reactive({ name: '', mount: '', encrypted: false, pass: '', again: '' })
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
const volUsed = computed(() => !!(vol.name || vol.mount || vol.encrypted))
const volError = computed(() => (volUsed.value && !(/^[a-z0-9][a-z0-9_-]{0,63}$/.test(vol.name) && /^\/[^\s]*[^/\s]$/.test(vol.mount)) ? s.jails.volumeInvalid : ''))
const passError = computed(() => {
  if (!vol.encrypted) return ''
  if (vol.pass && vol.pass.length < 12) return s.keys.tooShort
  if (vol.again && vol.pass !== vol.again) return s.keys.mismatch
  return ''
})
const volOk = computed(() => !volUsed.value || (!volError.value && (!vol.encrypted || (vol.pass.length >= 12 && vol.pass === vol.again))))
function clearVolume(): void {
  Object.assign(vol, { name: '', mount: '', encrypted: false, pass: '', again: '' })
}
const missing = computed(() => (!imageRefs.value.length ? s.jails.needImage : !stoNames.value.length ? s.jails.needStorage : ''))

watch(() => props.open, (o) => {
  if (!o) return
  error.value = null
  Object.assign(f, { name: '', image: imageRefs.value[0] ?? '', storage: stoNames.value[0] ?? '', network: netNames.value[0] ?? '' })
})
watch(() => props.open, (o) => { if (!o) clearVolume() })
watch(() => vol.encrypted, (e) => { if (!e) Object.assign(vol, { pass: '', again: '' }) })

async function save(): Promise<void> {
  busy.value = true
  error.value = null
  const volumes = volUsed.value ? [{ name: vol.name, mount: vol.mount, ...(vol.encrypted ? { encrypted: true } : {}) }] : []
  const passphrase = vol.encrypted ? vol.pass : ''
  Object.assign(vol, { pass: '', again: '' })
  try {
    await runOp(s.jails.createTitle + ': ' + f.name, () =>
      api.jails.create({ metadata: { name: f.name }, spec: { image: f.image, storage: f.storage, ...(f.network ? { network: [{ network: f.network, ipv4: 'auto' }] } : {}), ...(volumes.length ? { volumes } : {}) }, ...(passphrase ? { volume_passphrase: passphrase } : {}) }),
    )
    clearVolume()
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
      <fieldset class="vol-set">
        <legend>{{ s.jails.volume }}</legend>
        <div class="field"><label for="jc-vol">{{ s.jails.volumeName }}</label><input id="jc-vol" v-model="vol.name" class="mono" type="text" autocomplete="off" /></div>
        <div class="field"><label for="jc-mount">{{ s.jails.volumeMount }}</label><input id="jc-mount" v-model="vol.mount" class="mono" type="text" autocomplete="off" placeholder="/var/db/app" /></div>
        <label class="check"><input v-model="vol.encrypted" type="checkbox" data-test="jc-encrypted" aria-describedby="jc-enc-hint" /> {{ s.jails.volumeEncrypted }}</label>
        <span id="jc-enc-hint" class="hint">{{ s.jails.volumeEncryptedHint }}</span>
        <template v-if="vol.encrypted">
          <div class="field"><label for="jc-pass">{{ s.jails.passphrase }}</label><input id="jc-pass" v-model="vol.pass" type="password" autocomplete="new-password" /></div>
          <div class="field"><label for="jc-pass2">{{ s.jails.passphraseRepeat }}</label><input id="jc-pass2" v-model="vol.again" type="password" autocomplete="new-password" /></div>
        </template>
        <span v-if="volError || passError" class="field-error" role="alert" data-test="jc-vol-error">{{ volError || passError }}</span>
      </fieldset>
      <ErrorPanel v-if="error" :error="error" />
    </form>
    <template #footer>
      <button class="btn" type="button" @click="emit('close')">{{ s.app.cancel }}</button>
      <button v-if="!missing" class="btn btn-primary" type="submit" form="jc" :disabled="busy || !nameOk || !f.image || !f.storage || !volOk" data-test="jail-create-go">{{ s.jails.create }}</button>
    </template>
  </Modal>
</template>

<style>
.vol-set { border: 1px solid var(--border); border-radius: var(--radius); padding: 8px 12px; margin: 0 0 12px; }
.vol-set legend { padding: 0 4px; font-size: 13px; color: var(--text-2); }
.vol-set .check { display: flex; gap: 8px; align-items: center; }
</style>
