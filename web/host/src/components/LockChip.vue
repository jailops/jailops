<script setup lang="ts">
import { s } from '@/i18n'
import Icon from '@jailops/shared/components/Icon.vue'

// The encrypted volumes of a jail that wait for their passphrase.
defineProps<{ volumes: string[]; jail: string }>()
</script>

<template>
  <span class="pill warn lock-chip" :data-locked="jail" :title="s.keys.lockedHint"><Icon name="lock" />{{ s.keys.lockedChip(volumes) }}</span>
</template>

<style>
.lock-chip { margin-left: 6px; }
.lock-chip::before { display: none; }
</style>
