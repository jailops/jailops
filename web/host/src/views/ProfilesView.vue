<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import { runOp } from '@jailops/shared/composables/ops'
import { ApiError } from '@jailops/shared/api/client'
import Icon from '@jailops/shared/components/Icon.vue'
import Modal from '@jailops/shared/components/Modal.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import ConfirmDialog from '@jailops/shared/components/ConfirmDialog.vue'
import LoadState from '@jailops/shared/components/LoadState.vue'
import { checkProfile } from '@/lib/configKeys'
import { projectState } from '@/composables/project'
import type { Profile } from '@jailops/shared/api/types'

const scope = ref<'project' | 'host'>('project')
const l = useLoad(async () => ({ list: await api.profiles.list(scope.value === 'host' ? 'host' : undefined), jails: await api.jails.list().catch(() => []) }))
const usedBy = (n: string) => (l.data.value?.jails ?? []).filter((j) => (j.spec.profiles ?? []).includes(n)).map((j) => j.metadata.name)

const editing = ref<Profile | 'new' | null>(null)
const f = reactive({ name: '', description: '', rows: [] as Array<{ key: string; value: string }> })
const busy = ref(false)
const error = ref<ApiError | null>(null)
const msgs = { int: s.config.invalidInt, size: s.config.invalidSize, rate: s.config.invalidRate, mac: s.config.invalidMac, address: s.config.invalidAddress, listen: s.config.invalidListen, bool: s.config.invalidBool, name: s.config.invalidPattern, enum: s.config.invalidEnum }
const config = computed(() => Object.fromEntries(f.rows.filter((r) => r.key.trim()).map((r) => [r.key.trim(), r.value])))
const issues = computed(() => checkProfile(config.value, msgs, { unknown: s.profiles.unknownKey, notAllowed: s.profiles.notAllowed }))
const issueOf = (k: string) => issues.value.find((i) => i.key === k.trim())?.message

function open(p: Profile | 'new'): void {
  editing.value = p
  error.value = null
  if (p === 'new') Object.assign(f, { name: '', description: '', rows: [{ key: '', value: '' }] })
  else Object.assign(f, { name: p.metadata.name, description: p.spec.description ?? '', rows: Object.entries(p.spec.config ?? {}).map(([key, value]) => ({ key, value })) })
}
async function save(): Promise<void> {
  busy.value = true
  error.value = null
  const body: Profile = { metadata: { name: f.name }, spec: { ...(f.description ? { description: f.description } : {}), config: config.value } }
  try {
    if (editing.value === 'new') await runOp(s.profiles.createTitle + ': ' + f.name, () => api.profiles.create(body))
    else await runOp(s.profiles.editTitle(f.name), () => api.profiles.put({ ...(editing.value as Profile), spec: body.spec }))
    editing.value = null
    await l.reload()
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
const removing = ref<Profile | null>(null)
const typed = ref('')
const remove = () => runOp(s.profiles.deleteTitle(removing.value!.metadata.name), () => api.profiles.remove(removing.value!.metadata.name))
</script>

<template>
  <div>
    <div class="page-head"><h1>{{ s.profiles.title }}</h1><button class="btn btn-primary" type="button" data-test="new-profile" @click="open('new')"><Icon name="plus" />{{ s.profiles.create }}</button></div>
    <p class="muted">{{ s.profiles.lead }}</p>
    <div class="tabs" role="tablist">
      <button type="button" role="tab" :aria-selected="scope === 'project'" @click="scope = 'project'; l.reload()">{{ s.profiles.ofProject(projectState.current) }}</button>
      <button type="button" role="tab" :aria-selected="scope === 'host'" @click="scope = 'host'; l.reload()">{{ s.profiles.ofHost }}</button>
    </div>
    <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!l.data.value" @retry="l.reload">
      <div class="panel">
        <p v-if="!l.data.value?.list.length" class="empty">{{ s.profiles.empty }}</p>
        <div v-else class="table-wrap"><table data-test="profiles-table">
          <thead><tr><th>{{ s.profiles.name }}</th><th>{{ s.profiles.keys }}</th><th class="hide-sm">{{ s.profiles.appliedTo }}</th><th></th></tr></thead>
          <tbody><tr v-for="p in l.data.value?.list" :key="p.metadata.name" :data-profile="p.metadata.name">
            <td><span class="mono">{{ p.metadata.name }}</span><div v-if="p.spec.description" class="faint">{{ p.spec.description }}</div></td>
            <td class="mono"><div v-for="(v, k) in p.spec.config" :key="k">{{ k }}={{ v }}</div></td>
            <td class="hide-sm"><router-link v-for="j in usedBy(p.metadata.name)" :key="j" class="mono chip" :to="`/jails/${j}`">{{ j }}</router-link></td>
            <td class="nowrap"><button class="btn btn-sm" type="button" @click="open(p)">{{ s.profiles.edit }}</button><button class="btn btn-sm btn-danger" type="button" @click="removing = p; typed = ''">{{ s.profiles.delete }}</button></td>
          </tr></tbody></table></div>
      </div>
    </LoadState>

    <Modal :open="!!editing" :title="editing === 'new' ? s.profiles.createTitle : s.profiles.editTitle(f.name)" wide @close="editing = null">
      <form id="pr" @submit.prevent="save">
        <div class="grid-2"><div class="field"><label for="pr-name">{{ s.profiles.name }}</label><input id="pr-name" v-model="f.name" class="mono" type="text" required :disabled="editing !== 'new'" /></div>
          <div class="field"><label for="pr-desc">{{ s.profiles.description }}</label><input id="pr-desc" v-model="f.description" type="text" /></div></div>
        <h3>{{ s.profiles.keys }}</h3>
        <div v-for="(r, i) in f.rows" :key="i" class="grant-row grant-sto" :data-row="i">
          <input v-model="r.key" class="mono" type="text" list="profile-keys" :aria-label="s.profiles.key" :class="{ invalid: issueOf(r.key) }" />
          <input v-model="r.value" class="mono" type="text" :aria-label="s.profiles.value" />
          <button class="btn btn-sm btn-icon btn-danger" type="button" :aria-label="s.firewall.removeRule" @click="f.rows.splice(i, 1)"><Icon name="trash" /></button>
          <span v-if="issueOf(r.key)" class="err" data-test="profile-issue" style="grid-column: 1 / -1">{{ issueOf(r.key) }}</span>
        </div>
        <button class="btn btn-sm" type="button" @click="f.rows.push({ key: '', value: '' })"><Icon name="plus" />{{ s.profiles.addKey }}</button>
        <ErrorPanel v-if="error" :error="error" style="margin-top: 12px" />
      </form>
      <template #footer><button class="btn" type="button" @click="editing = null">{{ s.app.cancel }}</button><button class="btn btn-primary" type="submit" form="pr" :disabled="busy || !f.name || issues.length > 0" data-test="profile-save">{{ s.profiles.save }}</button></template>
    </Modal>
    <ConfirmDialog :open="!!removing" :title="s.profiles.deleteTitle(removing?.metadata.name ?? '')" :confirm-label="s.profiles.delete" danger :run="remove" @close="removing = null" @done="l.reload" />
  </div>
</template>
