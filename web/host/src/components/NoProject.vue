<script setup lang="ts">
import { s } from '@/i18n'
import { projectState } from '@/composables/project'
import { can } from '@jailops/shared/composables/session'
import Callout from '@jailops/shared/components/Callout.vue'
</script>

<template>
  <div data-test="no-project">
    <div class="page-head"><h1>{{ s.project.noneChosen }}</h1></div>
    <Callout tone="info">{{ s.project.noneChosenText }}</Callout>
    <p v-if="projectState.loaded && !projectState.projects.length" class="muted" style="margin-top: 12px">{{ s.project.noneYet }}</p>
    <p v-if="can('projects')" style="margin-top: 12px"><router-link class="btn" to="/projects">{{ s.project.goProjects }}</router-link></p>
  </div>
</template>
