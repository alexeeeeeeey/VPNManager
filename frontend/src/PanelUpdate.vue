<script setup lang="ts">
import { ref } from 'vue'
import { useMutation, useQuery } from '@tanstack/vue-query'
import { Download } from 'lucide-vue-next'
import { api, unwrap, useUI } from './client'
import Modal from './Modal.vue'

const ui = useUI()
const open = ref(false)
const restarting = ref(false)
const { data: update } = useQuery({
  queryKey: ['panel-update'],
  queryFn: async () => unwrap(await api.GET('/api/update')),
  staleTime: 10 * 60_000,
  refetchInterval: 10 * 60_000,
  retry: false,
})
const {
  mutate: install,
  isPending,
  error,
  reset,
} = useMutation({
  mutationFn: async (version: string) =>
    unwrap(await api.POST('/api/update', { body: { version } })),
  async onSuccess(release) {
    restarting.value = true
    const deadline = Date.now() + 35_000
    while (Date.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, 1200))
      try {
        const response = await api.GET('/api/update', {
          signal: AbortSignal.timeout(2500),
        })
        if (response.data?.current.replace(/^v/, '') === release.version) {
          window.location.reload()
          return
        }
      } catch {
        // The supervisor is restarting the service.
      }
    }
    restarting.value = false
    open.value = false
    ui.notify('Обновление записано. Панель ещё перезапускается.', true)
  },
})

function confirm() {
  reset()
  open.value = true
}
</script>

<template>
  <button
    v-if="update?.available"
    class="button primary"
    type="button"
    :disabled="isPending"
    @click="confirm"
  >
    <Download :size="16" /> Обновить панель
  </button>
  <Modal
    v-if="update?.available"
    v-model="open"
    title="Обновление панели"
    :description="
      'Версия ' +
      update.current +
      ' → ' +
      update.latest +
      '. На время перезапуска соединение может прерваться.'
    "
  >
    <p v-if="error" class="inline-error" role="alert">{{ error.message }}</p>
    <div class="actions">
      <button
        class="button primary"
        type="button"
        :disabled="isPending"
        @click="install(update.latest)"
      >
        {{
          restarting
            ? 'Перезапуск…'
            : isPending
              ? 'Загрузка и проверка…'
              : 'Установить ' + update.latest
        }}
      </button>
    </div>
  </Modal>
</template>
