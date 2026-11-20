<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from 'vue'
import { s } from '@/i18n'
import { api, type NotesKind, type NotesObject } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'
import { bytes } from '@jailops/shared/lib/format'
import { MAX_NOTES, notesBytes } from '@/lib/notes'
import ErrorPanel from '@jailops/shared/components/ErrorPanel.vue'
import Icon from '@jailops/shared/components/Icon.vue'
import NotesMarkdown from '@/components/NotesMarkdown'

// The notes of one object (spec.notes): the text as the reader sees it, and for whoever may change the object an
// editor that saves the text alone with a merge patch under If-Match. A change made meanwhile is refused (412) and the
// typed text stays in the editor. Compact is the form for a card in a list: no frame, and only a button while empty.
const props = withDefaults(defineProps<{
  kind: NotesKind
  name: string
  notes?: string
  generation?: number
  editable?: boolean
  empty: string
  compact?: boolean
}>(), { notes: '', generation: undefined, editable: false, compact: false })
const emit = defineEmits<{ saved: [obj: NotesObject] }>()

const id = useId()
const text = ref(props.notes ?? '')
const gen = ref(props.generation)
const editing = ref(false)
const draft = ref('')
const busy = ref(false)
const error = ref<ApiError | null>(null)
const status = ref('')
const area = ref<HTMLTextAreaElement | null>(null)
const editBtn = ref<HTMLButtonElement | null>(null)

// a reload of the page brings the object as it is now; an open editor keeps what is being typed
watch(() => [props.notes, props.generation] as const, ([n, g]) => {
  gen.value = g
  if (!editing.value) text.value = n ?? ''
})

const clean = (t: string) => t.replace(/\r\n?/g, '\n').replace(/\s+$/, '')
const used = computed(() => notesBytes(clean(draft.value)))
// the server's rule (api.ValidNotes): line feeds and tabs, no other control or invisible character
const unsafe = /[\u0000-\u0008\u000b-\u001f\u007f-\u009f​-‏‪-‮⁠⁦-⁩﻿]/
const problem = computed(() => used.value > MAX_NOTES ? s.notes.tooLong(bytes(MAX_NOTES)) : unsafe.test(clean(draft.value)) ? s.notes.unsafe : '')

async function open(): Promise<void> {
  draft.value = text.value
  error.value = null
  status.value = ''
  editing.value = true
  await nextTick()
  area.value?.focus()
}

async function close(): Promise<void> {
  editing.value = false
  error.value = null
  await nextTick()
  editBtn.value?.focus()
}

async function save(): Promise<void> {
  if (busy.value || problem.value) return
  const next = clean(draft.value)
  if (next === text.value) return close()
  busy.value = true
  error.value = null
  status.value = ''
  try {
    const r = await api.patchNotes(props.kind, props.name, next, gen.value)
    const o = r.kind === 'sync' ? r.data : undefined
    text.value = o?.spec ? (o.spec.notes ?? '') : next
    if (o?.metadata?.generation) gen.value = o.metadata.generation
    status.value = next ? s.notes.saved : s.notes.cleared
    emit('saved', o ?? {})
    await close()
  } catch (e) {
    error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
  } finally {
    busy.value = false
  }
}

function key(ev: KeyboardEvent): void {
  if (ev.key === 'Enter' && (ev.ctrlKey || ev.metaKey)) {
    ev.preventDefault()
    void save()
  } else if (ev.key === 'Escape') {
    ev.preventDefault()
    ev.stopPropagation()
    void close()
  }
}
</script>

<template>
  <section :class="compact ? 'notes-panel notes-compact' : 'panel notes-panel'" :aria-labelledby="`${id}-h`" data-test="notes-panel">
    <header :class="{ 'notes-head': compact }">
      <component :is="compact ? 'h3' : 'h2'" :id="`${id}-h`">{{ s.notes.title }}</component>
      <button v-if="editable && !editing && (text || !compact)" ref="editBtn" class="btn btn-sm" type="button" data-test="notes-edit" @click="open"><Icon name="fileText" />{{ text ? s.notes.edit : s.notes.add }}</button>
    </header>
    <div :class="compact ? 'notes-body' : 'body notes-body'">
      <template v-if="!editing">
        <NotesMarkdown v-if="text" :text="text" data-test="notes-view" />
        <button v-else-if="compact && editable" ref="editBtn" class="btn btn-sm btn-ghost notes-add" type="button" data-test="notes-edit" @click="open"><Icon name="plus" />{{ s.notes.add }}</button>
        <p v-else-if="editable" class="faint notes-empty" data-test="notes-empty">{{ empty }}</p>
        <p v-else class="faint notes-empty" data-test="notes-empty">{{ s.notes.none }}</p>
      </template>
      <form v-else class="notes-form" novalidate @submit.prevent="save">
        <div class="field">
          <label :for="`${id}-t`">{{ s.notes.field(name) }}</label>
          <textarea :id="`${id}-t`" ref="area" v-model="draft" rows="14" spellcheck="true" :placeholder="empty" :aria-invalid="!!problem" :aria-describedby="`${id}-k ${id}-e`" data-test="notes-input" @keydown="key" />
          <span :id="`${id}-k`" class="hint">{{ s.notes.keys }} <span class="notes-size" data-test="notes-size">{{ s.notes.size(bytes(used), bytes(MAX_NOTES)) }}</span></span>
          <span :id="`${id}-e`" class="field-error" role="alert">{{ problem }}</span>
        </div>
        <div class="notes-actions">
          <button class="btn btn-primary" type="submit" :disabled="busy" :aria-disabled="!!problem" data-test="notes-save">{{ s.notes.save }}</button>
          <button class="btn" type="button" :disabled="busy" data-test="notes-cancel" @click="close">{{ s.notes.cancel }}</button>
        </div>
        <ErrorPanel v-if="error" :error="error" />
      </form>
      <span class="sr-only" role="status" aria-live="polite" data-test="notes-status">{{ status }}</span>
    </div>
  </section>
</template>

<style>
.notes-panel { min-width: 0; }
.notes-compact { display: grid; gap: 6px; }
.notes-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; flex-wrap: wrap; }
.notes-body { min-width: 0; overflow-wrap: anywhere; }
.notes-empty { margin: 0; }
.notes-form textarea { width: 100%; min-height: 12em; }
.notes-form .field { margin-bottom: 8px; }
.notes-size { white-space: nowrap; }
.notes-actions { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 8px; }
.notes-md { line-height: 1.5; }
.notes-md > :first-child { margin-top: 0; }
.notes-md > :last-child { margin-bottom: 0; }
.notes-md p, .notes-md ul, .notes-md ol { margin: 0 0 8px; }
.notes-md ul, .notes-md ol { padding-left: 22px; }
.notes-md .notes-h { margin: 12px 0 6px; color: var(--text); text-transform: none; letter-spacing: 0; }
.notes-md h3.notes-h { font-size: 16px; }
.notes-md h4.notes-h { font-size: 14px; }
.notes-md h5.notes-h { font-size: 13px; }
.notes-md .notes-pre { margin: 0 0 8px; padding: 6px 10px; background: var(--surface-3); border-radius: var(--radius); font-family: var(--font-mono); font-size: 12.5px; white-space: pre; overflow-x: auto; max-width: 100%; }
.notes-md .notes-code { padding: 0 4px; background: var(--surface-3); border-radius: 3px; font-family: var(--font-mono); font-size: 0.92em; overflow-wrap: anywhere; }
.notes-md a { overflow-wrap: anywhere; }
</style>
