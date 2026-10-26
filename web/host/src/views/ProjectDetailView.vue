<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import { runOp } from '@jailops/shared/composables/ops'
import { isHub, hasExt } from '@jailops/shared/composables/session'
import { ApiError } from '@jailops/shared/api/client'
import { bytes } from '@jailops/shared/lib/format'
import Icon from '@jailops/shared/components/Icon.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import Modal from '@jailops/shared/components/Modal.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import LoadState from '@jailops/shared/components/LoadState.vue'
import QuotaBars from '@/components/QuotaBars.vue'
import ProjectForm from '@/components/ProjectForm.vue'
import { isGlobalAdmin, refreshProjects } from '@/composables/project'

const route = useRoute()
const router = useRouter()
const name = computed(() => String(route.params.name))
const l = useLoad(async () => {
  const [pr, networks, storage] = await Promise.all([api.projects.get(name.value), api.networks.list().catch(() => []), api.storage.list().catch(() => [])])
  const agg = isHub.value && hasExt('fleet_projects') && pr.data.spec.hosts?.length ? await api.projects.aggregate(name.value).catch(() => undefined) : undefined
  return { project: pr.data, etag: pr.etag, networks, storage, agg }
})
const p = computed(() => l.data.value?.project)
const canWrite = computed(() => isGlobalAdmin())
const editing = ref(false)
const deleting = ref(false)
const typed = ref('')
const error = ref<ApiError | null>(null)
const busy = ref(false)
const notFound = computed(() => l.error.value?.code === 'not_found')

