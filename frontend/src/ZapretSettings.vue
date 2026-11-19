<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useMutation, useQuery, useQueryClient } from '@tanstack/vue-query'
import { api, unwrap, useUI } from './client'
import Modal from './Modal.vue'
import InfoHint from './InfoHint.vue'
import type { components } from './api.gen'

const ui = useUI()
const query = useQueryClient()
const binary = ref('')
const args = ref('')
const removeOpen = ref(false)
const defaultTargets = [
  'https://www.google.com/generate_204',
  'https://www.youtube.com/',
  'https://redirector.googlevideo.com/',
  'https://discord.com/',
  'https://www.speedtest.net/',
].join('\n')
const targets = ref(defaultTargets)
const { data, isPending, isError, refetch } = useQuery({
  queryKey: ['zapret-settings'],
  queryFn: async () => unwrap(await api.GET('/api/settings/zapret')),
  staleTime: Infinity,
})
const { data: status } = useQuery({
  queryKey: ['zapret-status'],
  queryFn: async () => unwrap(await api.GET('/api/zapret')),
  refetchInterval: 8000,
})
const {
  mutate: checkRelease,
  data: latest,
  isPending: checkingRelease,
} = useMutation({
  mutationFn: async () =>
    unwrap(
      await api.GET('/api/zapret', { params: { query: { check: true } } }),
    ),
  onError(error) {
    ui.notify(error.message, true)
  },
})
watch(
  data,
  (value) => {
    if (!value) return
    binary.value = value.binary
    args.value = value.args.join('\n')
  },
  { immediate: true },
)

const lines = (value: string) =>
  value.split(/\r?\n/).filter((line) => line.trim())
