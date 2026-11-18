<script setup lang="ts">
import { computed, ref } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import LoadState from '@jailops/shared/components/LoadState.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import type { DiffDataset, DiffEntry, Jail } from '@jailops/shared/api/types'

// What a thin jail holds on top of its image: the files it added, changed or removed (GET /jails/NAME/diff).
const props = defineProps<{ jail: Jail }>()
const ALL = 20000
const limit = ref<number | undefined>(undefined)
const packages = ref(false)
const l = useLoad(() => api.diff.jail(props.jail.metadata.name, { limit: limit.value, packages: packages.value }))
const r = computed(() => l.data.value)
const shown = computed(() => (r.value?.datasets ?? []).filter((d) => d.total > 0 || d.note))
const total = computed(() => (r.value?.datasets ?? []).reduce((n, d) => n + d.total, 0))
const truncated = computed(() => shown.value.some((d) => d.truncated))
const showAll = (): void => { limit.value = ALL; l.reload() }
const toggle = (): void => { l.reload() }

const since = (d: DiffDataset): string => (d.base === 'image' ? s.diff.sinceImage(r.value?.image ?? '') : d.base === 'baseline' ? s.diff.sinceMade : s.diff.sinceTree)
const mark = (e: DiffEntry): string => ({ added: '+', modified: 'M', removed: '-', renamed: 'R' })[e.kind] ?? '?'
const kind = (e: DiffEntry): string => s.diff.kinds[e.kind as keyof typeof s.diff.kinds] ?? e.kind
const tone = (e: DiffEntry): string => (e.kind === 'added' ? 'ok' : e.kind === 'removed' ? 'bad' : 'warn')
const counts = (d: DiffDataset): string => Object.entries(d.counts).filter(([, n]) => n > 0).map(([k, n]) => `${n} ${s.diff.kinds[k as keyof typeof s.diff.kinds] ?? k}`).join(', ')
</script>

<template>
  <section class="panel" aria-labelledby="diff-h" data-test="changes-tab">
    <header>
      <h2 id="diff-h">{{ s.diff.title }}</h2>
      <label class="check"><input v-model="packages" type="checkbox" data-test="diff-packages" @change="toggle" />{{ s.diff.packagesToggle }}</label>
    </header>
    <div class="body stack">
      <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!r" @retry="l.reload">
        <div v-if="r" class="stack">
          <p class="muted">{{ s.diff.lead(r.image) }}</p>
          <p v-if="!shown.length && total === 0" class="empty" data-test="diff-none">{{ s.diff.none }}</p>
          <section v-for="d in shown" :key="d.name" class="stack" :data-dataset="d.name">
            <h3><span class="mono">{{ d.path }}</span> <span class="muted">{{ since(d) }}</span><span v-if="d.total" class="badge info">{{ s.diff.total(d.total) }}</span></h3>
            <p v-if="d.total" class="muted">{{ counts(d) }}</p>
            <Callout v-if="d.note && d.base !== 'image-tree'" tone="info">{{ d.note }}</Callout>
            <ul v-if="d.entries.length" class="diff-list">
              <li v-for="e in d.entries" :key="e.kind + e.path" :data-kind="e.kind">
                <span class="pill" :class="tone(e)" :title="kind(e)">{{ mark(e) }}</span>
                <span class="mono">{{ e.path }}<template v-if="e.to"> → {{ e.to }}</template></span>
              </li>
            </ul>
            <p v-if="d.truncated" class="muted" data-test="diff-more">{{ s.diff.more(d.total - d.entries.length) }}</p>
          </section>
          <button v-if="truncated && !limit" class="btn btn-sm" type="button" data-test="diff-all" @click="showAll">{{ s.diff.showAll }}</button>
          <div v-if="r.packages" class="stack" data-test="diff-package-list">
            <h3>{{ s.diff.packages }}</h3>
            <Callout v-if="r.packages.note" tone="info">{{ r.packages.note }}</Callout>
            <p v-else-if="!r.packages.packages.length" class="empty">{{ s.diff.packagesNone }}</p>
            <ul v-else class="mono"><li v-for="p in r.packages.packages" :key="p">{{ p }}</li></ul>
          </div>
        </div>
      </LoadState>
    </div>
  </section>
</template>

<style scoped>
.diff-list { list-style: none; margin: 0; padding: 0; display: grid; gap: 6px; }
.diff-list li { display: flex; gap: 10px; align-items: baseline; overflow-wrap: anywhere; }
.diff-list .pill { min-width: 2em; text-align: center; }
</style>
