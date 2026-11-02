<script setup lang="ts">
import { onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { s } from '@/i18n'
import { api } from '@/api/endpoints'
import LoginCard from '@jailops/shared/components/LoginCard.vue'

// A host without an administrator has nobody to sign in: the first-run wizard creates one with the setup token.
const router = useRouter()
onMounted(async () => {
  try {
    if ((await api.setup.status()).required) await router.replace('/setup')
  } catch {
    /* not_found: the host has an administrator (or the server predates setup); sign in as usual */
  }
})
</script>

<template>
  <LoginCard :brand="`${s.app.name} ${s.app.role}`" />
</template>
