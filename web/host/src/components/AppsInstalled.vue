<script setup lang="ts">
import { s } from '@/i18n'
import { can } from '@jailops/shared/composables/session'
import { projectState } from '@/composables/project'
import { LABEL_APP, LABEL_HASH, LABEL_VERSION, openLinks } from '@/lib/apps'
import type { ApiError } from '@jailops/shared/api/client'
import Icon from '@jailops/shared/components/Icon.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import LoadState from '@jailops/shared/components/LoadState.vue'
import AddrCopy from '@jailops/shared/components/AddrCopy.vue'
import AppIcon from '@/components/AppIcon.vue'
import StatePill from '@/components/StatePill.vue'
import type { AppDetail, Jail } from '@jailops/shared/api/types'

// The jails of the project that carry the label jailops.app: the project's jail list narrowed on the
// client, as the command line does. Ports and links come from the app's definition in the catalog.
defineProps<{ jails: Jail[]; loading: boolean; error: ApiError | null; details: Record<string, AppDetail> }>()
defineEmits<{ retry: [] }>()
const appOf = (j: Jail) => j.metadata.labels?.[LABEL_APP] ?? ''
</script>

<template>
  <div data-test="apps-installed">
    <Callout v-if="can('projects') && !projectState.current" tone="info">{{ s.apps.needProject }}</Callout>
    <LoadState v-else :loading="loading" :error="error" :has-data="!loading || !!jails.length" @retry="$emit('retry')">
      <p class="muted">{{ s.apps.installedLead }}</p>
      <div class="panel">
        <p v-if="!jails.length" class="empty" data-test="installed-empty">{{ s.apps.noInstalled }}</p>
        <div v-else class="table-wrap">
          <table data-test="installed-table">
            <thead>
              <tr>
                <th>{{ s.apps.app }}</th>
                <th>{{ s.apps.jail }}</th>
                <th class="hide-sm">{{ s.install.version }}</th>
                <th>{{ s.apps.state }}</th>
                <th class="hide-sm">{{ s.apps.addresses }}</th>
                <th class="hide-sm">{{ s.apps.ports }}</th>
                <th><span class="sr-only">{{ s.apps.open }}</span></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="j in jails" :key="j.metadata.name" :data-installed="j.metadata.name">
                <td>
                  <div class="inst-app">
                    <AppIcon :name="appOf(j)" :title="details[appOf(j)]?.definition.title" :icon="details[appOf(j)]?.icon" :size="28" />
                    <div>
                      <router-link :to="`/apps/${appOf(j)}`">{{ details[appOf(j)]?.definition.title || appOf(j) }}</router-link>
                      <span v-if="details[appOf(j)]?.source === 'local'" class="badge info inst-changed" :title="s.apps.localTitle" data-test="app-local">{{ s.apps.local }}</span>
                      <span v-if="details[appOf(j)] && j.metadata.labels?.[LABEL_HASH] && j.metadata.labels[LABEL_HASH] !== details[appOf(j)].hash" class="badge warn inst-changed" :title="s.apps.changedTitle">{{ s.apps.changed }}</span>
                    </div>
                  </div>
                </td>
                <td><router-link class="mono" :to="`/jails/${j.metadata.name}`">{{ j.metadata.name }}</router-link></td>
                <td class="mono hide-sm">{{ j.metadata.labels?.[LABEL_VERSION] || '-' }}</td>
                <td><StatePill :state="j.status.power" /></td>
                <td class="hide-sm"><span v-if="!j.status.addresses?.length" class="faint">-</span><div v-for="a in j.status.addresses" :key="a"><AddrCopy :value="a" /></div></td>
                <td class="hide-sm"><span v-for="p in details[appOf(j)]?.definition.ports ?? []" :key="p.port + p.proto" class="chip mono" :title="p.description">{{ p.name }} {{ p.port }}/{{ p.proto }}</span></td>
                <td class="nowrap">
                  <a v-for="o in openLinks(j, details[appOf(j)]?.definition.ports)" :key="o.url" class="btn btn-sm" :href="o.url" target="_blank" rel="noopener noreferrer" :title="s.apps.openTitle(o.url)" :aria-label="s.apps.openTitle(o.url)" :data-test="`open-${j.metadata.name}`"><Icon name="externalLink" />{{ s.apps.open }}</a>
                  <router-link class="btn btn-sm btn-ghost" :to="{ path: `/jails/${j.metadata.name}`, query: { tab: 'logs' } }"><Icon name="fileText" />{{ s.logs.tab }}</router-link>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </LoadState>
  </div>
</template>

<style>
.inst-app { display: flex; gap: 8px; align-items: center; }
.inst-changed { margin-left: 6px; }
</style>
