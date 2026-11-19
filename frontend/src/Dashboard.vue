<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import {
  ArrowDownLeft,
  ArrowUpRight,
  Cpu,
  HardDrive,
  Play,
  RotateCcw,
  Square,
  Timer,
  Zap,
} from 'lucide-vue-next'
import {
  api,
  formatBytes,
  stateLabel,
  unwrap,
  useActions,
  useInventory,
  useRouterDashboard,
  useStatus,
} from './client'
import TrafficChart from './TrafficChart.vue'
import RouterPanels from './RouterPanels.vue'
import PanelUpdate from './PanelUpdate.vue'

const { data: status, isError } = useStatus()
const { data: zapret } = useQuery({
  queryKey: ['zapret-status'],
  queryFn: async () => unwrap(await api.GET('/api/zapret')),
  refetchInterval: 5000,
})
const { data: inventory } = useInventory()
const { data: router } = useRouterDashboard()
const { run } = useActions()
const query = useQueryClient()
const busy = ref(false)
const active = computed(() =>
  inventory.value?.profiles.find((p) => p.id === status.value?.activeId),
)
const ram = computed(() =>
  status.value?.memoryTotal
    ? Math.round((status.value.memoryUsed / status.value.memoryTotal) * 100)
    : 0,
)
const uptime = computed(() => {
  const seconds = status.value?.uptime || 0
  const days = Math.floor(seconds / 86400)
  return [
    days ? days + ' д' : '',
    (Math.floor(seconds / 3600) % 24) + ' ч',
    (Math.floor(seconds / 60) % 60) + ' мин',
  ]
    .filter(Boolean)
    .join(' ')
})
const periods = [2, 5, 15, 30]
const period = ref(5)
const latest = ref(Date.now() / 1000)
const chartWindow = computed<[number, number]>(() => [
  latest.value - period.value * 60,
  latest.value,
])
type Sample = {
  t: number
  cpu: number | null
  ram: number | null
  rx: number | null
  tx: number | null
}
const samples = ref<Sample[]>([])
const visible = computed(() =>
  samples.value.filter(({ t }) => t >= chartWindow.value[0]),
)
const times = computed(() => visible.value.map(({ t }) => t))
const cpuPoints = computed<[number[], (number | null)[]]>(() => [
  times.value,
  visible.value.map((p) => p.cpu),
])
const ramPoints = computed<[number[], (number | null)[]]>(() => [
  times.value,
  visible.value.map((p) => p.ram),
])
const points = computed<[number[], (number | null)[], (number | null)[]]>(
  () => [
    times.value,
    visible.value.map((p) => p.rx),
    visible.value.map((p) => p.tx),
  ],
)
let previous: { t: number; rx: number; tx: number } | undefined
watch(
  status,
  (value) => {
    if (!value?.sampledAt) return
    const now = Date.parse(value.sampledAt) / 1000
    if (!Number.isFinite(now) || (previous && now <= previous.t)) return
    const elapsed = previous ? now - previous.t : 0
    if (previous && elapsed > 10) {
      samples.value.push({
        t: previous.t + 1,
        cpu: null,
        ram: null,
        rx: null,
        tx: null,
      })
    }
    samples.value.push({
      t: now,
      cpu: value.memoryTotal ? value.cpu : null,
      ram: value.memoryTotal
        ? (value.memoryUsed / value.memoryTotal) * 100
        : null,
      rx:
        previous && elapsed <= 10 && value.rxBytes >= previous.rx
          ? (value.rxBytes - previous.rx) / elapsed
          : null,
      tx:
        previous && elapsed <= 10 && value.txBytes >= previous.tx
          ? (value.txBytes - previous.tx) / elapsed
          : null,
    })
    const next = { t: now, rx: value.rxBytes, tx: value.txBytes }
    previous = next
    latest.value = now
    samples.value = samples.value.filter(({ t }) => t >= now - 30 * 60)
  },
  { immediate: true },
)
async function control(action: 'start' | 'stop' | 'clear') {
  busy.value = true
  try {
    await run(async () => {
      const status = unwrap(
        action === 'clear'
          ? await api.DELETE('/api/xray/profile')
          : await api.POST(
              action === 'start' ? '/api/xray/restart' : '/api/xray/stop',
            ),
      )
      query.setQueryData(['status'], status)
      void query.invalidateQueries({ queryKey: ['router'] })
    }, { start: 'Xray запущен', stop: 'Xray остановлен', clear: 'VPN-профиль снят' }[action])
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <header class="page-header">
    <div>
      <h1>Обзор роутера</h1>
    </div>
    <div class="actions">
      <PanelUpdate />
    </div>
  </header>
  <p v-if="isError" class="inline-error" role="alert">
    Не удалось получить состояние роутера.
  </p>
  <section class="hero panel">
    <div class="hero-top">
      <span class="status-pill" :class="status?.xray"
        ><i />{{ stateLabel[status?.xray || ''] || 'Загрузка' }}</span
      >
    </div>
    <div>
      <h2>Xray Core</h2>
      <p>
        {{
          active?.name ||
          (status?.activeId ? 'Выбранный профиль' : 'Без VPN-профиля')
        }}
      </p>
    </div>
    <p v-if="status?.message" class="hero-message">{{ status.message }}</p>
    <div class="hero-footer">
      <div class="hero-details">
        <span
          ><Zap :size="15" />
          {{ status?.tproxy ? 'TProxy активен' : 'TProxy выключен' }} </span
        ><span
          >Zapret2
          {{
            zapret ? (zapret.running ? 'работает' : 'не запущен') : '—'
          }}</span
        ><span
          ><RotateCcw :size="14" /> Автоперезапуски:
          {{ status?.restarts ?? '—' }}</span
        >
      </div>
      <div class="actions">
        <button
          v-if="status?.xrayEnabled"
          class="button"
          :disabled="busy"
          @click="control('stop')"
        >
          <Square :size="14" /> Остановить
        </button>
        <button
          v-else
          class="button primary"
          :disabled="busy || !status"
          @click="control('start')"
        >
          <Play :size="16" /> Запустить
        </button>
        <button
          v-if="status?.xrayEnabled && status.xray === 'error'"
          class="button"
          :disabled="busy"
          @click="control('start')"
        >
          <RotateCcw :size="15" /> Повторить запуск
        </button>
        <button
          v-if="status?.activeId"
          class="button"
          :disabled="busy"
          @click="control('clear')"
        >
          Снять VPN-профиль
        </button>
        <RouterLink v-else to="/connections" class="button">
          Выбрать профиль <ArrowUpRight :size="16" />
        </RouterLink>
      </div>
    </div>
  </section>

  <section class="system-metrics" aria-labelledby="metrics-title">
    <div class="section-header">
      <h2 id="metrics-title">Системные метрики</h2>
      <label class="chart-period"
        >Период всех графиков
        <select v-model.number="period">
          <option v-for="minutes in periods" :key="minutes" :value="minutes">
            {{ minutes }} мин
          </option>
        </select>
      </label>
    </div>
    <div class="resource-panels">
      <article class="panel traffic">
        <div class="section-header">
          <h2><Cpu :size="17" /> Процессор</h2>
          <strong>{{ cpuPoints[0].length ? status?.cpu + ' %' : '—' }}</strong>
        </div>
        <TrafficChart
          :points="cpuPoints"
          :window="chartWindow"
          percent
          label="Загрузка процессора"
        />
      </article>
      <article class="panel traffic">
        <div class="section-header">
          <div>
            <h2><HardDrive :size="17" /> Память</h2>
            <p v-if="status?.memoryTotal">
              {{ formatBytes(status.memoryUsed) }} из
              {{ formatBytes(status.memoryTotal) }}
            </p>
          </div>
          <strong>{{ status?.memoryTotal ? ram + ' %' : '—' }}</strong>
        </div>
        <TrafficChart
          :points="ramPoints"
          :window="chartWindow"
          percent
          label="Использование оперативной памяти"
        />
      </article>
    </div>
  </section>
  <section class="panel traffic">
    <div class="section-header">
      <div>
        <h2>Трафик LAN</h2>
        <p>Последние {{ period }} мин · скорость</p>
      </div>
      <div class="traffic-legend">
        <span
          ><ArrowDownLeft :size="16" /> Приём
          {{ formatBytes(points[1].at(-1) || 0) }}/с</span
        ><span
          ><ArrowUpRight :size="16" /> Передача
          {{ formatBytes(points[2].at(-1) || 0) }}/с</span
        >
      </div>
    </div>
    <TrafficChart :points="points" :window="chartWindow" />
    <div class="traffic-total">
      <span>Xray · принято {{ formatBytes(status?.xrayRxBytes || 0) }}</span
      ><span>отправлено {{ formatBytes(status?.xrayTxBytes || 0) }}</span>
    </div>
  </section>
  <RouterPanels />
  <section class="panel storage-compact">
    <h3><HardDrive :size="16" /> Хранилище</h3>
    <div
      v-for="volume in router?.storage"
      :key="volume.path"
      class="storage-volume"
    >
      <small :title="volume.path">{{ volume.name }}</small>
      <progress
        :aria-label="'Занято на ' + volume.path"
        :value="Math.max(0, volume.total - volume.free)"
        :max="volume.total || 1"
      />
      <small
        ><strong>{{ formatBytes(volume.available) }}</strong> свободно из
        {{ formatBytes(volume.total) }}</small
      >
    </div>
    <small v-if="!router?.storage.length">Нет данных о разделах</small>
  </section>
  <footer class="router-uptime">
    <Timer :size="16" /> Время работы роутера
    <strong>{{ status?.uptime ? uptime : '—' }}</strong>
  </footer>
</template>