const dirty = computed(
  () =>
    binary.value !== data.value?.binary ||
    args.value !== data.value?.args.join('\n'),
)
const {
  mutate: save,
  isPending: saving,
  error,
} = useMutation({
  mutationFn: async (nextEnabled?: boolean) =>
    unwrap(
      await api.PUT('/api/settings/zapret', {
        body: {
          binary: binary.value,
          args: lines(args.value),
          enabled: nextEnabled ?? data.value?.enabled ?? false,
        },
      }),
    ),
  onSuccess(value, nextEnabled) {
    query.setQueryData(['zapret-settings'], value)
    void query.invalidateQueries({ queryKey: ['zapret-status'] })
    void query.invalidateQueries({ queryKey: ['status'] })
    ui.notify(
      nextEnabled === undefined
        ? 'Настройки Zapret2 сохранены'
        : nextEnabled
          ? 'Zapret2 включён'
          : 'Zapret2 выключен',
    )
  },
  onError(error) {
    ui.notify(error.message, true)
  },
})
const { mutate: manage, isPending: managing } = useMutation({
  mutationFn: async (action: 'install' | 'remove' | 'check') =>
    unwrap(await api.POST('/api/zapret', { body: { action } })),
  onSuccess(value, action) {
    removeOpen.value = false
    query.setQueryData(['zapret-status'], value)
    void query.invalidateQueries({ queryKey: ['zapret-settings'] })
    ui.notify(
      action === 'check'
        ? 'nfqws2 принял параметры стратегии'
        : action === 'remove'
          ? 'Установка Zapret2 удалена'
          : 'Zapret2 установлен',
    )
  },
  onError(error) {
    ui.notify(error.message, true)
  },
})
const lua = [
  '--lua-init=@{zapret}/lua/zapret-lib.lua',
  '--lua-init=@{zapret}/lua/zapret-antidpi.lua',
]
const tls = [
  '--filter-tcp=443',
  '--filter-l7=tls',
  '--payload=tls_client_hello',
]
const upstreamHTTP = [
  '--filter-tcp=80',
  '--filter-l7=http',
  '--payload=http_req',
  '--lua-desync=fake:blob=fake_default_http:tcp_md5',
  '--lua-desync=multisplit:pos=method+2',
]
const upstreamTLS = [
  ...tls,
  '--lua-desync=fake:blob=fake_default_tls:tcp_md5:tcp_seq=-10000',
  '--lua-desync=multidisorder:pos=1,midsld',
]
const upstreamQUIC = [
  '--filter-udp=443',
  '--filter-l7=quic',
  '--payload=quic_initial',
  '--lua-desync=fake:blob=fake_default_quic:repeats=6',
]
function useTrial(trialArgs: string[]) {
  args.value = trialArgs.join('\n')
  ui.notify('Проверь параметры и нажми «Сохранить»')
}
const strategies = [
  { name: 'TLS · Split', args: [...tls, '--lua-desync=multisplit:pos=2'] },
  {
    name: 'TLS · Disorder',
    args: [...tls, '--lua-desync=multidisorder:pos=2'],
  },
  {
    name: 'TLS · Fake',
    args: [...tls, '--lua-desync=fake:blob=fake_default_tls:tcp_ts=-1000'],
  },
  { name: 'Zapret2 · HTTP', args: upstreamHTTP },
  { name: 'Zapret2 · TLS', args: upstreamTLS },
  { name: 'Zapret2 · QUIC', args: upstreamQUIC },
  {
    name: 'Zapret2 · HTTP + TLS + QUIC',
    args: [...upstreamHTTP, '--new', ...upstreamTLS, '--new', ...upstreamQUIC],
  },
]
const tuningResult = ref<components['schemas']['ZapretTuneResult']>()
const activeTrial = ref(-1)
const selectedTrial = ref(0)
const followTrial = ref(true)
const {
  mutate: tune,
  isPending: tuning,
  error: tuneError,
} = useMutation({
  mutationFn: async () => {
    tuningResult.value = undefined
    activeTrial.value = -1
    selectedTrial.value = 0
    followTrial.value = true
    const response = await fetch('/api/zapret/tune?stream=1', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ targets: lines(targets.value) }),
    })
    if (response.status === 401) window.location.replace('/login')
    if (!response.ok) throw new Error((await response.json()).message)
    if (!response.body) throw new Error('Нет потока результатов')
    const reader = response.body
      .pipeThrough(new TextDecoderStream())
      .getReader()
    let buffer = ''
    try {
      while (true) {
        const { value, done } = await reader.read()
        if (done) throw new Error('Подбор прервался до получения итогов')
        buffer += value
        const records = buffer.split('\n')
        buffer = records.pop() ?? ''
        for (const record of records) {
          const event = JSON.parse(record)
          tuningResult.value = event.result
          activeTrial.value = event.active
          if (followTrial.value) {
            selectedTrial.value = Math.max(
              0,
              event.done ? event.result.best : event.active,
            )
          }
          if (event.error) throw new Error(event.error)
          if (event.done) return
        }
      }
    } finally {
      await reader.cancel()
    }
  },
})
const trialDetails = computed(
  () => tuningResult.value?.trials[selectedTrial.value],
)
function selectTrial(index: number) {
  followTrial.value = false
  selectedTrial.value = index
}
const {
  mutate: probe,
  data: results,
  isPending: probing,
} = useMutation({
  mutationFn: () =>
    Promise.all(
      lines(targets.value)
        .slice(0, 5)
        .map(async (target) => {
          try {
            const value = unwrap(
              await api.POST('/api/zapret/probe', {
                body: { url: target.trim() },
              }),
            )
            return {
              target,
              text: 'HTTP ' + value.status + ' · ' + value.latency + ' мс',
              failed: false,
            }
          } catch (error) {
            return {
              target,
              text:
                'HTTPS не подтверждён · ' +
                (error instanceof Error ? error.message : 'Нет ответа'),
              failed: true,
            }
          }
        }),
    ),
})
</script>

