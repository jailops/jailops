<script setup lang="ts">
import { ref } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { useLoad } from '@jailops/shared/composables/load'
import { can } from '@jailops/shared/composables/session'
import { runOp } from '@jailops/shared/composables/ops'
import { stamp } from '@jailops/shared/lib/format'
import Icon from '@jailops/shared/components/Icon.vue'
import Modal from '@jailops/shared/components/Modal.vue'
import ConfirmDialog from '@jailops/shared/components/ConfirmDialog.vue'
import Callout from '@jailops/shared/components/Callout.vue'
import LoadState from '@jailops/shared/components/LoadState.vue'
import type { Image } from '@jailops/shared/api/types'

const l = useLoad(() => api.images.list())

function aliasesOf(img: Image, n: number): string[] {
  return Object.entries(img.status.aliases ?? {}).filter(([, g]) => g === n).map(([a]) => a)
}
function build(img: Image): void {
  const last = Math.max(0, ...(img.status.generations ?? []).map((g) => g.number))
  runOp(`${s.images.build}: ${img.metadata.name}`, () => api.images.build(img.metadata.name, { from_generation: last || undefined })).catch(() => undefined)
}

function upgrade(img: Image): void {
  runOp(`${s.images.upgrade}: ${img.metadata.name}`, () => api.images.upgrade(img.metadata.name, {})).catch(() => undefined)
}

const pinning = ref<{ image: string; generation: number } | null>(null)
const aliasName = ref('')
const removingAlias = ref<{ image: string; alias: string } | null>(null)
function pin(img: Image, generation: number): void {
  aliasName.value = ''
  pinning.value = { image: img.metadata.name, generation }
}
const savePin = async (): Promise<void> => {
  const p = pinning.value!
  await runOp(`${s.images.setAlias}: ${p.image} ${aliasName.value}`, () => api.images.setAlias(p.image, aliasName.value, p.generation))
  pinning.value = null
  l.reload()
}
const removeAlias = () => runOp(s.images.deleteAliasTitle(removingAlias.value!.alias), () => api.images.deleteAlias(removingAlias.value!.image, removingAlias.value!.alias))
</script>

<template>
  <div>
    <div class="page-head"><h1>{{ s.images.title }}</h1></div>
    <LoadState :loading="l.loading.value" :error="l.error.value" :has-data="!!l.data.value" @retry="l.reload">
      <p v-if="!l.data.value?.length" class="empty">{{ s.images.empty }}</p>
      <div class="stack">
        <section v-for="img in l.data.value" :key="img.metadata.name" class="panel" :data-image="img.metadata.name">
          <header>
            <div class="row">
              <h2 class="mono">{{ img.metadata.name }}</h2>
              <span class="faint mono">{{ img.spec.os }} {{ img.spec.release }} {{ img.spec.arch }}</span>
              <span v-for="(g, a) in img.status.aliases" :key="a" class="pill info">{{ a }} = g{{ g }}<button v-if="can('image_aliases')" class="btn btn-sm" type="button" :title="s.images.deleteAlias" :aria-label="s.images.deleteAlias" :data-test="`delete-alias-${a}`" style="margin-left: 4px" @click="removingAlias = { image: img.metadata.name, alias: String(a) }"><Icon name="x" /></button></span>
            </div>
            <div class="row">
              <button v-if="can('generations')" class="btn btn-sm" type="button" @click="build(img)"><Icon name="plus" />{{ s.images.build }}</button>
              <button v-if="can('image_upgrade')" class="btn btn-sm" type="button" :title="s.images.upgradeHint" :data-test="`upgrade-${img.metadata.name}`" @click="upgrade(img)"><Icon name="play" />{{ s.images.upgrade }}</button>
            </div>
          </header>
          <div class="body muted">
            {{ s.images.source }}: <span class="mono">{{ img.spec.source.type }}</span><span v-if="img.spec.source.repo" class="mono"> {{ img.spec.source.repo }}</span>
            <span v-if="img.spec.retention?.keep_last || img.spec.retention?.keep_days"> · {{ s.images.retention }}: {{ s.images.keep(img.spec.retention.keep_last ?? 0, img.spec.retention.keep_days ?? 0) }}</span>
          </div>
          <div class="table-wrap">
            <table>
              <thead><tr><th>{{ s.images.generations }}</th><th class="hide-sm">{{ s.images.created }}</th><th class="hide-sm">{{ s.images.manifest }}</th><th class="num">{{ s.images.packagesCol }}</th><th>{{ s.images.usedBy }}</th><th v-if="can('image_aliases')"></th></tr></thead>
              <tbody>
                <tr v-for="g in [...(img.status.generations ?? [])].sort((a, b) => b.number - a.number)" :key="g.number">
                  <td class="nowrap"><span class="mono">g{{ g.number }}</span> <span v-if="g.parent" class="faint">{{ s.images.parent(g.parent) }}</span><span v-for="a in aliasesOf(img, g.number)" :key="a" class="pill info" style="margin-left: 6px">{{ a }}</span></td>
                  <td class="faint hide-sm nowrap">{{ stamp(g.created_at) }}</td>
                  <td class="mono faint hide-sm" :title="g.manifest">{{ g.manifest.slice(0, 12) }}</td>
                  <td class="num">{{ g.packages ?? '-' }}</td>
                  <td>
                    <span v-if="!g.refs?.length" class="faint">{{ s.images.unused }}</span>
                    <template v-else><router-link v-for="r in g.refs" :key="r" class="mono refl" :to="`/jails/${r}`">{{ r }}</router-link></template>
                  </td>
                  <td v-if="can('image_aliases')" class="num"><button class="btn btn-sm" type="button" :data-test="`set-alias-g${g.number}`" @click="pin(img, g.number)">{{ s.images.setAlias }}</button></td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </div>
    </LoadState>
    <Modal :open="!!pinning" :title="s.images.setAliasTitle(pinning?.generation ?? 0)" @close="pinning = null">
      <form id="alias-form" @submit.prevent="savePin">
        <div class="field"><label for="alias-name">{{ s.images.aliasName }}</label><input id="alias-name" v-model="aliasName" class="mono" type="text" required pattern="[a-z0-9][a-z0-9._-]{0,31}" /><span class="hint">{{ s.images.aliasHint }}</span></div>
      </form>
      <template #footer><button class="btn" type="button" @click="pinning = null">{{ s.app.cancel }}</button><button class="btn btn-primary" type="submit" form="alias-form" :disabled="!aliasName">{{ s.images.setAlias }}</button></template>
    </Modal>
    <ConfirmDialog :open="!!removingAlias" :title="s.images.deleteAliasTitle(removingAlias?.alias ?? '')" :confirm-label="s.images.deleteAlias" danger :run="removeAlias" @close="removingAlias = null" @done="l.reload"><Callout tone="warn">{{ s.images.deleteAliasText }}</Callout></ConfirmDialog>
  </div>
</template>

<style>
.refl { margin-right: 10px; }
</style>
