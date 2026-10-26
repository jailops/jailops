<script setup lang="ts">
import { s } from '@/i18n'
</script>

<template>
  <div class="empty"><p>{{ s.app.notFound }}</p><router-link to="/">{{ s.nav.overview }}</router-link></div>
</template>
