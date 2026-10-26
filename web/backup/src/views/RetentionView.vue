<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import { runOp } from '@jailops/shared/composables/ops'
import { session } from '@jailops/shared/composables/session'
import { ApiError } from '@jailops/shared/api/client'
import { stamp } from '@jailops/shared/lib/format'
import Icon from '@jailops/shared/components/Icon.vue'
import Modal from '@jailops/shared/components/Modal.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import ConfirmDialog from '@jailops/shared/components/ConfirmDialog.vue'
import LoadState from '@jailops/shared/components/LoadState.vue'
import RoleNote from '@/components/RoleNote.vue'
import { allows } from '@/lib/roles'
import { runJob } from '@/lib/jobs'
import type { RetentionPolicyRow, RetentionResult } from '@/types'
import type { RetentionPolicy } from '@jailops/shared/api/types'

const l = useLoad(async () => ({ rows: await api.retentionPolicies.list(), groups: await api.groups.list().catch(() => []) }))
const operate = computed(() => allows(session.role, 'backup-admin'))
const node = computed(() => session.info?.hostname ?? '')
const fields = ['last', 'hourly', 'daily', 'weekly', 'monthly', 'yearly'] as const
const keep = (p: RetentionPolicy) => fields.filter((f) => p[f]).map((f) => `${p[f]} ${f}`).join(', ') || '-'

