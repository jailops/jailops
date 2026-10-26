<script setup lang="ts">
import { computed, ref } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import { runOp } from '@jailops/shared/composables/ops'
import { session } from '@jailops/shared/composables/session'
import { ApiError } from '@jailops/shared/api/client'
import { stamp, ago } from '@jailops/shared/lib/format'
import Modal from '@jailops/shared/components/Modal.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import ConfirmDialog from '@jailops/shared/components/ConfirmDialog.vue'
import LoadState from '@jailops/shared/components/LoadState.vue'
import RoleNote from '@/components/RoleNote.vue'
import { allows } from '@/lib/roles'
import type { Snapshot } from '@/types'

interface Row extends Snapshot { _group: string }
const l = useLoad(async () => {
  const groups = (await api.groups.list()).filter((g) => g.pending_deletion > 0)
  const all = await Promise.all(groups.map(async (g) => (await api.groups.snapshots(g.group_id)).map((x) => ({ ...x, _group: g.group_id }) as Row)))
  return all.flat().filter((x) => x.pending_deletion_after)
})
const operate = computed(() => allows(session.role, 'backup-admin'))
const rows = computed(() => [...(l.data.value ?? [])].sort((a, b) => (a.pending_deletion_after ?? '').localeCompare(b.pending_deletion_after ?? '')))
const ready = (x: Row) => !!x.pending_deletion_after && Date.parse(x.pending_deletion_after) <= Date.now()

const cancelling = ref<Row | null>(null)
const executing = ref<Row | null>(null)
const typed = ref('')
const busy = ref(false)
const error = ref<ApiError | null>(null)
const doCancel = () => runOp(s.deletions.cancelTitle(cancelling.value!.snapshot_id), () => api.groups.cancelDeletion(cancelling.value!._group, cancelling.value!.snapshot_id))
function openExec(x: Row): void {
  executing.value = x
  typed.value = ''
  error.value = null
}
async function exec(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    await runOp(s.deletions.executeTitle(executing.value!.snapshot_id), () => api.groups.executeDeletion(executing.value!._group, executing.value!.snapshot_id))
    executing.value = null
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div>
    <div class="page-head"><h1>{{ s.deletions.title }}</h1></div>
    <Callout style="margin-bottom: 12px">{{ s.deletions.lead }}</Callout>
    <RoleNote need="backup-admin" />
    <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!l.data.value" @retry="l.reload">
      <div class="panel" style="margin-top: 12px">
        <p v-if="!rows.length" class="empty">{{ s.deletions.empty }}</p>
        <div v-else class="table-wrap"><table>
          <thead><tr><th>{{ s.deletions.snapshot }}</th><th class="hide-sm">{{ s.deletions.requestedBy }}</th><th>{{ s.deletions.executes }}</th><th>{{ s.deletions.state }}</th><th></th></tr></thead>
          <tbody><tr v-for="x in rows" :key="x._group + x.snapshot_id" :data-deletion="x.snapshot_id">
            <td><span class="mono">{{ x.snapshot_id }}</span><br /><span class="faint mono">{{ x._group }}</span></td>
            <td class="hide-sm"><span class="mono">{{ x.pending_deletion_by }}</span><br /><span class="faint">{{ ago(x.pending_deletion_requested_at) }}</span></td>
            <td class="nowrap">{{ stamp(x.pending_deletion_after) }}</td>
            <td><span class="pill" :class="ready(x) ? 'danger' : 'warn'">{{ ready(x) ? s.deletions.ready : s.deletions.waiting }}</span></td>
            <td class="nowrap">
              <button class="btn btn-sm" type="button" :disabled="!operate" @click="cancelling = x" :aria-label="`${s.deletions.cancel} ${x.snapshot_id}`">{{ s.deletions.cancel }}</button>
              <button class="btn btn-sm btn-danger" type="button" :disabled="!operate || !ready(x)" data-test="execute" :aria-label="`${s.deletions.execute} ${x.snapshot_id}`" @click="openExec(x)">{{ s.deletions.execute }}</button>
            </td>
          </tr></tbody></table></div>
      </div>
    </LoadState>

    <ConfirmDialog :open="!!cancelling" :title="s.deletions.cancelTitle(cancelling?.snapshot_id ?? '')" :confirm-label="s.deletions.cancel" :run="doCancel" @close="cancelling = null" @done="l.reload" />
    <Modal :open="!!executing" :title="s.deletions.executeTitle(executing?.snapshot_id ?? '')" @close="executing = null">
      <form id="ex" @submit.prevent="exec">
        <Callout tone="danger">{{ s.deletions.executeText }}</Callout>
        <div class="field" style="margin-top: 12px"><label for="ex-id">{{ s.deletions.typeId(executing?.snapshot_id ?? '') }}</label><input id="ex-id" v-model="typed" class="mono" type="text" autocomplete="off" spellcheck="false" /></div>
        <ErrorPanel v-if="error" :error="error" />
      </form>
      <template #footer><button class="btn" type="button" @click="executing = null">{{ s.app.cancel }}</button><button class="btn btn-danger" type="submit" form="ex" :disabled="busy || typed !== executing?.snapshot_id" data-test="execute-go">{{ s.deletions.execute }}</button></template>
    </Modal>
  </div>
</template>
