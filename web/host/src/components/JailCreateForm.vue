<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { runOp } from '@jailops/shared/composables/ops'
import { useFormCheck } from '@jailops/shared/composables/form'
import { nameHint, nameProblem } from '@jailops/shared/lib/names'
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
const volUsed = computed(() => !!(vol.name || vol.mount || vol.encrypted))

// Field id -> problem, in the order of the form: the first one is named next to Create and focused on submit.
const check = useFormCheck(() => ({
  'jc-name': nameProblem(f.name),
  'jc-image': f.image ? '' : s.jails.needImageField,
  'jc-sto': f.storage ? '' : s.jails.needStorageField,
  'jc-vol': !volUsed.value ? '' : !vol.name ? s.jails.volumeNameNeeded : nameProblem(vol.name),
  'jc-mount': !volUsed.value ? '' : !vol.mount ? s.jails.mountNeeded : /^\/[^\s]*[^/\s]$/.test(vol.mount) ? '' : s.jails.mountInvalid,
  'jc-pass': !vol.encrypted ? '' : !vol.pass ? s.jails.passphraseNeeded : vol.pass.length < 12 ? s.keys.tooShort : '',
  'jc-pass2': !vol.encrypted ? '' : !vol.again ? s.jails.repeatNeeded : vol.pass !== vol.again ? s.keys.mismatch : '',
}))
const labels: Record<string, string> = { 'jc-name': s.jails.name, 'jc-image': s.jails.image, 'jc-sto': s.nav.storage, 'jc-vol': s.jails.volumeName, 'jc-mount': s.jails.volumeMount, 'jc-pass': s.jails.passphrase, 'jc-pass2': s.jails.passphraseRepeat }
const why = computed(() => (check.firstId.value ? s.form.fixFirst(`${labels[check.firstId.value]}: ${check.reason.value}`) : ''))
const describe = (id: string, hint?: string) => [check.err(id) ? `${id}-err` : '', hint && check.err(id) !== nameHint() ? hint : ''].filter(Boolean).join(' ') || undefined

function clearVolume(): void {
  Object.assign(vol, { name: '', mount: '', encrypted: false, pass: '', again: '' })
}
const missing = computed(() => (!imageRefs.value.length ? s.jails.needImage : !stoNames.value.length ? s.jails.needStorage : ''))

watch(() => props.open, (o) => {
  if (!o) {
    clearVolume()
    return
  }
  error.value = null
  check.reset()
  Object.assign(f, { name: '', image: imageRefs.value[0] ?? '', storage: stoNames.value[0] ?? '', network: netNames.value[0] ?? '' })
})
watch(() => vol.encrypted, (e) => { if (!e) Object.assign(vol, { pass: '', again: '' }) })