// policy editor
const editing = ref(false)
const form = reactive({ scope: 'default', timezone: '', values: {} as Record<string, number | undefined> })
const busy = ref(false)
const error = ref<ApiError | null>(null)
function openEdit(r?: RetentionPolicyRow): void {
  form.scope = r?.scope ?? 'default'
  form.timezone = r?.timezone ?? ''
  form.values = { ...(r?.policy ?? {}) } as Record<string, number | undefined>
  error.value = null
  editing.value = true
}
async function save(): Promise<void> {
  busy.value = true
  error.value = null
  const policy: Record<string, number> = {}
  for (const f of fields) if (form.values[f]) policy[f] = Number(form.values[f])
  try {
    await runOp(s.retention.editTitle(form.scope), () => api.retentionPolicies.set(form.scope, policy as RetentionPolicy, form.timezone || undefined))
    editing.value = false
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
const removing = ref<RetentionPolicyRow | null>(null)
const remove = () => runOp(s.retention.removeTitle(removing.value!.scope), () => api.retentionPolicies.remove(removing.value!.scope))

// plan and run
const planGroup = ref('')
const plan = ref<RetentionResult | null>(null)
const planOpen = ref(false)
const planBusy = ref(false)
const planError = ref<ApiError | null>(null)
async function dryRun(): Promise<void> {
  planBusy.value = true
  planError.value = null
  try {
    const r = await runJob<RetentionResult>(s.retention.preview, () => api.jobs.retention({ dry_run: true, ...(planGroup.value ? { group: planGroup.value } : {}) }), { silent: true })
    plan.value = r.result ?? { dry_run: true, groups: [], deleted: 0 }
    planOpen.value = true
  } catch (e) {
    planError.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    planBusy.value = false
  }
}
const running = ref(false)
const typed = ref('')
const result = ref<RetentionResult | null>(null)
function openRun(): void {
  typed.value = ''
  planError.value = null
  running.value = true
}
async function run(): Promise<void> {
  planBusy.value = true
  planError.value = null
  try {
    const r = await runJob<RetentionResult>(s.retention.runTitle, () => api.jobs.retention({ ...(planGroup.value ? { group: planGroup.value } : {}) }))
    result.value = r.result ?? null
    running.value = false
  } catch (e) {
    planError.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    planBusy.value = false
  }
}
const totalDelete = computed(() => (plan.value?.groups ?? []).reduce((a, g) => a + g.delete.length, 0))
const totalKeep = computed(() => (plan.value?.groups ?? []).reduce((a, g) => a + g.keep.length, 0))
</script>

<template>
  <div>
    <div class="page-head"><h1>{{ s.retention.title }}</h1><button class="btn btn-primary" type="button" :disabled="!operate" data-test="add-policy" @click="openEdit()"><Icon name="plus" />{{ s.retention.add }}</button></div>
    <p class="muted">{{ s.retention.lead }}</p>
    <RoleNote need="backup-admin" />
    <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!l.data.value" @retry="l.reload">
      <div class="stack" style="margin-top: 12px">
        <section class="panel">
          <header><h2>{{ s.retention.policies }}</h2></header>
          <p v-if="!l.data.value?.rows.length" class="empty">{{ s.retention.empty }}</p>
          <div v-else class="table-wrap"><table>
            <thead><tr><th>{{ s.retention.scope }}</th><th>{{ s.retention.keep }}</th><th class="hide-sm">{{ s.retention.timezone }}</th><th class="hide-sm">{{ s.retention.updated }}</th><th></th></tr></thead>
            <tbody><tr v-for="r in l.data.value?.rows" :key="r.scope" :data-policy="r.scope">
              <td class="mono">{{ r.scope === 'default' ? s.retention.default : r.scope }}</td><td>{{ keep(r.policy) }}</td><td class="mono hide-sm">{{ r.timezone }}</td>
              <td class="faint hide-sm nowrap">{{ stamp(r.updated_at) }} <span class="mono">{{ r.updated_by }}</span></td>
              <td class="nowrap"><button class="btn btn-sm" type="button" :disabled="!operate" @click="openEdit(r)" :aria-label="`${s.retention.edit} ${r.scope}`">{{ s.retention.edit }}</button><button class="btn btn-sm btn-danger" type="button" :disabled="!operate" @click="removing = r" :aria-label="`${s.retention.remove} ${r.scope}`">{{ s.retention.remove }}</button></td>
            </tr></tbody></table></div>
        </section>
        <section class="panel">
          <header><h2>{{ s.retention.plan }}</h2></header>
          <div class="body stack">
            <div class="field" style="max-width: 360px; margin: 0"><label for="rp-group">{{ s.retention.onlyGroup }}</label>
              <select id="rp-group" v-model="planGroup"><option value="">-</option><option v-for="g in l.data.value?.groups" :key="g.group_id" :value="g.group_id">{{ g.group_id }}</option></select></div>
            <div class="row">
              <button class="btn" type="button" :disabled="planBusy" data-test="dry-run" @click="dryRun">{{ planBusy ? s.retention.previewing : s.retention.preview }}</button>
              <button class="btn btn-danger" type="button" :disabled="!operate" data-test="run" @click="openRun">{{ s.retention.run }}</button>
            </div>
            <ErrorPanel v-if="planError && !running" :error="planError" />
            <Callout v-if="result" tone="ok" data-test="run-result">{{ s.retention.done(result.deleted) }}</Callout>
          </div>
        </section>
      </div>
    </LoadState>

    <Modal :open="planOpen" :title="s.retention.preview" wide @close="planOpen = false">
      <div v-if="plan" class="stack" data-test="plan">
        <Callout tone="info">{{ s.retention.dryRun }}</Callout>
        <p v-if="!totalDelete" class="muted">{{ s.retention.nothing }}</p>
        <p v-else><strong>{{ s.retention.wouldDelete(totalDelete) }}</strong> · {{ s.retention.kept(totalKeep) }}</p>
        <section v-for="g in plan.groups.filter((x) => x.delete.length || x.error)" :key="g.group">
          <h3 class="mono">{{ g.group }} <span class="faint">({{ s.retention.source }}: {{ g.policy_source }})</span></h3>
          <p v-if="g.error" class="err">{{ g.error }}</p>
          <div class="table-wrap"><table>
            <thead><tr><th>{{ s.retention.snapshot }}</th><th class="hide-sm">{{ s.retention.time }}</th><th>{{ s.retention.reasons }}</th></tr></thead>
            <tbody><tr v-for="d in g.delete" :key="d.snapshot"><td class="mono">{{ d.snapshot }}</td><td class="faint nowrap hide-sm">{{ stamp(d.time) }}</td><td>{{ d.reasons.join('; ') }}</td></tr></tbody></table></div>
        </section>
      </div>
      <template #footer><button class="btn" type="button" @click="planOpen = false">{{ s.app.close }}</button></template>
    </Modal>

    <Modal :open="editing" :title="s.retention.editTitle(form.scope)" @close="editing = false">
      <form id="pe" @submit.prevent="save">
        <div class="field"><label for="pe-scope">{{ s.retention.scopeLabel }}</label><input id="pe-scope" v-model="form.scope" class="mono" type="text" required /></div>
        <div class="grid-2"><div v-for="f in fields" :key="f" class="field"><label :for="'pe-' + f">{{ s.retention.fields[f] }}</label><input :id="'pe-' + f" v-model.number="form.values[f]" type="number" min="0" /></div></div>
        <div class="field"><label for="pe-tz">{{ s.retention.timezone }}</label><input id="pe-tz" v-model="form.timezone" class="mono" type="text" placeholder="UTC" /></div>
        <ErrorPanel v-if="error" :error="error" />
      </form>
      <template #footer><button class="btn" type="button" @click="editing = false">{{ s.app.cancel }}</button><button class="btn btn-primary" type="submit" form="pe" :disabled="busy || !form.scope">{{ s.retention.save }}</button></template>
    </Modal>

    <Modal :open="running" :title="s.retention.runTitle" @close="running = false">
      <form id="rr" @submit.prevent="run">
        <Callout tone="danger">{{ s.retention.runText }}</Callout>
        <div class="field" style="margin-top: 12px"><label for="rr-typed">{{ s.retention.typeName(node) }}</label><input id="rr-typed" v-model="typed" class="mono" type="text" autocomplete="off" spellcheck="false" /></div>
        <ErrorPanel v-if="planError" :error="planError" />
      </form>
      <template #footer><button class="btn" type="button" @click="running = false">{{ s.app.cancel }}</button><button class="btn btn-danger" type="submit" form="rr" :disabled="planBusy || typed !== node" data-test="run-go">{{ s.retention.run }}</button></template>
    </Modal>

    <ConfirmDialog :open="!!removing" :title="s.retention.removeTitle(removing?.scope ?? '')" :confirm-label="s.retention.remove" danger :run="remove" @close="removing = null" @done="l.reload" />
  </div>
</template>
