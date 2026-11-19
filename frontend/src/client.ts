import createClient from 'openapi-fetch'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import type { components, paths } from './api.gen'

export const api = createClient<paths>()
api.use({
  onResponse({ response }) {
    if (response.status === 401) window.location.replace('/login')
  },
})
export type Status = components['schemas']['Status']
export type Profile = components['schemas']['Profile']
export type Probe = components['schemas']['Probe']
export type Inventory = components['schemas']['Inventory']

export function unwrap<T>(response: {
  data?: T
  error?: { message: string }
}): T {
  if (response.data === undefined)
    throw new Error(response.error?.message || 'Сервер недоступен')
  return response.data
}

export const useUI = defineStore('ui', () => {
  const notice = ref<{ text: string; error: boolean }>()
  const probes = ref<Record<string, Probe>>({})
  const live = ref(false)
  let timer: ReturnType<typeof setTimeout>
  function notify(text: string, error = false) {
    notice.value = { text, error }
    clearTimeout(timer)
    timer = setTimeout(() => (notice.value = undefined), 6500)
  }
  return { notice, probes, live, notify }
})

export function useStatus() {
  const ui = useUI()
  return useQuery({
    queryKey: ['status'],
    queryFn: async () => unwrap(await api.GET('/api/status')),
    refetchInterval: computed(() => (ui.live ? false : 4000)),
    staleTime: 2000,
  })
}

export function useInventory() {
  return useQuery({
    queryKey: ['inventory'],
    queryFn: async () => unwrap(await api.GET('/api/inventory')),
    refetchInterval: 30000,
  })
}

export function useRouterDashboard() {
  return useQuery({
    queryKey: ['router'],
    queryFn: async () => unwrap(await api.GET('/api/router')),
    refetchInterval: 5000,
    staleTime: 5000,
  })
}

export function useActions() {
  const query = useQueryClient()
  const ui = useUI()
  async function run(task: () => Promise<unknown>, success?: string) {
    try {
      await task()
      if (success) ui.notify(success)
      return true
    } catch (error) {
      ui.notify(
        error instanceof Error ? error.message : 'Операция не выполнена',
        true,
      )
      return false
    } finally {
      void query.invalidateQueries({ queryKey: ['inventory'] })
      if (!ui.live) void query.invalidateQueries({ queryKey: ['status'] })
    }
  }
  return { run }
}

export function formatBytes(value: number) {
  if (value < 1024) return value + ' Б'
  const index = Math.min(4, Math.floor(Math.log(value) / Math.log(1024)))
  return (
    new Intl.NumberFormat('ru', { maximumFractionDigits: 1 }).format(
      value / 1024 ** index,
    ) +
    ' ' +
    ['Б', 'КБ', 'МБ', 'ГБ', 'ТБ'][index]
  )
}

export const stateLabel: Record<string, string> = {
  starting: 'Запускается',
  running: 'Работает',
  stopped: 'Остановлен',
  error: 'Ошибка',
}
