import { ref, watch, onMounted, onBeforeUnmount, type Ref } from 'vue'
import { ApiError } from '@jailops/shared/api/client'
import { dataVersion } from './events'

export interface Loaded<T> {
  data: Ref<T | undefined>
  error: Ref<ApiError | null>
  loading: Ref<boolean>
  reload: () => Promise<void>
}

/**
 * Load data on mount and again whenever the server reports a change (dataVersion). A failed
 * reload keeps the previous data on screen and sets error.
 */
export function useLoad<T>(fn: () => Promise<T>, opts: { every?: number } = {}): Loaded<T> {
  const data = ref<T>() as Ref<T | undefined>
  const error = ref<ApiError | null>(null)
  const loading = ref(true)
  let alive = true
  let tick: ReturnType<typeof setInterval> | undefined
  let n = 0

  async function reload(): Promise<void> {
    const mine = ++n
    try {
      const v = await fn()
      if (!alive || mine !== n) return
      data.value = v
      error.value = null
    } catch (e) {
      if (!alive || mine !== n) return
      error.value = e instanceof ApiError ? e : new ApiError({ message: String(e) })
    } finally {
      if (alive && mine === n) loading.value = false
    }
  }
  onMounted(() => {
    void reload()
    if (opts.every) tick = setInterval(() => void reload(), opts.every)
  })
  onBeforeUnmount(() => {
    alive = false
    clearInterval(tick)
  })
  watch(dataVersion, () => void reload())
  return { data, error, loading, reload }
}
