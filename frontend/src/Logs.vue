<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { RotateCcw } from 'lucide-vue-next'
import { api, unwrap } from './client'
const { data, refetch, isError, isFetching } = useQuery({
  queryKey: ['logs'],
  queryFn: async () => unwrap(await api.GET('/api/logs')),
  refetchInterval: 5000,
})
</script>

<template>
  <header class="page-header">
    <div>
      <h1>Журнал</h1>
    </div>
    <button class="button" :disabled="isFetching" @click="refetch()">
      <RotateCcw :size="16" /> Обновить
    </button>
  </header>
  <section class="panel log-panel">
    <p v-if="isError" class="inline-error" role="alert">
      Не удалось загрузить журнал.
    </p>
    <pre v-else-if="data?.lines.length" tabindex="0">{{
      data.lines.join('\n')
    }}</pre>
    <p v-else class="empty">{{ data ? 'Новых сообщений нет' : 'Загрузка…' }}</p>
  </section>
</template>
