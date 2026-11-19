<script setup lang="ts">
import InfoHint from './InfoHint.vue'
import { computed, ref } from 'vue'
import { useMutation } from '@tanstack/vue-query'
import {
  Activity,
  ArrowUpRight,
  Globe2,
  Network,
  Shield,
  Users,
} from 'lucide-vue-next'
import {
  api,
  formatBytes,
  unwrap,
  useRouterDashboard,
  useStatus,
} from './client'

const { data: status } = useStatus()
const { data, isError } = useRouterDashboard()
const {
  mutate: check,
  data: external,
  isPending: checking,
  error: checkError,
} = useMutation({
  mutationFn: async () => unwrap(await api.POST('/api/router/external-check')),
})
const clientLimit = ref(12)
const activeClients = computed(
  () => data.value?.clients.filter((client) => client.sessions > 0).length || 0,
)
const clients = computed(() =>
  [...(data.value?.clients || [])].sort(
    (a, b) => b.sessions - a.sessions || b.bytesPerSecond - a.bytesPerSecond,
  ),
)
const endpoints = computed(
  () =>
    data.value?.outbounds.filter((outbound) => outbound.category === 'vpn') ||
    [],
)
const categories = computed(() => {
  const names = {
    vpn: 'Через VPN',
    zapret: 'Через Zapret2',
    direct: 'Напрямую через Xray',
    blocked: 'В блокирующий outbound',
  }
  return Object.entries(names).map(([key, label]) => ({
    label,
    bytes: (data.value?.outbounds || [])
      .filter((outbound) => outbound.category === key)
      .reduce((total, outbound) => total + outbound.rx + outbound.tx, 0),
  }))
})
const labels: Record<string, string> = {
  probe: 'Недоступный профиль',
  config: 'Конфигурация',
  connection: 'Подключение',
  crash: 'Падение процесса',
  dns: 'DNS',
  tproxy: 'TProxy',
}
const when = (value?: string) =>
  value ? new Date(value).toLocaleString('ru') : '—'
const rate = (value: number) =>
  new Intl.NumberFormat('ru', { maximumFractionDigits: 1 }).format(value)
</script>

