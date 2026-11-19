<script setup lang="ts">
import { ref, watch } from 'vue'
import { useMutation, useQuery, useQueryClient } from '@tanstack/vue-query'
import { api, unwrap, useInventory, useUI } from './client'
import type { components } from './api.gen'
import InfoHint from './InfoHint.vue'

type Preset = components['schemas']['RoutePreset']
type Rule = components['schemas']['RouteRule']
type RuleDraft = Omit<Rule, 'domains' | 'ips' | 'protocols'> & {
  domains: string
  ips: string
  protocols: string
}
type Draft = Omit<Preset, 'rules'> & {
  key: number
  originalName: string
  rules: RuleDraft[]
}
const actions = ['DIRECT', 'VPN', 'ZAPRET', 'BLOCK']
const actionHint =
  'DIRECT — обычный интернет. VPN — через туннель. ZAPRET — напрямую через обработку Zapret2. BLOCK — запретить соединение.'
const { data: inventory } = useInventory()
const ui = useUI()
const query = useQueryClient()
const presets = ref<Draft[]>([])
const queue = ref(200)
const defaultKey = ref<number | null>(null)
let nextKey = 0
const split = (text: string) =>
  text
    .split(/\r?\n|,/)
    .map((value) => value.trim())
    .filter(Boolean)
const { data, isPending, isError, refetch } = useQuery({
  queryKey: ['routing-settings'],
  queryFn: async () => unwrap(await api.GET('/api/settings/routing')),
  staleTime: Infinity,
})
watch(
  data,
  (value) => {
    if (!value) return
    queue.value = value.queue
    presets.value = value.presets.map((preset) => ({
      ...preset,
      key: nextKey++,
      originalName: preset.name,
      macs: preset.macs || [],
      ips: preset.ips || [],
      rules: (preset.rules || []).map((rule) => ({
        ...rule,
        domains: (rule.domains || []).join('\n'),
        ips: (rule.ips || []).join('\n'),
        protocols: (rule.protocols || []).join(', '),
      })),
    }))
    defaultKey.value =
      presets.value.find((preset) => preset.name === value.defaultPreset)
        ?.key ?? null
  },
  { immediate: true },
)
const {
  mutate: save,
  isPending: saving,
  error,
} = useMutation({
  mutationFn: async () => {
    const latest = unwrap(await api.GET('/api/settings/routing'))
    return unwrap(
      await api.PUT('/api/settings/routing', {
        body: {
          queue: Number(queue.value),
          defaultPreset:
            presets.value
              .find((preset) => preset.key === defaultKey.value)
              ?.name.trim() ?? '',
          deviceNames: latest.deviceNames,
          presets: presets.value.map((preset) => {
            const saved = latest.presets.find(
              (item) => item.name === preset.originalName,
            )
            return {
              name: preset.name,
              action: preset.action,
              fallback: preset.fallback,
              profileId: preset.profileId,
              outboundTag: preset.outboundTag,
              macs: saved?.macs ?? preset.macs,
              ips: saved?.ips ?? preset.ips,
              rules: preset.rules.map(
                ({ domains, ips, protocols, ...rule }) => ({
                  ...rule,
                  domains: split(domains),
                  ips: split(ips),
                  protocols: split(protocols),
                }),
              ),
            }
          }),
        },
      }),
    )
  },
  onSuccess(value) {
    query.setQueryData(['routing-settings'], value)
    ui.notify('Пресеты маршрутизации сохранены')
  },
})
function addPreset() {
  presets.value.push({
    key: nextKey++,
    name: '',
    originalName: '',
    macs: [],
    ips: [],
    profileId: '',
    outboundTag: '',
    action: 'VPN',
    fallback: 'BLOCK',
    rules: [],
  })
}
function addRule(preset: Draft) {
  preset.rules.push({
    domains: '',
    ips: '',
    ports: '',
    network: '',
    protocols: '',
    action: 'DIRECT',
    outboundTag: '',
  })
}
</script>

