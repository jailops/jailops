<script setup lang="ts">
import { s } from '@/i18n'
import LoginCard from '@jailops/shared/components/LoginCard.vue'
</script>

<template>
  <LoginCard :brand="`${s.app.name} ${s.app.role}`" />
</template>