<template>
  <section class="panel router-section" aria-labelledby="external-title">
    <div class="section-header">
      <div>
        <h2 id="external-title">
          <Globe2 :size="17" /> Внешнее подключение
          <InfoHint
            text="Публичный IP и сеть, которые видны сайтам. Панель проверяет обычный выход роутера и выход через Xray отдельно."
          />
        </h2>
      </div>
      <button class="button" :disabled="checking" @click="check()">
        <ArrowUpRight :size="16" />
        {{ checking ? 'Проверяем…' : 'Проверить выход' }}
      </button>
    </div>
    <p class="router-muted">
      DNS роутера · {{ data?.dnsServers.join(', ') || 'нет данных' }}
    </p>
    <p v-if="checkError" class="inline-error">{{ checkError.message }}</p>
    <div
      v-if="external"
      class="router-table-wrap"
      tabindex="0"
      aria-label="Результаты проверки внешнего подключения"
    >
      <table class="router-table">
        <thead>
          <tr>
            <th>Маршрут</th>
            <th>Публичный адрес</th>
            <th>Страна / ASN</th>
            <th>Ответ</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="route in external.routes" :key="route.mode + route.family">
            <td>
              {{ route.mode === 'vpn' ? 'Через Xray' : 'Напрямую' }} · IPv{{
                route.family
              }}
            </td>
            <td>{{ route.ip || '—' }}</td>
            <td>
              {{
                [route.country, route.asn].filter(Boolean).join(' · ') ||
                route.geoError ||
                '—'
              }}
            </td>
            <td :class="{ failed: route.error }">
              {{ route.error || route.latency + ' мс' }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <p class="router-muted">
      {{
        external
          ? 'Проверено ' + when(external.checkedAt)
          : 'Запросы отправляются в ipify и ipwho.is по кнопке.'
      }}
    </p>
  </section>
  <p v-if="isError" class="inline-error router-section" role="alert">
    Не удалось получить расширенные метрики.
  </p>

  <div class="router-grid">
    <section class="panel router-section" aria-labelledby="tunnel-title">
      <div class="section-header">
        <div>
          <h2 id="tunnel-title"><Shield :size="17" /> VPN-туннель</h2>
        </div>
      </div>
      <ul v-if="endpoints.length" class="router-list">
        <li v-for="(outbound, index) in endpoints" :key="index">
          <strong>{{
            outbound.protocol === 'hysteria'
              ? 'HY2'
              : outbound.protocol.toUpperCase()
          }}</strong><span>{{
            outbound.address || outbound.tag || 'Адрес задаётся конфигурацией'
          }}</span>
        </li>
      </ul>
      <p v-else class="router-muted">
        {{
          status?.xray === 'running'
            ? 'В текущей конфигурации нет VPN-outbound.'
            : 'VPN-туннель остановлен.'
        }}
      </p>
      <dl class="router-facts">
        <div>
          <dt>Задержка HTTPS через Xray</dt>
          <dd>
            {{
              data?.health.checkedAt && !data.health.error
                ? data.health.rtt + ' мс'
                : '—'
            }}
          </dd>
        </div>
        <div>
          <dt>Сбои HTTPS-проверок</dt>
          <dd>
            {{
              data?.health.samples
                ? data.health.failed + ' из ' + data.health.samples
                : '—'
            }}
          </dd>
        </div>
        <div>
          <dt>RTT до сервера · ICMP</dt>
          <dd>
            {{
              data?.icmp.available && data.icmp.loss < 100
                ? rate(data.icmp.rtt) + ' мс'
                : '—'
            }}
          </dd>
        </div>
        <div>
          <dt>Потери пакетов · ICMP</dt>
          <dd>
            {{ data?.icmp.available ? rate(data.icmp.loss) + ' %' : '—' }}
          </dd>
        </div>
        <div>
          <dt>Последний реконнект</dt>
          <dd>{{ when(data?.reconnectedAt) }}</dd>
        </div>
        <div>
          <dt>Успешные реконнекты</dt>
          <dd>{{ data?.reconnects ?? '—' }}</dd>
        </div>
      </dl>
      <p
        v-if="data?.health.error && status?.xray === 'running'"
        class="router-muted failed"
      >
        Последняя проверка через Xray не прошла.
      </p>
      <p v-if="data?.icmp.error" class="router-muted">{{ data.icmp.error }}</p>
      <p
        v-else-if="data?.icmp.available && data.icmp.loss === 100"
        class="router-muted"
      >
        Сервер или сеть могут фильтровать ICMP.
      </p>
    </section>

    <section class="panel router-section" aria-labelledby="routing-title">
      <div class="section-header">
        <div>
          <h2 id="routing-title">
            <Network :size="17" /> Маршрутизация
            <InfoHint
              text="Показывает, какие правила сейчас загружены в Xray и сколько трафика прошло через его выходы."
            />
          </h2>
        </div>
        <span class="chip">{{
          status?.tproxy ? 'TProxy включён' : 'TProxy выключен'
        }}</span>
      </div>
      <dl class="router-facts">
        <div v-for="category in categories" :key="category.label">
          <dt>{{ category.label }}</dt>
          <dd>
            {{ data?.statsAvailable ? formatBytes(category.bytes) : '—' }}
          </dd>
        </div>
        <div>
          <dt>Мимо Xray · пересылка LAN</dt>
          <dd>
            {{
              data?.forwarded.available
                ? formatBytes(data.forwarded.rx + data.forwarded.tx)
                : '—'
            }}
          </dd>
        </div>
      </dl>
      <p class="router-muted">
        Пересылка LAN учитывается nftables с последнего применения TProxy.
        Аппаратный offload может снижать показания.
      </p>
      <details v-if="data?.routingRules.length" class="routing-rules">
        <summary>Активные правила · {{ data.routingRules.length }}</summary>
        <ol>
          <li v-for="(rule, index) in data.routingRules" :key="index">
            {{ rule || 'Правило без отображаемых условий' }}
          </li>
        </ol>
      </details>
      <p v-else class="router-muted">Активных правил Xray нет.</p>
    </section>
  </div>

  <section class="panel router-section" aria-labelledby="clients-title">
    <div class="section-header">
      <div>
        <h2 id="clients-title"><Users :size="17" /> Устройства LAN</h2>
        <p>
          {{
            data?.connections.available
              ? activeClients + ' с активными сессиями · '
              : ''
          }}{{ clients.length }} в таблице соседей
        </p>
      </div>
    </div>
    <p v-if="data && !data.clientsAvailable" class="router-muted">
      Таблица соседей LAN недоступна. Эта метрика собирается на Linux / OpenWrt
      через iproute2.
    </p>
    <p v-else-if="!clients.length" class="router-muted">
      Ожидание устройств в таблице соседей.
    </p>
    <template v-else>
      <div
        class="router-table-wrap"
        tabindex="0"
        aria-label="Устройства LAN и их трафик"
      >
        <table class="router-table">
          <thead>
            <tr>
              <th>IP / MAC</th>
              <th>Политика VPN</th>
              <th>Сессии</th>
              <th>Скорость</th>
              <th>Учтено</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="device in clients.slice(0, clientLimit)"
              :key="device.mac"
            >
              <td>
                <strong>{{
                  device.alias ||
                  device.hostname ||
                  device.addresses.join(', ') ||
                  device.mac
                }}</strong>
                <small v-if="device.alias || device.hostname">{{
                  device.addresses.join(', ') || '—'
                }}</small>
                <small>{{ device.mac }}</small>
              </td>
              <td>{{ device.profile || '—' }}</td>
              <td>{{ data?.connections.available ? device.sessions : '—' }}</td>
              <td>
                {{
                  device.accounted
                    ? formatBytes(device.bytesPerSecond) + '/с'
                    : '—'
                }}
              </td>
              <td>
                {{
                  device.rx || device.tx || device.accounted
                    ? formatBytes(device.rx + device.tx)
                    : '—'
                }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <button
        v-if="clients.length > clientLimit"
        class="button"
        @click="clientLimit += 12"
      >
        Показать ещё
      </button>
      <p class="router-muted">
        Счётчики conntrack отражают наблюдаемый трафик. Для скорости нужны
        включённые счётчики байтов в ядре.
      </p>
    </template>
  </section>

  <div class="router-grid">
    <section class="panel router-section" aria-labelledby="sessions-title">
      <div class="section-header">
        <div>
          <h2 id="sessions-title">
            <Activity :size="17" /> Соединения LAN
            <InfoHint
              text="Сколько сетевых соединений роутер сейчас отслеживает. Один сайт или приложение может открыть несколько соединений одновременно."
            />
          </h2>
        </div>
      </div>
      <template v-if="data?.connections.available">
        <dl class="router-facts">
          <div>
            <dt>TCP</dt>
            <dd>{{ data.connections.tcp }}</dd>
          </div>
          <div>
            <dt>UDP</dt>
            <dd>{{ data.connections.udp }}</dd>
          </div>
          <div>
            <dt>Новых в секунду · оценка</dt>
            <dd>{{ rate(data.connections.newPerSecond) }}</dd>
          </div>
        </dl>
        <p v-if="data.connections.truncated" class="router-muted">
          Показана выборка из первых 20 000 записей.
        </p>
        <details
          v-if="data.connections.destinations.length"
          class="routing-rules"
        >
          <summary>Активные назначения</summary>
          <ul class="router-list">
            <li
              v-for="item in data.connections.destinations"
              :key="item.address"
            >
              <span>{{ item.address }}</span><strong
                >{{ item.sessions }} сес. ·
                {{
                  data.connections.accounting ? formatBytes(item.bytes) : '—'
                }}</strong
              >
            </li>
          </ul>
        </details>
      </template>
      <p v-else class="router-muted">
        Сессии недоступны без таблицы соседей и conntrack.
      </p>
      <details v-if="data?.outbounds.length" class="routing-rules">
        <summary>Outbound Xray · подключения в журнале</summary>
        <ul class="router-list">
          <li v-for="(item, index) in data.outbounds" :key="index">
            <span>{{ item.tag || item.protocol }}</span><strong
              >{{ item.observedConnections }} ·
              {{
                data.statsAvailable ? formatBytes(item.rx + item.tx) : '—'
              }}</strong
            >
          </li>
        </ul>
      </details>
    </section>
    <section class="panel router-section" aria-labelledby="diag-title">
      <div class="section-header">
        <div>
          <h2 id="diag-title">Диагностика</h2>
        </div>
        <RouterLink class="button" to="/logs"
          >Журнал <ArrowUpRight :size="15"
        /></RouterLink>
      </div>
      <dl class="router-facts">
        <div>
          <dt>Конфигурация применена</dt>
          <dd>{{ when(data?.appliedAt) }}</dd>
        </div>
        <div>
          <dt>Неожиданные завершения</dt>
          <dd>{{ data?.crashes ?? '—' }}</dd>
        </div>
      </dl>
      <ul v-if="data?.events.length" class="events-list">
        <li v-for="(event, index) in [...data.events].reverse()" :key="index">
          <small
            >{{ labels[event.kind] || event.kind
            }}<template v-if="event.at">
              · {{ when(event.at) }}</template
            ></small
          >
          <span>{{ event.message }}</span>
        </li>
      </ul>
      <p v-else class="router-muted">Ошибок в текущем сеансе нет.</p>
    </section>
  </div>
</template>
