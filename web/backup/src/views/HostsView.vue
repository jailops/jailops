<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import { runOp } from '@jailops/shared/composables/ops'
import { session } from '@jailops/shared/composables/session'
import { ApiError } from '@jailops/shared/api/client'
import { ago } from '@jailops/shared/lib/format'
import Icon from '@jailops/shared/components/Icon.vue'
import Modal from '@jailops/shared/components/Modal.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import LoadState from '@jailops/shared/components/LoadState.vue'
import RoleNote from '@/components/RoleNote.vue'
import { allows } from '@/lib/roles'
import type { Host } from '@/types'

const l = useLoad(() => api.hosts.list())
const admin = computed(() => allows(session.role, 'admin'))
const scopeOptions = ['backup-push', 'backup-pull']
const stale = (h: Host) => !h.last_seen_at || Date.now() - Date.parse(h.last_seen_at) > 2 * 86400_000

const enrolling = ref(false)
const form = reactive({ name: '', fingerprint: '', certificate: '', note: '', scopes: ['backup-push', 'backup-pull'] as string[] })
const busy = ref(false)
const error = ref<ApiError | null>(null)
function openEnrol(): void {
  Object.assign(form, { name: '', fingerprint: '', certificate: '', note: '', scopes: ['backup-push', 'backup-pull'] })
  error.value = null
  enrolling.value = true
}
async function enrol(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    await runOp(s.hosts.enrolTitle + ': ' + form.name, () => api.hosts.enrol({ name: form.name, ...(form.fingerprint.trim() ? { fingerprint: form.fingerprint.trim() } : {}), ...(form.certificate.trim() ? { certificate: form.certificate } : {}), scopes: form.scopes, ...(form.note ? { note: form.note } : {}) }))
    enrolling.value = false
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}

const editing = ref<Host | null>(null)
const editScopes = ref<string[]>([])
function openEdit(h: Host): void {
  editing.value = h
  editScopes.value = [...h.scopes]
  error.value = null
}
async function saveScopes(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    await runOp(s.hosts.editScopes + ': ' + editing.value!.name, () => api.hosts.setScopes(editing.value!.name, editScopes.value))
    editing.value = null
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}

