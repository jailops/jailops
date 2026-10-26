<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import { runOp } from '@jailops/shared/composables/ops'
import { ApiError } from '@jailops/shared/api/client'
import { bytes, percent } from '@jailops/shared/lib/format'
import Icon from '@jailops/shared/components/Icon.vue'
import Meter from '@jailops/shared/components/Meter.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import Modal from '@jailops/shared/components/Modal.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import ConfirmDialog from '@jailops/shared/components/ConfirmDialog.vue'
import LoadState from '@jailops/shared/components/LoadState.vue'
import { isGlobalAdmin, projectState } from '@/composables/project'
import type { Storage } from '@jailops/shared/api/types'

const l = useLoad(async () => ({ storage: await api.storage.list(), projects: await api.projects.list().catch(() => []), jails: projectState.current ? await api.jails.list().catch(() => []) : [] }))
const canWrite = computed(() => isGlobalAdmin())

/** Projects that may place data on a storage: an explicit grant or the "*" wildcard (storage only). */
function projectsOf(name: string): string[] {
  return (l.data.value?.projects ?? []).filter((p) => (p.spec.storage ?? []).some((g) => g.storage === name || g.storage === '*')).map((p) => p.metadata.name)
}
const jailsOn = (name: string) => (l.data.value?.jails ?? []).filter((j) => j.spec.storage === name).length
const healthTone = (h: string | undefined) => (h === 'ONLINE' ? 'ok' : h === 'DEGRADED' ? 'warn' : h ? 'danger' : '')

const creating = ref(false)
const form = reactive({ name: '', dataset: '', description: '', compression: '' })
const busy = ref(false)
const error = ref<ApiError | null>(null)
function open(): void {
  Object.assign(form, { name: '', dataset: '', description: '', compression: '' })
  error.value = null
  creating.value = true
}
async function create(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    await runOp(s.storage.createTitle + ': ' + form.name, () => api.storage.create({ metadata: { name: form.name }, spec: { dataset: form.dataset, ...(form.description ? { description: form.description } : {}), ...(form.compression ? { compression: form.compression } : {}), } }))
    creating.value = false
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
const removing = ref<Storage | null>(null)
const remove = () => runOp(s.storage.deleteTitle(removing.value!.metadata.name), () => api.storage.remove(removing.value!.metadata.name))
</script>

<template>
  <div>
    <div class="page-head"><h1>{{ s.storage.title }}</h1><button class="btn btn-primary" type="button" :disabled="!canWrite" data-test="new-storage" @click="open"><Icon name="plus" />{{ s.storage.create }}</button></div>
    <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!l.data.value" @retry="l.reload">
      <p v-if="!l.data.value?.storage.length" class="empty panel">{{ s.storage.empty }}</p>
      <div class="stack">
        <section v-for="x in l.data.value?.storage" :key="x.metadata.name" class="panel" :data-storage="x.metadata.name">
          <header>
            <div class="row"><h2 class="mono">{{ x.metadata.name }}</h2><span class="mono faint">{{ x.spec.dataset }}</span><span v-if="x.status.health" class="pill" :class="healthTone(x.status.health)" data-test="health">{{ x.status.health }}</span><span v-if="x.spec.encrypted" class="badge info">{{ s.storage.encrypted }}</span></div>
            <button class="btn btn-sm btn-danger" type="button" :disabled="!canWrite" @click="removing = x">{{ s.storage.delete }}</button>
          </header>
          <div class="body stack">
            <div v-if="x.status.size">
              <Meter :value="percent(x.status.used ?? 0, x.status.size)" :label="s.storage.usage" :warn-at="75" :danger-at="90" />
              <p class="muted mono" style="margin-top: 4px">{{ bytes(x.status.used) }} {{ s.storage.used.toLowerCase() }} · {{ bytes(x.status.available) }} {{ s.storage.available.toLowerCase() }} · {{ bytes(x.status.size) }}<span v-if="x.status.fragmentation"> · {{ s.storage.fragmentation }} {{ x.status.fragmentation }}%</span></p>
            </div>
            <dl class="kv">
              <dt>{{ s.storage.pool }}</dt><dd class="mono">{{ x.status.pool || '-' }}</dd>
              <dt v-if="x.spec.compression">{{ s.storage.compression }}</dt><dd v-if="x.spec.compression" class="mono">{{ x.spec.compression }}</dd>
              <dt>{{ s.storage.projects }}</dt>
              <dd data-test="storage-projects"><span v-for="n in projectsOf(x.metadata.name)" :key="n" class="chip mono">{{ n }}</span><span v-if="!projectsOf(x.metadata.name).length" class="faint">{{ s.storage.noProjects }}</span></dd>
              <dt>{{ s.storage.jails }}</dt><dd class="mono">{{ projectState.current ? jailsOn(x.metadata.name) : '-' }}</dd>
            </dl>
          </div>
        </section>
      </div>
    </LoadState>

    <Modal :open="creating" :title="s.storage.createTitle" @close="creating = false">
      <form id="sc" @submit.prevent="create">
        <div class="field"><label for="sc-name">{{ s.storage.name }}</label><input id="sc-name" v-model="form.name" class="mono" type="text" required pattern="[a-z0-9._\-]+" /></div>
        <div class="field"><label for="sc-ds">{{ s.storage.dataset }}</label><input id="sc-ds" v-model="form.dataset" class="mono" type="text" required placeholder="tank/jailops" /><span class="hint">{{ s.storage.datasetHint }}</span></div>
        <div class="field"><label for="sc-desc">{{ s.storage.description }}</label><input id="sc-desc" v-model="form.description" type="text" /></div>
        <div class="field"><label for="sc-comp">{{ s.storage.compression }}</label><select id="sc-comp" v-model="form.compression"><option value="">-</option><option>zstd</option><option>lz4</option></select></div>
        <ErrorPanel v-if="error" :error="error" />
      </form>
      <template #footer><button class="btn" type="button" @click="creating = false">{{ s.app.cancel }}</button><button class="btn btn-primary" type="submit" form="sc" :disabled="busy || !form.name || !form.dataset">{{ s.storage.save }}</button></template>
    </Modal>
    <ConfirmDialog :open="!!removing" :title="s.storage.deleteTitle(removing?.metadata.name ?? '')" :confirm-label="s.storage.delete" danger :run="remove" @close="removing = null"><Callout tone="warn">{{ s.storage.deleteText }}</Callout></ConfirmDialog>
  </div>
</template>