async function del(): Promise<void> {
  busy.value = true
  error.value = null
  try {
    await runOp(s.projects.deleteTitle(name.value), () => api.projects.remove(name.value))
    await refreshProjects()
    await router.replace('/projects')
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div>
    <router-link class="back" to="/projects"><Icon name="chevronLeft" />{{ s.projects.back }}</router-link>
    <p v-if="notFound" class="empty">{{ s.projects.notFound }}</p>
    <LoadState :loading="l.loading.value" :error="notFound ? null : l.error.value" :has-data="!!p" @retry="l.reload">
      <div v-if="p" class="stack" :data-project-detail="p.metadata.name">
        <div class="page-head">
          <div class="row"><h1 class="mono">{{ p.metadata.name }}</h1><span v-if="p.spec.hosts?.length" class="badge info">{{ s.projects.fleet }}</span></div>
          <div class="row">
            <button class="btn" type="button" :disabled="!canWrite" data-test="edit-project" @click="editing = true">{{ s.projects.edit }}</button>
            <button class="btn btn-danger" type="button" :disabled="!canWrite" @click="deleting = true; typed = ''; error = null">{{ s.projects.delete }}</button>
          </div>
        </div>
        <p v-if="p.spec.description" class="muted">{{ p.spec.description }}</p>

        <section class="panel"><header><h2>{{ s.projects.quotas }}</h2></header>
          <div class="body"><QuotaBars :project="p" />
            <dl class="kv" style="margin-top: 8px"><dt>{{ s.projects.diskUsed }}</dt><dd class="mono">{{ bytes(p.status.disk_used) }}</dd><dt>{{ s.projects.memoryUsed }}</dt><dd class="mono">{{ bytes(p.status.memory_used) }}</dd><dt>{{ s.projects.jailsUsed }}</dt><dd class="mono">{{ p.status.jails }}</dd></dl>
            <p class="hint muted">{{ s.projects.sums }}</p></div>
        </section>

        <div class="grid-2">
          <section class="panel" data-section="networks"><header><h2>{{ s.projects.grantNetworks }}</h2></header>
            <p v-if="!p.spec.networks?.length" class="empty">-</p>
            <div v-else class="table-wrap"><table><thead><tr><th>{{ s.projects.network }}</th></tr></thead>
              <tbody><tr v-for="n in p.spec.networks" :key="n.network"><td class="mono">{{ n.network }}</td></tr></tbody></table></div>
          </section>
          <section class="panel" data-section="storage"><header><h2>{{ s.projects.grantStorage }}</h2></header>
            <p v-if="!p.spec.storage?.length" class="empty">-</p>
            <div v-else class="table-wrap"><table><thead><tr><th>{{ s.projects.storage }}</th><th>{{ s.projects.quotaField }}</th></tr></thead>
              <tbody><tr v-for="x in p.spec.storage" :key="x.storage"><td class="mono">{{ x.storage === '*' ? '* (' + s.projects.wildcard + ')' : x.storage }}</td><td class="mono">{{ x.quota || s.projects.unlimited }}</td></tr></tbody></table></div>
          </section>
        </div>

        <section class="panel"><header><h2>{{ s.projects.firewall }}</h2></header>
          <div class="body"><span v-if="p.spec.firewall" class="mono" data-test="mandatory-policy">{{ p.spec.firewall }}</span><span v-else class="faint">-</span>
            <p class="muted" style="margin-top: 6px">{{ s.projects.images }}: <span class="mono">{{ p.spec.images?.join(', ') || s.projects.allImages }}</span></p></div>
        </section>

        <section v-if="p.status.conditions?.length" class="panel"><header><h2>{{ s.projects.conditions }}</h2></header>
          <ul class="plain body"><li v-for="c in p.status.conditions" :key="c.type"><span class="pill" :class="c.status === 'True' ? 'ok' : 'danger'">{{ c.type }}</span> <span class="muted">{{ c.message }}</span></li></ul></section>

        <section v-if="p.spec.hosts?.length" class="panel" data-section="hosts">
          <header><h2>{{ s.projects.hostStatus }}</h2><span class="mono faint">{{ p.spec.hosts.join(', ') }}</span></header>
          <p v-if="!l.data.value?.agg" class="empty">{{ s.projects.hubOnly }}</p>
          <div v-else class="table-wrap"><table data-test="host-status">
            <thead><tr><th>{{ s.projects.host }}</th><th>{{ s.hosts.title }}</th><th>{{ s.projects.applied }}</th><th class="num">{{ s.projects.jailsUsed }}</th><th class="hide-sm">{{ s.projects.memoryUsed }}</th></tr></thead>
            <tbody>
              <tr v-for="h in l.data.value.agg.hosts" :key="h.host" :data-host="h.host" :class="{ drift: !!h.error || !h.online }">
                <td class="mono">{{ h.host }}</td>
                <td><span class="pill" :class="h.online ? 'ok' : 'danger'">{{ h.online ? s.projects.online : s.projects.offline }}</span></td>
                <td><span class="pill" :class="h.applied ? (h.in_sync ? 'ok' : 'warn') : 'danger'">{{ !h.applied ? s.projects.notApplied : h.in_sync ? s.projects.inSync : s.projects.outOfSync }}</span>
                  <div v-for="c in h.conditions ?? []" :key="c.type" class="faint">{{ c.type }}: {{ c.reason }} {{ c.message }}</div>
                  <div v-if="h.error" class="err" style="color: var(--danger)"><span class="mono">{{ h.error.code }}</span> {{ h.error.message }}</div></td>
                <td class="num mono">{{ h.status?.jails ?? '-' }}</td><td class="mono hide-sm">{{ h.status ? bytes(h.status.memory_used) : '-' }}</td>
              </tr>
              <tr><th scope="row" colspan="3">{{ s.projects.total }}</th><td class="num mono">{{ l.data.value.agg.total.jails }}</td><td class="mono hide-sm">{{ bytes(l.data.value.agg.total.memory_used) }}</td></tr>
            </tbody></table></div>
        </section>

        <ProjectForm :open="editing" :project="p" :etag="l.data.value?.etag" :networks="l.data.value?.networks ?? []" :storage="l.data.value?.storage ?? []" @close="editing = false" @done="l.reload" />
        <Modal :open="deleting" :title="s.projects.deleteTitle(p.metadata.name)" @close="deleting = false">
          <form id="dp" @submit.prevent="del">
            <Callout tone="danger">{{ s.projects.deleteText }}</Callout>
            <div class="field" style="margin-top: 12px"><label for="dp-typed">{{ s.projects.typeName(p.metadata.name) }}</label><input id="dp-typed" v-model="typed" class="mono" type="text" autocomplete="off" spellcheck="false" /></div>
            <ErrorPanel v-if="error" :error="error" />
          </form>
          <template #footer><button class="btn" type="button" @click="deleting = false">{{ s.app.cancel }}</button><button class="btn btn-danger" type="submit" form="dp" :disabled="busy || typed !== p.metadata.name" data-test="project-delete-go">{{ s.projects.delete }}</button></template>
        </Modal>
      </div>
    </LoadState>
  </div>
</template>