async function save(): Promise<void> {
  busy.value = true
  error.value = null
  const volumes = volUsed.value ? [{ name: vol.name, mount: vol.mount, ...(vol.encrypted ? { encrypted: true } : {}) }] : []
  const passphrase = vol.encrypted ? vol.pass : ''
  Object.assign(vol, { pass: '', again: '' })
  check.reset()
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
    <form v-else id="jc" novalidate @submit.prevent="check.submit(save)">
      <p class="hint faint jc-note">{{ s.form.requiredNote }}</p>
      <div class="field">
        <label for="jc-name">{{ s.jails.name }} <span class="req" aria-hidden="true">*</span></label>
        <input id="jc-name" v-model.trim="f.name" class="mono" type="text" required autocomplete="off" spellcheck="false" :aria-invalid="!!check.err('jc-name')" :aria-describedby="describe('jc-name', 'jc-name-hint')" @blur="f.name && check.touch('jc-name')" />
        <span v-if="check.err('jc-name')" id="jc-name-err" class="field-error" role="alert">{{ check.err('jc-name') }}</span>
        <span v-if="check.err('jc-name') !== nameHint()" id="jc-name-hint" class="hint">{{ nameHint() }}</span>
      </div>
      <div class="field">
        <label for="jc-image">{{ s.jails.image }} <span class="req" aria-hidden="true">*</span></label>
        <select id="jc-image" v-model="f.image" required :aria-invalid="!!check.err('jc-image')" :aria-describedby="describe('jc-image')"><option v-for="x in imageRefs" :key="x" :value="x">{{ x }}</option></select>
        <span v-if="check.err('jc-image')" id="jc-image-err" class="field-error" role="alert">{{ check.err('jc-image') }}</span>
      </div>
      <div class="field">
        <label for="jc-sto">{{ s.nav.storage }} <span class="req" aria-hidden="true">*</span></label>
        <select id="jc-sto" v-model="f.storage" required :aria-invalid="!!check.err('jc-sto')" :aria-describedby="describe('jc-sto')"><option v-for="x in stoNames" :key="x" :value="x">{{ x }}</option></select>
        <span v-if="check.err('jc-sto')" id="jc-sto-err" class="field-error" role="alert">{{ check.err('jc-sto') }}</span>
      </div>
      <div class="field">
        <label for="jc-net">{{ s.install.network }}</label>
        <select id="jc-net" v-model="f.network" aria-describedby="jc-net-hint"><option value="">{{ s.jails.noNetwork }}</option><option v-for="x in netNames" :key="x" :value="x">{{ x }}</option></select>
        <span id="jc-net-hint" class="hint">{{ s.jails.networkHint }}</span>
      </div>
      <fieldset class="vol-set">
        <legend>{{ s.jails.volume }}</legend>
        <div class="field">
          <label for="jc-vol">{{ s.jails.volumeName }}<span v-if="volUsed" class="req" aria-hidden="true"> *</span></label>
          <input id="jc-vol" v-model.trim="vol.name" class="mono" type="text" autocomplete="off" spellcheck="false" :aria-invalid="!!check.err('jc-vol')" :aria-describedby="describe('jc-vol', 'jc-vol-hint')" @blur="vol.name && check.touch('jc-vol')" />
          <span v-if="check.err('jc-vol')" id="jc-vol-err" class="field-error" role="alert" data-test="jc-vol-error">{{ check.err('jc-vol') }}</span>
          <span v-if="check.err('jc-vol') !== nameHint()" id="jc-vol-hint" class="hint">{{ nameHint() }}</span>
        </div>
        <div class="field">
          <label for="jc-mount">{{ s.jails.volumeMount }}<span v-if="volUsed" class="req" aria-hidden="true"> *</span></label>
          <input id="jc-mount" v-model.trim="vol.mount" class="mono" type="text" autocomplete="off" spellcheck="false" placeholder="/var/db/app" :aria-invalid="!!check.err('jc-mount')" :aria-describedby="describe('jc-mount')" @blur="vol.mount && check.touch('jc-mount')" />
          <span v-if="check.err('jc-mount')" id="jc-mount-err" class="field-error" role="alert" data-test="jc-vol-error">{{ check.err('jc-mount') }}</span>
        </div>
        <label class="check"><input v-model="vol.encrypted" type="checkbox" data-test="jc-encrypted" aria-describedby="jc-enc-hint" /> {{ s.jails.volumeEncrypted }}</label>
        <span id="jc-enc-hint" class="hint">{{ s.jails.volumeEncryptedHint }}</span>
        <template v-if="vol.encrypted">
          <div class="field jc-pass-field">
            <label for="jc-pass">{{ s.jails.passphrase }} <span class="req" aria-hidden="true">*</span></label>
            <input id="jc-pass" v-model="vol.pass" type="password" autocomplete="new-password" :aria-invalid="!!check.err('jc-pass')" :aria-describedby="describe('jc-pass', 'jc-pass-hint')" @blur="vol.pass && check.touch('jc-pass')" />
            <span v-if="check.err('jc-pass')" id="jc-pass-err" class="field-error" role="alert" data-test="jc-vol-error">{{ check.err('jc-pass') }}</span>
            <span id="jc-pass-hint" class="hint">{{ s.jails.passphraseHint }}</span>
          </div>
          <div class="field">
            <label for="jc-pass2">{{ s.jails.passphraseRepeat }} <span class="req" aria-hidden="true">*</span></label>
            <input id="jc-pass2" v-model="vol.again" type="password" autocomplete="new-password" :aria-invalid="!!check.err('jc-pass2')" :aria-describedby="describe('jc-pass2')" @blur="vol.again && check.touch('jc-pass2')" />
            <span v-if="check.err('jc-pass2')" id="jc-pass2-err" class="field-error" role="alert" data-test="jc-vol-error">{{ check.err('jc-pass2') }}</span>
          </div>
        </template>
      </fieldset>
      <ErrorPanel v-if="error" :error="error" />
    </form>
    <template #footer>
      <span v-if="!missing && why" class="why-disabled" data-test="jail-create-why">{{ why }}</span>
      <button class="btn" type="button" @click="emit('close')">{{ s.app.cancel }}</button>
      <button v-if="!missing" class="btn btn-primary" type="submit" form="jc" :disabled="busy" :aria-disabled="!check.ok.value" data-test="jail-create-go">{{ s.jails.create }}</button>
    </template>
  </Modal>
</template>

<style>
.vol-set { border: 1px solid var(--border); border-radius: var(--radius); padding: 8px 12px; margin: 0 0 12px; }
.vol-set legend { padding: 0 4px; font-size: 13px; color: var(--text-2); }
.vol-set .check { display: flex; gap: 8px; align-items: center; }
.vol-set > .hint { display: block; margin-top: 2px; color: var(--text-3); font-size: 12px; }
.jc-pass-field { margin-top: 10px; }
.jc-note { margin: 0 0 8px; font-size: 12px; }
</style>
