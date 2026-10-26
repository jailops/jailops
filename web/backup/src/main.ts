import '@fontsource/ibm-plex-sans/latin-400.css'
import '@fontsource/ibm-plex-sans/latin-500.css'
import '@fontsource/ibm-plex-sans/latin-600.css'
import '@fontsource/ibm-plex-mono/latin-400.css'
import '@fontsource/ibm-plex-mono/latin-500.css'
import '@jailops/shared/styles/tokens.css'
import '@jailops/shared/styles/base.css'
import { createApp } from 'vue'
import App from './App.vue'
import { makeRouter } from './router'
import { boot } from '@jailops/shared/composables/session'
import { initTheme } from '@jailops/shared/composables/theme'

initTheme()
const router = makeRouter()

void boot({ role: 'backup', loadDemo: async (o) => (await import('./demo/transport')).demoTransport(o) }).then(async () => {
  const app = createApp(App)
  app.use(router)
  await router.isReady()
  app.mount('#app')
})
