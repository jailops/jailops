<script setup lang="ts">
import { useId } from 'vue'

// The JailOps mark: an isometric cube (the jail) with bars on one face and a prompt on the other.
// Drawn from the same source as the website logo. The gradient and mask ids are per instance: the shell
// renders the mark twice (top bar and side bar) and a hidden first copy would otherwise own the ids.
defineProps<{ size?: number }>()
const uid = useId().replace(/[^A-Za-z0-9]/g, '')
</script>

<template>
  <svg class="brandmark" :width="size ?? 22" :height="size ?? 22" viewBox="0 0 64 64" aria-hidden="true">
<defs>
<linearGradient :id="`${uid}t`" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#d4e8ff"/><stop offset="1" stop-color="#a9d1ff"/></linearGradient>
<linearGradient :id="`${uid}l`" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#74b2ff"/><stop offset="1" stop-color="#4a8df0"/></linearGradient>
<linearGradient :id="`${uid}r`" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#3a78e6"/><stop offset="1" stop-color="#2459c0"/></linearGradient>
<mask :id="`${uid}m`" maskUnits="userSpaceOnUse" x="0" y="0" width="64" height="64">
<rect width="64" height="64" fill="#fff"/>
<g fill="#000"><path d="M13.2 27.10L16.0 28.72V47.72L13.2 46.10Z"/><path d="M19.4 30.69L22.2 32.32V51.32L19.4 49.69Z"/><path d="M25.6 34.29L28.400000000000002 35.91V54.91L25.6 53.29Z"/></g>
<g transform="translate(32 33) matrix(1 -0.58 0 1 0 0)" fill="none" stroke="#000" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><path d="M6 5.3L11 15.7 6 20.3"/><path d="M15 22h5"/></g>
</mask>
</defs>
<g :mask="`url(#${uid}m)`" stroke-linejoin="round">
<path d="M32 4 57 18.5 32 33 7 18.5Z" :fill="`url(#${uid}t)`"/>
<path d="M7 18.5 32 33v29L7 47.5Z" :fill="`url(#${uid}l)`"/>
<path d="M57 18.5 32 33v29l25-14.5Z" :fill="`url(#${uid}r)`"/>
</g>
  </svg>
</template>

<style>
.brandmark { flex: none; }
.brand { display: inline-flex; align-items: center; gap: 8px; }
</style>
