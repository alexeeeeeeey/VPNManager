<script setup lang="ts">
import { ref, watch } from 'vue'
import { useMutation, useQuery } from '@tanstack/vue-query'
import { applyAccent, savedAccent } from './accent'

type Settings = {
  port: number
  https: boolean
  tlsAvailable: boolean
  editable: boolean
}
const port = ref(9080)
const https = ref(false)
const password = ref('')
const accent = ref(savedAccent())
const nextAddress = ref('')
const periods = ref({ subscriptions: 1, panel: 24, zapret: 24 })
const updateEnabled = ref({ subscriptions: true, panel: true, zapret: true })
const schedules = [
  { key: 'subscriptions', label: 'Подписки' },
  { key: 'panel', label: 'Панель' },
  { key: 'zapret', label: 'Zapret2' },
] as const
async function updateSchedule(method = 'GET'): Promise<typeof periods.value> {
  const response = await fetch('/api/panel/auto-updates', {
    method,
    ...(method === 'PUT'
      ? {
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(
            Object.fromEntries(
              schedules.map(({ key }) => [
                key,
                updateEnabled.value[key] ? periods.value[key] : 0,
              ]),
            ),
          ),
        }
      : {}),
  })
  if (response.status === 401) location.assign('/login')
  const result = await response.json()
  if (!response.ok) throw new Error(result.message)
  return result
}
const { data: updatePeriods, error: scheduleError } = useQuery({
  queryKey: ['auto-updates'],
  queryFn: () => updateSchedule(),
  staleTime: Infinity,
})
watch(
  updatePeriods,
  (value) => {
    if (value) {
      for (const { key } of schedules) {
        updateEnabled.value[key] = value[key] > 0
        if (value[key] > 0) periods.value[key] = value[key]
      }
    }
  },
  { immediate: true },
)
const {
  mutate: saveSchedule,
  isPending: savingSchedule,
  error: saveScheduleError,
  isSuccess: scheduleSaved,
} = useMutation({ mutationFn: () => updateSchedule('PUT') })
async function request(init?: RequestInit): Promise<Settings> {
  const response = await fetch('/api/panel/settings', init)
  if (response.status === 401) location.assign('/login')
  const value = await response.json()
  if (!response.ok)
    throw new Error(value.message ?? 'Не удалось сохранить настройки')
  return value
}
const { data, error: loadError } = useQuery({
  queryKey: ['panel-settings'],
  queryFn: () => request(),
  staleTime: Infinity,
})
watch(
  data,
  (value) => {
    if (value) {
      port.value = value.port
      https.value = value.https
    }
  },
  { immediate: true },
)
watch(accent, applyAccent)
const {
  mutate: save,
  isPending,
  error,
  variables: savedSection,
} = useMutation({
  mutationFn: (section: 'password' | 'connection') =>
    request({
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        port: section === 'connection' ? port.value : data.value?.port,
        https: section === 'connection' ? https.value : data.value?.https,
        password: section === 'password' ? password.value : '',
      }),
    }),
  onSuccess(value) {
    password.value = ''
    const url = new URL(location.href)
    url.protocol = value.https ? 'https:' : 'http:'
    url.port = String(value.port)
    url.pathname = '/login'
    nextAddress.value = url.toString()
  },
})
</script>

<template>
  <header class="page-header"><h1>Настройки</h1></header>
  <div class="panel-settings-grid">
    <section class="panel settings-panel">
      <h2>Пароль</h2>
      <p v-if="loadError" class="inline-error">{{ loadError.message }}</p>
      <p v-else-if="!data" role="status">Загрузка…</p>
      <p v-else-if="nextAddress" role="status">
        Панель перезапускается. Через несколько секунд
        <a :href="nextAddress"><u>открой страницу входа</u></a
        >.
      </p>
      <form v-else class="form" @submit.prevent="save('password')">
        <label
          >Новый пароль
          <input
            v-model="password"
            type="password"
            autocomplete="new-password"
            minlength="8"
            maxlength="128"
            required
          />
        </label>
        <p v-if="error && savedSection === 'password'" class="inline-error">
          {{ error.message }}
        </p>
        <div class="actions">
          <button
            class="button primary"
            :disabled="!data.editable || isPending"
            type="submit"
          >
            Сменить пароль
          </button>
        </div>
      </form>
    </section>
    <section v-if="data && !nextAddress" class="panel settings-panel">
      <h2>Подключение к панели</h2>
      <form class="form" @submit.prevent="save('connection')">
        <div class="panel-connection-row">
          <label
            >Порт панели
            <input
              v-model.number="port"
              type="number"
              min="1"
              max="65535"
              required
          /></label>
          <button
            type="button"
            class="setting-switch"
            role="switch"
            :aria-checked="https"
            :disabled="!data.editable || !data.tlsAvailable || isPending"
            @click="https = !https"
          >
            <span class="setting-switch-track" aria-hidden="true"
              ><span
            /></span>
            HTTPS
          </button>
        </div>
        <small v-if="!data.tlsAvailable">Для HTTPS нужен сертификат.</small>
        <small v-else-if="!https"
          >По HTTP пароль и сеанс передаются без шифрования.</small
        >
        <p v-if="error && savedSection === 'connection'" class="inline-error">
          {{ error.message }}
        </p>
        <div class="actions">
          <button
            class="button primary"
            :disabled="!data.editable || isPending"
            type="submit"
          >
            Сохранить
          </button>
        </div>
      </form>
    </section>
    <section class="panel settings-panel">
      <h2>Внешний вид</h2>
      <label class="accent-picker"
        >Цвет темы <input v-model="accent" type="color"
      /></label>
    </section>
    <section class="panel settings-panel">
      <h2>Автообновление</h2>
      <p v-if="scheduleError" class="inline-error">
        {{ scheduleError.message }}
      </p>
      <form
        v-else-if="updatePeriods"
        class="form panel-updates-form"
        @submit.prevent="saveSchedule()"
      >
        <div v-for="item in schedules" :key="item.key" class="panel-update-row">
          <button
            type="button"
            class="setting-switch"
            role="switch"
            :aria-checked="updateEnabled[item.key]"
            :disabled="savingSchedule"
            @click="updateEnabled[item.key] = !updateEnabled[item.key]"
          >
            <span class="setting-switch-track" aria-hidden="true"
              ><span
            /></span>
            {{ item.label }}
          </button>
          <label v-if="updateEnabled[item.key]">
            <span>Каждые</span>
            <input
              v-model.number="periods[item.key]"
              type="number"
              min="1"
              max="720"
              step="1"
              required
              :aria-label="'Интервал в часах: ' + item.label"
              :disabled="savingSchedule"
            />
            <span>ч</span>
          </label>
        </div>
        <p v-if="saveScheduleError" class="inline-error">
          {{ saveScheduleError.message }}
        </p>
        <p v-else-if="scheduleSaved" role="status">Расписание сохранено.</p>
        <div class="actions">
          <button
            class="button primary"
            type="submit"
            :disabled="savingSchedule"
          >
            Сохранить расписание
          </button>
        </div>
      </form>
    </section>
  </div>
</template>
