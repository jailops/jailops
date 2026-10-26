<script setup lang="ts">
import { s } from '@/i18n'
import { session } from '@jailops/shared/composables/session'
</script>

<template>
  <div>
    <div class="page-head"><h1>{{ s.overview.title }}</h1><span class="muted mono">{{ session.info?.hostname }}</span></div>
    <p class="empty panel">{{ s.overview.empty }}</p>
  </div>
</template>
