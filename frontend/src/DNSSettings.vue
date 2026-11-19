<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ArrowDown, ArrowUp } from 'lucide-vue-next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/vue-query'
import { api, unwrap, useUI } from './client'
import InfoHint from './InfoHint.vue'

const ui = useUI()
const query = useQueryClient()
const stages = ['xray', 'doh', 'direct'] as const
type Stage = (typeof stages)[number]
const labels: Record<Stage, string> = {
  xray: 'Xray',
  doh: 'DoH',
  direct: 'Direct',
}
const order = ref<{ stage: Stage; enabled: boolean }[]>([])
const doh = ref('')
const direct = ref('')
const active = computed(() =>
  order.value.filter((entry) => entry.enabled).map((entry) => entry.stage),
)
const { data, isPending, isError, refetch } = useQuery({
  queryKey: ['dns-settings'],
  queryFn: async () => unwrap(await api.GET('/api/settings/dns')),
  staleTime: Infinity,
})
watch(
  data,
  (value) => {
    if (!value) return
    order.value = [
      ...value.stages.map((stage) => ({ stage, enabled: true })),
      ...stages
        .filter((stage) => !value.stages.includes(stage))
        .map((stage) => ({ stage, enabled: false })),
    ]
    doh.value = value.doh.join('\n')
    direct.value = value.direct.join('\n')
  },
  { immediate: true },
)

function move(index: number, delta: number) {
  const next = index + delta
  if (next < 0 || next >= order.value.length) return
  const [entry] = order.value.splice(index, 1)
  order.value.splice(next, 0, entry)
}

const lines = (value: string) =>
  value
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean)
const {
  mutate: save,
  isPending: saving,
  error,
} = useMutation({
  mutationFn: async (enabled?: boolean) =>
    unwrap(
      await api.PUT('/api/settings/dns', {
        body: {
          stages: active.value,
          doh: lines(doh.value),
          direct: lines(direct.value),
          listenPort: data.value?.listenPort ?? 0,
          enabled: enabled ?? data.value?.enabled,
        },
      }),
    ),
  onSuccess(value, enabled) {
    query.setQueryData(['dns-settings'], value)
    ui.notify(
      enabled === undefined
        ? 'Настройки DNS сохранены'
        : enabled
          ? 'DNS-цепочка включена'
          : 'DNS-цепочка выключена',
    )
  },
})
</script>

<template>
  <header class="page-header">
    <div>
      <h1>DNS</h1>
    </div>
  </header>
  <section v-if="data" class="panel settings-panel dns-switch-panel">
    <button
      type="button"
      role="switch"
      class="setting-switch"
      :aria-checked="data.enabled ?? false"
      :disabled="saving"
      @click="save(!data.enabled)"
    >
      <span class="setting-switch-track" aria-hidden="true"><span /></span>
      Использовать DNS-цепочку
    </button>
    <small>{{
      data.enabled
        ? 'Роутер использует DNS-цепочку панели.'
        : 'Роутер использует свой обычный DNS.'
    }}</small>
    <p v-if="error" class="inline-error" role="alert">{{ error.message }}</p>
  </section>
  <section class="panel settings-panel">
    <p v-if="isPending" role="status">Загрузка настроек…</p>
    <div v-else-if="isError">
      <p class="inline-error" role="alert">
        Не удалось загрузить настройки DNS.
      </p>
      <button type="button" class="button" @click="refetch()">Повторить</button>
    </div>
    <form v-else class="form dns-form" @submit.prevent="save()">
      <fieldset>
        <legend>
          Порядок этапов
          <InfoHint
            text="Роутер пробует включённые способы сверху вниз. При ошибке переходит к следующему. Ответ, что домен не существует, завершает поиск. Этап DNS через Xray работает только при запущенном Xray и выбранном VPN-профиле."
          />
        </legend>
        <ol class="dns-stages">
          <li v-for="(entry, index) in order" :key="entry.stage">
            <label class="dns-stage-toggle"
              ><input
                v-model="entry.enabled"
                :disabled="saving"
                type="checkbox"
              />
              {{ labels[entry.stage] }}</label
            >
            <button
              type="button"
              class="icon-button"
              :disabled="saving || index === 0"
              :aria-label="labels[entry.stage] + ' выше'"
              @click="move(index, -1)"
            >
              <ArrowUp :size="16" />
            </button>
            <button
              type="button"
              class="icon-button"
              :disabled="saving || index === order.length - 1"
              :aria-label="labels[entry.stage] + ' ниже'"
              @click="move(index, 1)"
            >
              <ArrowDown :size="16" />
            </button>
          </li>
        </ol>
      </fieldset>
      <label
        ><span
          >DoH
          <InfoHint
            text="DNS через зашифрованный HTTPS-запрос. Укажи полный адрес сервера, например https://1.1.1.1/dns-query. Один адрес на строку."
        /></span>
        <textarea
          v-model="doh"
          :disabled="saving"
          rows="2"
          spellcheck="false"
          placeholder="https://1.1.1.1/dns-query"
        />
      </label>
      <label
        ><span
          >Direct
          <InfoHint
            text="Укажи IP или IP:порт, по одному на строку. Первый сервер также разрешает имя DoH-сервера."
        /></span>
        <textarea
          v-model="direct"
          :disabled="saving"
          rows="2"
          spellcheck="false"
          placeholder="9.9.9.9"
        />
      </label>
      <div class="actions">
        <button
          class="button primary"
          type="submit"
          :disabled="saving || active.length === 0"
        >
          {{ saving ? 'Сохраняем…' : 'Сохранить' }}
        </button>
      </div>
    </form>
  </section>
</template>