const revoking = ref<Host | null>(null)
const typed = ref('')
function openRevoke(h: Host): void {
  revoking.value = h
  typed.value = ''
  error.value = null
}
async function revoke(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    await runOp(s.hosts.revokeTitle(revoking.value!.name), () => api.hosts.revoke(revoking.value!.name))
    revoking.value = null
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div>
    <div class="page-head"><h1>{{ s.hosts.title }}</h1><button class="btn btn-primary" type="button" :disabled="!admin" data-test="enrol" @click="openEnrol"><Icon name="plus" />{{ s.hosts.enrol }}</button></div>
    <p class="muted">{{ s.hosts.lead }}</p>
    <RoleNote need="admin" />
    <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!l.data.value" @retry="l.reload">
      <div class="panel" style="margin-top: 12px">
        <p v-if="!l.data.value?.length" class="empty">{{ s.hosts.empty }}</p>
        <div v-else class="table-wrap"><table>
          <thead><tr><th>{{ s.hosts.name }}</th><th class="hide-sm">{{ s.hosts.fingerprint }}</th><th>{{ s.hosts.scopes }}</th><th>{{ s.hosts.lastSeen }}</th><th></th></tr></thead>
          <tbody><tr v-for="h in l.data.value" :key="h.id" :data-host="h.name" :class="{ drift: false }">
            <td><span class="mono">{{ h.name }}</span><span v-if="h.revoked_at" class="pill danger" style="margin-left: 6px">{{ s.hosts.revoked }}</span><div v-if="h.note" class="faint">{{ h.note }}</div></td>
            <td class="mono faint hide-sm" :title="h.fingerprint">{{ h.fingerprint.slice(0, 23) }}</td>
            <td><div v-for="sc in h.scopes" :key="sc"><span class="mono">{{ sc }}</span> <span class="faint">{{ s.hosts.scopeText[sc] }}</span></div></td>
            <td>{{ ago(h.last_seen_at) }}<span v-if="!h.revoked_at && stale(h)" class="pill warn" style="margin-left: 6px">{{ s.hosts.stale }}</span></td>
            <td class="nowrap">
              <button class="btn btn-sm" type="button" :disabled="!admin || !!h.revoked_at" @click="openEdit(h)" :aria-label="`${s.hosts.editScopes} ${h.name}`">{{ s.hosts.editScopes }}</button>
              <button class="btn btn-sm btn-danger" type="button" :disabled="!admin || !!h.revoked_at" data-test="revoke" :aria-label="`${s.hosts.revoke} ${h.name}`" @click="openRevoke(h)">{{ s.hosts.revoke }}</button>
            </td>
          </tr></tbody></table></div>
      </div>
    </LoadState>

    <Modal :open="enrolling" :title="s.hosts.enrolTitle" wide @close="enrolling = false">
      <form id="en" @submit.prevent="enrol">
        <p class="muted">{{ s.hosts.enrolText }}</p>
        <div class="field"><label for="en-name">{{ s.hosts.name }}</label><input id="en-name" v-model="form.name" class="mono" type="text" required /></div>
        <div class="field"><label for="en-fp">{{ s.hosts.fp }}</label><input id="en-fp" v-model="form.fingerprint" class="mono" type="text" spellcheck="false" /></div>
        <div class="field"><label for="en-pem">{{ s.hosts.pem }}</label><textarea id="en-pem" v-model="form.certificate" rows="4" spellcheck="false"></textarea></div>
        <fieldset class="field"><legend class="label">{{ s.hosts.scopes }}</legend><label v-for="sc in scopeOptions" :key="sc" class="check"><input v-model="form.scopes" type="checkbox" :value="sc" /><span class="mono">{{ sc }}</span> <span class="faint">{{ s.hosts.scopeText[sc] }}</span></label></fieldset>
        <div class="field"><label for="en-note">{{ s.hosts.note }}</label><input id="en-note" v-model="form.note" type="text" /></div>
        <ErrorPanel v-if="error" :error="error" />
      </form>
      <template #footer><button class="btn" type="button" @click="enrolling = false">{{ s.app.cancel }}</button><button class="btn btn-primary" type="submit" form="en" :disabled="busy || !form.name || (!form.fingerprint.trim() && !form.certificate.trim())" data-test="enrol-go">{{ s.hosts.submit }}</button></template>
    </Modal>

    <Modal :open="!!editing" :title="s.hosts.editScopes + ': ' + (editing?.name ?? '')" @close="editing = null">
      <fieldset class="field"><legend class="label">{{ s.hosts.scopes }}</legend><label v-for="sc in scopeOptions" :key="sc" class="check"><input v-model="editScopes" type="checkbox" :value="sc" /><span class="mono">{{ sc }}</span> <span class="faint">{{ s.hosts.scopeText[sc] }}</span></label></fieldset>
      <ErrorPanel v-if="error" :error="error" />
      <template #footer><button class="btn" type="button" @click="editing = null">{{ s.app.cancel }}</button><button class="btn btn-primary" type="button" :disabled="busy || !editScopes.length" @click="saveScopes">{{ s.hosts.save }}</button></template>
    </Modal>

    <Modal :open="!!revoking" :title="s.hosts.revokeTitle(revoking?.name ?? '')" @close="revoking = null">
      <form id="rv" @submit.prevent="revoke">
        <p class="muted">{{ s.hosts.revokeText }}</p>
        <div class="field"><label for="rv-typed">{{ s.hosts.typeName(revoking?.name ?? '') }}</label><input id="rv-typed" v-model="typed" class="mono" type="text" autocomplete="off" spellcheck="false" /></div>
        <ErrorPanel v-if="error" :error="error" />
      </form>
      <template #footer><button class="btn" type="button" @click="revoking = null">{{ s.app.cancel }}</button><button class="btn btn-danger" type="submit" form="rv" :disabled="busy || typed !== revoking?.name" data-test="revoke-go">{{ s.hosts.revoke }}</button></template>
    </Modal>
  </div>
</template>