<template>
  <header class="page-header">
    <div>
      <h1>Zapret2</h1>
    </div>
  </header>
  <section class="panel settings-panel zapret-panel">
    <div class="section-header">
      <h2>
        Установка
        <InfoHint
          text="Здесь устанавливается и включается обработчик Zapret2. Он получает только те соединения, которые Xray направляет по правилу ZAPRET."
        />
      </h2>
      <span class="chip">{{
        status?.installed ? 'v' + status.installed : 'Не скачан'
      }}</span>
    </div>
    <button
      v-if="data"
      type="button"
      class="setting-switch"
      role="switch"
      :aria-checked="data.enabled"
      :disabled="
        saving ||
        managing ||
        dirty ||
        (!data.enabled &&
          (!data.args.length || (!data.binary && !status?.installed)))
      "
      @click="save(!data.enabled)"
    >
      <span class="setting-switch-track" aria-hidden="true"><span /></span>
      Включить Zapret2
    </button>
    <small v-if="data" class="router-muted">
      Если Zapret2 выключен, срабатывает запасное действие пресета. Если оно тоже ZAPRET, соединение блокируется.
    </small>
    <small v-if="data && (!data.args.length || dirty)" class="router-muted">
      Сначала сохрани параметры стратегии.
    </small>
    <p v-if="status && !status.supported" class="router-muted">
      Установка выполняется на Linux amd64 или arm64.
    </p>
    <div class="actions">
      <button
        v-if="!status?.installed"
        class="button primary"
        :disabled="!status?.supported || managing || saving || dirty"
        @click="manage('install')"
      >
        Установить
      </button>
      <template v-else>
        <button
          class="button"
          :disabled="checkingRelease || managing"
          @click="checkRelease()"
        >
          Проверить обновления
        </button>
        <button
          v-if="latest?.updateAvailable && latest.latest !== status.installed"
          class="button primary"
          :disabled="managing || saving || dirty"
          @click="manage('install')"
        >
          Обновить до {{ latest.latest }}
        </button>
        <button
          class="button"
          :disabled="managing || saving || dirty"
          @click="removeOpen = true"
        >
          Удалить
        </button>
      </template>
      <small v-if="managing || checkingRelease" role="status">{{
        managing ? 'Выполняется операция…' : 'Проверяем релиз…'
      }}</small>
      <small
        v-else-if="
          latest &&
          (!latest.updateAvailable || latest.latest === status?.installed)
        "
        >Установлена актуальная версия</small
      >
    </div>
  </section>
  <div class="zapret-settings-grid">
    <section class="panel settings-panel zapret-panel">
      <div class="section-header">
        <h2>
          Настройки и стратегии
          <InfoHint
            text="Стратегия определяет, как Zapret2 обрабатывает пакеты. Можно начать с готового шаблона и проверить его на нужных сайтах."
          />
        </h2>
      </div>
      <p v-if="isPending" role="status">Загрузка настроек…</p>
      <div v-else-if="isError">
        <p class="inline-error">Не удалось загрузить настройки.</p>
        <button class="button" @click="refetch()">Повторить</button>
      </div>
      <form v-else class="form" @submit.prevent="save()">
        <label
          >Путь
          <input
            v-model="binary"
            :disabled="saving || managing"
            spellcheck="false"
            :placeholder="status?.binaryPath || 'Путь к nfqws2'"
            :title="binary || status?.binaryPath"
        /></label>
        <div class="actions">
          <button
            v-for="item in strategies"
            :key="item.name"
            type="button"
            class="button"
            :disabled="saving || managing || tuning || !status?.installed"
            @click="args = [...lua, ...item.args].join('\n')"
          >
            {{ item.name }}
          </button>
        </div>
        <label
          ><span
            >Аргументы CLI
            <InfoHint
              text="Параметры запуска nfqws2, один на строку. Для обычной настройки можно выбрать шаблон выше и сохранить его."
          /></span>
          <textarea
            v-model="args"
            :disabled="saving || managing"
            rows="8"
            spellcheck="false"
          />
          <small
            >Один аргумент на строку. {zapret} подставляет путь к установленной
            версии.</small
          >
        </label>
        <p v-if="error" class="inline-error" role="alert">
          {{ error.message }}
        </p>
        <div class="actions">
          <button
            class="button primary"
            type="submit"
            :disabled="saving || managing || tuning || !dirty"
          >
            {{ saving ? 'Применяем…' : 'Сохранить' }}
          </button>
          <button
            class="button"
            type="button"
            :disabled="saving || managing || dirty || !status?.supported"
            @click="manage('check')"
          >
            Проверить параметры
          </button>
        </div>
      </form>
    </section>
    <section class="panel settings-panel zapret-panel">
      <h2>Подбор стратегии</h2>
      <label class="form">
        Домены для подбора и проверки
        <textarea
          v-model="targets"
          rows="5"
          placeholder="example.org"
          :disabled="tuning || probing"
        />
      </label>
      <div class="actions">
        <button
          type="button"
          class="button primary"
          :disabled="
            !status?.installed ||
            tuning ||
            probing ||
            saving ||
            managing ||
            lines(targets).length < 1 ||
            lines(targets).length > 5
          "
          @click="tune()"
        >
          {{ tuning ? 'Подбираем стратегию…' : 'Подобрать стратегию' }}
        </button>
        <button
          type="button"
          class="button"
          :disabled="
            !status?.running ||
            tuning ||
            probing ||
            dirty ||
            saving ||
            managing ||
            !targets.trim()
          "
          @click="probe()"
        >
          {{ probing ? 'Проверяем…' : 'Проверить домены' }}
        </button>
        <button
          v-if="targets !== defaultTargets"
          type="button"
          class="button"
          :disabled="probing || tuning"
          @click="targets = defaultTargets"
        >
          Вернуть стандартные
        </button>
      </div>
      <p v-if="tuning" class="trial-progress" role="status">
        {{
          activeTrial < 0
            ? 'Готовим проверку…'
            : 'Сейчас проверяем: ' + tuningResult?.trials[activeTrial]?.name
        }}
      </p>
      <p v-else-if="tuneError" class="inline-error" role="alert">
        {{ tuneError.message }}
      </p>
      <template v-if="tuningResult && trialDetails">
        <p v-if="tuningResult.best === 0" class="router-muted">
          Ни одна стратегия не дала больше ответов, чем прямое подключение.
        </p>
        <p
          v-if="!tuning && !tuneError && tuningResult.best < 0"
          class="router-muted"
        >
          Ни один адрес не вернул HTTPS-ответ.
        </p>
        <div class="trial-grid" role="group" aria-label="Результаты стратегий">
          <button
            v-for="(trial, i) in tuningResult.trials"
            :key="trial.name"
            type="button"
            class="trial-card"
            :aria-pressed="i === selectedTrial"
            :class="{ 'trial-active': tuning && i === activeTrial }"
            @click="selectTrial(i)"
          >
            <strong>{{ trial.name }}</strong>
            <span
              >{{ trial.statuses.filter((status) => status > 0).length }} из
              {{ tuningResult.targets.length }} ответили</span
            >
            <span v-if="i === tuningResult.best && i > 0" class="trial-best"
              >Лучший результат</span
            >
            <span v-else-if="trial.error" class="failed">Ошибка запуска</span>
            <span v-else-if="tuning && i === activeTrial" class="trial-best"
              >Проверяется</span
            >
            <span v-else-if="!trial.statuses.length">В очереди</span>
          </button>
        </div>
        <div class="trial-detail">
          <h3>{{ trialDetails.name }}</h3>
          <p v-if="trialDetails.error" class="inline-error">
            {{ trialDetails.error }}
          </p>
          <ul v-else class="router-list">
            <li v-for="(target, index) in tuningResult.targets" :key="target">
              <span class="trial-target">{{ target }}</span>
              <span
                class="trial-status"
                :class="{ answered: trialDetails.statuses[index] > 0 }"
              >
                {{
                  trialDetails.statuses[index] > 0
                    ? 'HTTP ' + trialDetails.statuses[index]
                    : trialDetails.statuses[index] === 0
                      ? 'Нет ответа'
                      : 'Ожидаем'
                }}
              </span>
            </li>
          </ul>
          <button
            v-if="
              !tuning &&
              trialDetails.args.length &&
              trialDetails.statuses.some((status) => status > 0)
            "
            type="button"
            class="button"
            :disabled="saving || managing"
            @click="useTrial(trialDetails.args)"
          >
            Подставить в настройки
          </button>
        </div>
      </template>
      <div v-if="probing || results?.length" class="trial-detail">
        <h3>Текущая стратегия</h3>
        <p v-if="probing" role="status">Проверяем домены…</p>
        <ul v-else class="router-list">
          <li v-for="(result, i) in results" :key="i">
            <span>{{ result.target }}</span><span
              :class="{ failed: result.failed }"
              >{{ result.text }}</span
            >
          </li>
        </ul>
      </div>
    </section>
  </div>
  <Modal
    v-model="removeOpen"
    title="Удалить Zapret2?"
    description="Будут удалены файлы установленного из панели релиза. Параметры стратегии сохранятся."
  >
    <div class="actions">
      <button class="button" :disabled="managing" @click="removeOpen = false">
        Отмена</button
      ><button
        class="button destructive"
        :disabled="managing"
        @click="manage('remove')"
      >
        Удалить
      </button>
    </div>
  </Modal>
</template>