<template>
  <header class="page-header">
    <div>
      <h1>Маршрутизация</h1>
    </div>
  </header>
  <p v-if="isPending" role="status">Загрузка пресетов…</p>
  <div v-else-if="isError">
    <p class="inline-error">Не удалось загрузить пресеты.</p>
    <button class="button" @click="refetch()">Повторить</button>
  </div>
  <form v-else class="form" @submit.prevent="save()">
    <fieldset class="routing-form" :disabled="saving">
      <section class="panel settings-panel">
        <label
          ><span
            >Общие правила для устройств без назначения
            <InfoHint
              text="Этот пресет действует на устройства, которым не назначили отдельный. Если выбрана маршрутизация активного профиля, Xray применит его собственные правила."
            />
          </span>
          <select v-model="defaultKey">
            <option :value="null">Маршрутизация активного профиля</option>
            <option
              v-for="preset in presets"
              :key="preset.key"
              :value="preset.key"
            >
              {{ preset.name || 'Новый пресет' }}
            </option>
          </select>
        </label>
        <small
          >Назначения отдельных клиентов доступны во вкладке
          «Устройства».</small
        >
      </section>
      <article
        v-for="(preset, index) in presets"
        :key="preset.key"
        class="panel settings-panel routing-panel"
      >
        <div class="section-header">
          <h2>
            {{ preset.name || 'Новый пресет' }}
            <InfoHint
              text="Пресет — набор правил выхода в интернет. Его можно назначить одному или нескольким клиентам во вкладке «Устройства»."
            />
          </h2>
          <button
            class="button"
            type="button"
            @click="presets.splice(index, 1)"
          >
            Удалить
          </button>
        </div>
        <label
          ><span
            >Имя
            <InfoHint
              text="Название, по которому ты найдёшь этот набор правил во вкладке «Устройства»."
          /></span>
          <input v-model="preset.name" maxlength="80" required
        /></label>
        <div class="routing-fields">
          <label
            ><span
              >Действие по умолчанию
              <InfoHint
                :text="
                  'Куда пойдёт соединение, если ни одно правило ниже не подошло. ' +
                  actionHint
                "
            /></span>
            <select v-model="preset.action">
              <option v-for="action in actions" :key="action">
                {{ action }}
              </option>
            </select>
          </label>
          <label
            ><span
              >Запасное действие
              <InfoHint
                text="Что делать, если выбранный VPN-выход перестал отвечать или Zapret2 выключен. Если запасное действие тоже ZAPRET и он выключен, соединение блокируется. Балансировщик импортированного профиля использует свои настройки."
            /></span>
            <select v-model="preset.fallback">
              <option>BLOCK</option>
              <option>VPN</option>
              <option>ZAPRET</option>
              <option>DIRECT</option>
            </select>
          </label>
          <label
            ><span
              >VPN-профиль
              <InfoHint
                text="Какой VPN использовать для действия VPN. Можно оставить активный профиль или выбрать здесь отдельный профиль с одним VPN-выходом."
            /></span>
            <select v-model="preset.profileId">
              <option value="">Из активного профиля</option>
              <option
                v-for="profile in inventory?.profiles"
                :key="profile.id"
                :value="profile.id"
              >
                {{ profile.name }}
              </option>
            </select>
          </label>
          <label
            ><span
              >Или тег outbound активного Xray
              <InfoHint
                text="Внутреннее имя конкретного VPN-выхода в JSON активного профиля. Если не знаешь его, оставь поле пустым."
            /></span>
            <input
              v-model.trim="preset.outboundTag"
              placeholder="Необязательно"
          /></label>
        </div>
        <details>
          <summary>
            Правила · {{ preset.rules.length }}
            <InfoHint
              text="Xray проверяет правила сверху вниз и выполняет первое подходящее. Если в правиле заполнено несколько разных полей, должны совпасть все эти условия."
            />
          </summary>
          <div
            v-for="(rule, ri) in preset.rules"
            :key="ri"
            class="routing-rule"
          >
            <div class="routing-fields">
              <label
                ><span
                  >Домены и GeoSite
                  <InfoHint
                    text="Укажи сайт, например domain:example.com, чтобы учесть и его поддомены. GeoSite — готовые списки сайтов, например geosite:youtube."
                /></span>
                <textarea
                  v-model="rule.domains"
                  class="routing-auto-height"
                  :rows="Math.max(2, rule.domains.split('\n').length)"
                  placeholder="domain:example.com, geosite:youtube"
                />
              </label>
              <label
                ><span
                  >IP/CIDR и GeoIP
                  <InfoHint
                    text="Срабатывает по адресу назначения. Можно указать один IP, целую подсеть вроде 192.0.2.0/24 или готовую группу адресов страны, например geoip:ru."
                /></span>
                <textarea
                  v-model="rule.ips"
                  class="routing-auto-height"
                  :rows="Math.max(2, rule.ips.split('\n').length)"
                  placeholder="geoip:private"
                />
              </label>
              <label
                ><span
                  >Порты
                  <InfoHint
                    text="Ограничивает правило портами назначения. Например, 443 — типичный порт HTTPS. Пустое поле означает любой порт."
                /></span>
                <input v-model.trim="rule.ports" placeholder="80,443,1000-2000"
              /></label>
              <label
                ><span
                  >Сеть
                  <InfoHint
                    text="Выбирает тип передачи. TCP используется, например, для обычного HTTPS, а UDP — для QUIC и многих звонков. Если разница не нужна, оставь «Любая»."
                /></span>
                <select v-model="rule.network">
                  <option value="">Любая</option>
                  <option>tcp</option>
                  <option>udp</option>
                  <option>tcp,udp</option>
                </select></label
              >
              <label
                ><span
                  >Протокол sniffing
                  <InfoHint
                    text="Xray пытается распознать тип трафика по первым пакетам, например HTTP, TLS или QUIC. Оставь пустым, если правило должно работать независимо от этого."
                /></span>
                <input v-model="rule.protocols" placeholder="http, tls, quic"
              /></label>
              <label
                ><span>Действие <InfoHint :text="actionHint" /></span>
                <select v-model="rule.action">
                  <option v-for="action in actions" :key="action">
                    {{ action }}
                  </option>
                </select></label
              >
              <label v-if="rule.action === 'VPN'"
                ><span
                  >Тег outbound
                  <InfoHint
                    text="Особый VPN-выход только для этого правила. Если поле пустое, используется VPN-выход пресета."
                /></span>
                <input
                  v-model.trim="rule.outboundTag"
                  placeholder="По умолчанию из пресета"
              /></label>
            </div>
            <div class="actions">
              <button
                type="button"
                class="button"
                :disabled="ri === 0"
                @click="
                  preset.rules.splice(ri - 1, 0, preset.rules.splice(ri, 1)[0])
                "
              >
                Выше</button
              ><button
                type="button"
                class="button"
                @click="preset.rules.splice(ri, 1)"
              >
                Удалить правило
              </button>
            </div>
          </div>
          <button type="button" class="button" @click="addRule(preset)">
            Добавить правило
          </button>
        </details>
      </article>
      <div class="actions">
        <button
          class="button"
          type="button"
          :disabled="presets.length >= 16"
          @click="addPreset"
        >
          Добавить пресет
        </button>
      </div>
      <label v-if="presets.length"
        ><span
          >Номер очереди NFQUEUE
          <InfoHint
            text="Внутренний номер очереди, по которой firewall передаёт пакеты в Zapret2. Меняй только если этот номер уже используется другой службой."
        /></span>
        <input
          v-model.number="queue"
          type="number"
          min="0"
          max="65535"
          required
      /></label>
      <small
        >Правила выполняются сверху вниз. Балансировщик активного профиля
        использует собственные настройки переключения. Для ZAPRET используются
        параметры из вкладки Zapret2.</small
      >
      <p v-if="error" class="inline-error" role="alert">{{ error.message }}</p>
      <div class="actions">
        <button class="button primary" type="submit">
          {{ saving ? 'Применяем…' : 'Сохранить' }}
        </button>
      </div>
    </fieldset>
  </form>
</template>
