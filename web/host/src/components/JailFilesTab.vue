<script setup lang="ts">
import { computed, ref } from 'vue'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { bytes } from '@jailops/shared/lib/format'
import Callout from '@jailops/shared/components/Callout.vue'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import { projectState } from '@/composables/project'
import type { Jail } from '@jailops/shared/api/types'

// A file between the browser and a running jail: GET and PUT of /jails/NAME/files/PATH (`jailops cp`). A download is
// one file; a directory is copied with `jailops cp -r` from a terminal.
const props = defineProps<{ jail: Jail }>()
const name = computed(() => props.jail.metadata.name)
const running = computed(() => props.jail.status.power === 'running')
const project = computed(() => props.jail.metadata.project || projectState.current)

const getPath = ref('')
const putPath = ref('')
const picked = ref<File | null>(null)
const chooser = ref<HTMLInputElement | null>(null)
const busy = ref(false)
const error = ref<ApiError | null>(null)
const note = ref('')

const fail = (e: unknown) => {
  error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
}
const slash = (p: string) => (p.startsWith('/') ? p : '/' + p)
const base = (p: string) => p.slice(p.lastIndexOf('/') + 1)

async function download(): Promise<void> {
  error.value = null
  note.value = ''
  const p = slash(getPath.value.trim())
  if (p === '/') return
  busy.value = true
  try {
    const st = await api.files.stat(name.value, p)
    if (st.type === 'none') throw new ApiError({ code: 'not_found', message: s.files.missing(p) })
    if (st.type !== 'file') throw new ApiError({ code: 'invalid', message: s.files.notAFile(p, st.type) })
    const blob = await api.files.download(name.value, p)
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = base(p)
    document.body.appendChild(a)
    a.click()
    a.remove()
    setTimeout(() => URL.revokeObjectURL(url), 10_000)
    note.value = `${base(p)} (${bytes(blob.size)})`
  } catch (e) {
    fail(e)
  } finally {
    busy.value = false
  }
}

function choose(ev: Event): void {
  picked.value = (ev.target as HTMLInputElement).files?.[0] ?? null
  if (picked.value && !putPath.value) putPath.value = '/tmp/'
}

async function upload(): Promise<void> {
  error.value = null
  note.value = ''
  const f = picked.value
  if (!f || !putPath.value.trim()) return
  let to = slash(putPath.value.trim())
  if (to.endsWith('/')) to += f.name
  busy.value = true
  try {
    await api.files.upload(name.value, to, f)
    note.value = s.files.sent(f.name, bytes(f.size), to)
    picked.value = null
    if (chooser.value) chooser.value.value = ''
  } catch (e) {
    fail(e)
  } finally {
    busy.value = false
  }
}
const cli = computed(() => {
  const p = project.value ? ` --project ${project.value}` : ''
  return `jailops cp ./FILE ${name.value}:/tmp/${p}`
})
</script>

<template>
  <section class="panel" data-test="files-tab">
    <header><h2>{{ s.files.tab }}</h2></header>
    <div class="stack" style="padding: 0 16px 16px">
      <p class="muted">{{ s.files.lead }}</p>
      <Callout v-if="!running" tone="info">{{ s.files.notRunning }}</Callout>
      <ErrorPanel v-if="error" :error="error" />
      <Callout v-if="note" tone="ok" data-test="files-note">{{ note }}</Callout>
      <form class="stack" novalidate data-test="files-download" @submit.prevent="download">
        <div class="field">
          <label for="f-get">{{ s.files.downloadHint }}</label>
          <input id="f-get" v-model.trim="getPath" class="mono" type="text" autocomplete="off" spellcheck="false" placeholder="/etc/rc.conf" data-test="f-get" />
        </div>
        <div><button class="btn" type="submit" :disabled="busy || !running || !getPath.trim()" data-test="f-download">{{ s.files.download }}</button></div>
      </form>
      <form class="stack" novalidate data-test="files-upload" @submit.prevent="upload">
        <div class="field">
          <label for="f-file">{{ s.files.choose }}</label>
          <input id="f-file" ref="chooser" type="file" data-test="f-file" @change="choose" />
        </div>
        <div class="field">
          <label for="f-put">{{ s.files.uploadTo }}</label>
          <input id="f-put" v-model.trim="putPath" class="mono" type="text" autocomplete="off" spellcheck="false" placeholder="/tmp/" data-test="f-put" />
          <span class="hint faint">{{ s.files.uploadHint }}</span>
        </div>
        <div><button class="btn btn-primary" type="submit" :disabled="busy || !running || !picked || !putPath.trim()" data-test="f-upload">{{ s.files.send }}</button></div>
      </form>
      <p class="faint" style="margin: 0; font-size: 12px"><span>{{ s.files.cli }}:</span> <code class="mono">{{ cli }}</code></p>
    </div>
  </section>
</template>
