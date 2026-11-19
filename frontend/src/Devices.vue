<script setup lang="ts">
import { computed, reactive } from 'vue'
import { useMutation, useQuery, useQueryClient } from '@tanstack/vue-query'
import { api, unwrap, useRouterDashboard, useUI } from './client'
import InfoHint from './InfoHint.vue'

const ui = useUI()
const query = useQueryClient()
const { data: router, isError: routerError } = useRouterDashboard()
const {
  data: settings,
  isPending,
  isError,
} = useQuery({
  queryKey: ['routing-settings'],
  queryFn: async () => unwrap(await api.GET('/api/settings/routing')),
})
const drafts = reactive<Record<string, { name: string; preset: string }>>({})
const clients = computed(() => {
  const known = new Map(
    (router.value?.clients ?? []).map((client) => [client.mac, client]),
  )
  const macs = new Set([
    ...known.keys(),
    ...Object.keys(settings.value?.deviceNames ?? {}),
    ...(settings.value?.presets ?? []).flatMap((preset) => preset.macs),
  ])
  return [...macs].sort().map((mac) => {
    const client = known.get(mac)
    const addresses = client?.addresses ?? []
    return {
      mac,
      ipv4: addresses.filter((address) => !address.includes(':')),
      ipv6: addresses.filter((address) => address.includes(':')),
      hostname: client?.hostname,
      name: settings.value?.deviceNames?.[mac] ?? '',
      preset:
        settings.value?.presets.find((preset) => preset.macs.includes(mac))
          ?.name ?? '',
    }
  })
})
function change(mac: string, name: string, preset: string) {
  drafts[mac] = { name, preset }
}
const { mutate: save, isPending: saving } = useMutation({
  mutationFn: async (mac: string) => {
    const draft = drafts[mac]
    const latest = unwrap(await api.GET('/api/settings/routing'))
    const current =
      latest.presets.find((preset) => preset.macs.includes(mac))?.name ?? ''
    if (current !== draft.preset) {
      const selected = latest.presets.find(
        (preset) => preset.name === draft.preset,
      )
      if (draft.preset && !selected)
        throw new Error('Пресет больше не существует')
      for (const preset of latest.presets) {
        if (preset.name === current)
          preset.macs = preset.macs.filter((value) => value !== mac)
      }
      selected?.macs.push(mac)
    }
    const deviceNames = { ...latest.deviceNames }
    const name = draft.name.trim()
    if (name) deviceNames[mac] = name
    else delete deviceNames[mac]
    return unwrap(
      await api.PUT('/api/settings/routing', {
        body: { ...latest, deviceNames },
      }),
    )
  },
  onSuccess(value, mac) {
    delete drafts[mac]
    query.setQueryData(['routing-settings'], value)
    void query.invalidateQueries({ queryKey: ['router'] })
    ui.notify('Устройство сохранено')
  },
  onError(error) {
    ui.notify(error.message, true)
  },
})
</script>

<template>
  <header class="page-header">
    <div>
      <h1>Устройства</h1>
    </div>
    <RouterLink class="button" to="/routing">Маршрутизация</RouterLink>
  </header>
  <p class="device-mac-note">
    Для этой сети Wi‑Fi выбери постоянный MAC: отключи смену случайного MAC или
    включи фиксированный частный адрес. При смене MAC роутер увидит новое
    устройство, и сохранённые имя и пресет к нему не применятся.
  </p>
  <p v-if="isPending" role="status">Загрузка устройств…</p>
  <p v-else-if="isError" class="inline-error">
    Не удалось загрузить назначения.
  </p>
  <section v-else class="panel settings-panel form device-list">
    <p
      v-if="routerError || (router && !router.clientsAvailable)"
      class="inline-error"
    >
      Список соседей недоступен. Показаны сохранённые устройства.
    </p>
    <p v-if="!clients.length" class="router-muted">
      Устройства пока не обнаружены.
    </p>
    <form
      v-for="client in clients"
      :key="client.mac"
      class="routing-rule form"
      @submit.prevent="save(client.mac)"
    >
      <div class="device-summary">
        <strong>{{ client.name || client.hostname || client.mac }}</strong>
        <dl class="device-addresses">
          <dt>MAC</dt>
          <dd>{{ client.mac }}</dd>
          <template v-if="client.ipv4.length">
            <dt>IPv4</dt>
            <dd>
              <span v-for="address in client.ipv4" :key="address">{{
                address
              }}</span>
            </dd>
          </template>
          <template v-if="client.ipv6.length">
            <dt>IPv6</dt>
            <dd>
              <span v-for="address in client.ipv6" :key="address">{{
                address
              }}</span>
            </dd>
          </template>
          <template v-if="!client.ipv4.length && !client.ipv6.length">
            <dt>IP</dt>
            <dd class="router-muted">Нет текущего IP</dd>
          </template>
        </dl>
        <small
          v-if="
            client.name && client.hostname && client.name !== client.hostname
          "
        >
          Имя в сети · {{ client.hostname }}
        </small>
      </div>
      <div class="routing-fields">
        <label
          >Имя устройства
          <input
            :value="drafts[client.mac]?.name ?? client.name"
            maxlength="80"
            placeholder="Например, телевизор"
            :disabled="saving"
            @input="
              change(
                client.mac,
                ($event.target as HTMLInputElement).value,
                drafts[client.mac]?.preset ?? client.preset,
              )
            "
          />
        </label>
        <label
          ><span
            >Пресет
            <InfoHint
              text="Выбери, по каким правилам это устройство будет выходить в интернет. Без назначения по MAC действуют общие правила или существующая привязка по IP."
          /></span>
          <select
            :value="drafts[client.mac]?.preset ?? client.preset"
            :disabled="saving"
            @change="
              change(
                client.mac,
                drafts[client.mac]?.name ?? client.name,
                ($event.target as HTMLSelectElement).value,
              )
            "
          >
            <option value="">Без привязки по MAC</option>
            <option v-for="preset in settings?.presets" :key="preset.name">
              {{ preset.name }}
            </option>
          </select>
        </label>
      </div>
      <div v-if="drafts[client.mac]" class="actions">
        <button class="button primary" :disabled="saving" type="submit">
          Сохранить
        </button>
        <button
          class="button"
          :disabled="saving"
          type="button"
          @click="delete drafts[client.mac]"
        >
          Отмена
        </button>
      </div>
    </form>
  </section>
</template>
