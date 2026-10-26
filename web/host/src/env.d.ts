/// <reference types="vite/client" />
declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  const c: DefineComponent<object, object, unknown>
  export default c
}
declare module 'lucide-static/icons/*.svg' {
  const url: string
  export default url
}
