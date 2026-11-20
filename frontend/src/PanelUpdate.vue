<script setup lang="ts">
import { ref } from 'vue'
import { useMutation, useQuery, useQueryClient } from '@tanstack/vue-query'
import { Download, RefreshCw } from 'lucide-vue-next'
import { api, unwrap, useUI } from './client'
import Modal from './Modal.vue'

const ui = useUI()
const query = useQueryClient()
const updateKey = ['panel-update']
const open = ref(false)
const restarting = ref(false)
const { data: update } = useQuery({
  queryKey: updateKey,
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

const { mutate: check, isPending: checking } = useMutation({
  mutationFn: async () =>
    unwrap(
      await api.GET('/api/update', { params: { query: { check: true } } }),
    ),
  async onMutate() {
    await query.cancelQueries({ queryKey: updateKey })
  },
  onSuccess(value) {
    query.setQueryData(updateKey, value)
    if (value.available) ui.notify('Доступна версия ' + value.latest)
    else if (value.latest) ui.notify('Доступных обновлений нет')
    else ui.notify('Не удалось получить список релизов', true)
  },
  onError(error) {
    ui.notify(error.message, true)
  },
})

function confirm() {
  reset()
  open.value = true
}
</script>

<template>
  <span v-if="update" class="chip">Версия {{ update.current }}</span>
  <button
    class="button"
    type="button"
    :disabled="checking || isPending || restarting"
    @click="check()"
  >
    <RefreshCw :size="16" />
    {{ checking ? 'Проверяем…' : 'Проверить обновления' }}
  </button>
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
