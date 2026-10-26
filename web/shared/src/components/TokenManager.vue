<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { common as s } from '../i18n/common'
import { ApiError } from '../api/client'
import { runOp } from '../composables/ops'
import { stamp } from '../lib/format'
import type { MutationResult } from '../api/types'
import Modal from './Modal.vue'
import Callout from './Callout.vue'
import CopyField from './CopyField.vue'
import ErrorPanel from './ErrorPanel.vue'
import ConfirmDialog from './ConfirmDialog.vue'

/** One token as a console lists it; each app maps its own wire shape to this. */
export interface TokenRow { name: string; role: string; expires_at?: string; allow_from?: string[]; projects?: string[]; global?: boolean }
export interface TokenForm { name: string; role: string; expires_at?: string; allow_from?: string[]; projects?: string[]; global?: boolean }

const props = defineProps<{
  tokens: TokenRow[]
  /** Roles a token may have, with a short description of what each may do. */
  roles: Array<{ value: string; text: string }>
  create: (f: TokenForm) => Promise<MutationResult<{ secret?: string }>>
  remove: (name: string) => Promise<MutationResult<unknown>>
  /** When given, the form asks for a scope: some of these projects, or Global. */
  projectNames?: string[]
}>()
const emit = defineEmits<{ changed: [] }>()

const creating = ref(false)
const form = reactive({ name: '', role: '', expires: '', allowFrom: '', projects: [] as string[], global: false })
const scoped = computed(() => !!props.projectNames)
const scopeOk = computed(() => !scoped.value || form.global || form.projects.length > 0)
const busy = ref(false)
const error = ref<ApiError | null>(null)
const secret = ref('')
const secretFor = ref('')
const revoking = ref<TokenRow | null>(null)
const expired = (t: string | undefined) => !!t && !t.startsWith('0001') && Date.parse(t) < Date.now()
const roleText = computed(() => props.roles.find((r) => r.value === form.role)?.text ?? '')
const split = (v: string) => v.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean)

defineExpose({ openCreate })
function openCreate(): void {
  Object.assign(form, { name: '', role: props.roles.find((r) => r.value === 'operator')?.value ?? props.roles[0].value, expires: '', allowFrom: '', projects: [], global: false })
  error.value = null
  creating.value = true
}
async function create(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    const r = await runOp(s.access.createTitle(form.name), () =>
      props.create({
        name: form.name,
        role: form.role,
        ...(form.expires ? { expires_at: new Date(form.expires + 'T23:59:59Z').toISOString() } : {}),
        ...(form.allowFrom.trim() ? { allow_from: split(form.allowFrom) } : {}),
        ...(scoped.value ? (form.global ? { global: true } : { projects: form.projects }) : {}),
      }),
    )
    secret.value = (r.data as { secret?: string } | undefined)?.secret ?? ''
    secretFor.value = form.name
    creating.value = false
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
const revoke = () => runOp(s.access.revokeTitle(revoking.value!.name), () => props.remove(revoking.value!.name))
function closeSecret(): void {
  secret.value = ''
  emit('changed')
}
</script>

<template>
  <div>
    <div class="panel">
      <p v-if="!tokens.length" class="empty">{{ s.access.noTokens }}</p>
      <div v-else class="table-wrap"><table>
        <thead><tr><th>{{ s.access.tokenName }}</th><th>{{ s.access.permission }}</th><th>{{ s.access.expires }}</th><th>{{ s.access.scope }}</th><th class="hide-sm">{{ s.access.allowFrom }}</th><th></th></tr></thead>
        <tbody><tr v-for="t in tokens" :key="t.name">
          <td class="mono">{{ t.name }}</td><td class="mono">{{ t.role }}</td>
          <td><span v-if="!t.expires_at || t.expires_at.startsWith('0001')" class="faint">{{ s.access.expiresNever }}</span><span v-else>{{ stamp(t.expires_at) }} <span v-if="expired(t.expires_at)" class="pill danger">{{ s.access.expired }}</span></span></td>
          <td><span v-if="t.global" class="badge warn" data-test="global-badge">{{ s.access.globalBadge }}</span><template v-else><span v-for="p in t.projects ?? []" :key="p" class="chip mono">{{ p }}</span><span v-if="!t.projects?.length" class="faint">-</span></template></td>
          <td class="mono hide-sm">{{ t.allow_from?.join(', ') || '-' }}</td>
          <td><button class="btn btn-sm btn-danger" type="button" @click="revoking = t">{{ s.access.revoke }}</button></td>
        </tr></tbody></table></div>
    </div>

    <Modal :open="creating" :title="s.access.newToken" @close="creating = false">
      <form id="nt" @submit.prevent="create">
        <div class="field"><label for="nt-name">{{ s.access.tokenName }}</label><input id="nt-name" v-model="form.name" class="mono" type="text" required /></div>
        <div class="field"><label for="nt-perm">{{ s.access.permission }}</label>
          <select id="nt-perm" v-model="form.role"><option v-for="r in roles" :key="r.value" :value="r.value">{{ r.value }}</option></select><span class="hint">{{ roleText }}</span></div>
        <fieldset v-if="scoped" class="field scope-box" data-test="scope">
          <legend class="label">{{ s.access.scope }}</legend>
          <label v-for="p in projectNames" :key="p" class="check"><input v-model="form.projects" type="checkbox" :value="p" :disabled="form.global" /><span class="mono">{{ p }}</span></label>
          <label class="check"><input v-model="form.global" type="checkbox" data-test="global-toggle" />{{ s.access.scopeGlobal }}</label>
          <Callout v-if="form.global" tone="warn">{{ s.access.scopeGlobalWarning }}</Callout>
          <span v-if="!scopeOk" class="err" data-test="scope-required">{{ s.access.scopeRequired }}</span>
        </fieldset>
        <div class="field"><label for="nt-exp">{{ s.access.expires }}</label><input id="nt-exp" v-model="form.expires" type="date" /></div>
        <div class="field"><label for="nt-from">{{ s.access.allowFrom }}</label><input id="nt-from" v-model="form.allowFrom" class="mono" type="text" /><span class="hint">{{ s.access.allowFromHint }}</span></div>
        <ErrorPanel v-if="error" :error="error" />
      </form>
      <template #footer>
        <button class="btn" type="button" @click="creating = false">{{ s.app.cancel }}</button>
        <button class="btn btn-primary" type="submit" form="nt" :disabled="busy || !form.name || !scopeOk">{{ s.access.create }}</button>
      </template>
    </Modal>

    <Modal :open="!!secret" :title="s.access.createTitle(secretFor)" @close="() => {}">
      <Callout tone="warn">{{ s.access.shownOnce }}</Callout>
      <div style="margin-top: 12px"><CopyField :value="secret" :label="s.access.tokenName" /></div>
      <template #footer><button class="btn btn-primary" type="button" data-test="secret-done" @click="closeSecret">{{ s.access.done }}</button></template>
    </Modal>

    <ConfirmDialog :open="!!revoking" :title="s.access.revokeTitle(revoking?.name ?? '')" :confirm-label="s.access.revoke" danger :run="revoke" @close="revoking = null" @done="emit('changed')" />
  </div>
</template>
