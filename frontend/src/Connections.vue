<script setup lang="ts">
import { computed, defineAsyncComponent, ref, watch } from 'vue'
import { useQueryClient } from '@tanstack/vue-query'
import { useForm } from 'vee-validate'
import { toTypedSchema } from '@vee-validate/zod'
import { z } from 'zod'
import {
  ChevronRight,
  FileJson,
  Gauge,
  Link2,
  Network,
  Plus,
  RotateCcw,
  Search,
  Trash2,
  Zap,
} from 'lucide-vue-next'
import {
  api,
  unwrap,
  useActions,
  useInventory,
  useStatus,
  useUI,
  type Profile,
} from './client'
import Modal from './Modal.vue'
import Button from './components/ui/button/Button.vue'

const JsonEditor = defineAsyncComponent(() => import('./JsonEditor.vue'))
const { data, isPending, isError } = useInventory()
const { data: status } = useStatus()
const query = useQueryClient()
const ui = useUI()
const { run } = useActions()
const search = ref('')
const filter = ref('')
const sort = ref('subscription')
const leadingFlag = /^\s*(\p{Regional_Indicator}{2})\s*/u
const flagImages = import.meta.glob(
  '../node_modules/flag-icons/flags/4x3/*.svg',
  {
    eager: true,
    query: '?url&no-inline',
    import: 'default',
  },
) as Record<string, string>
function flag(name: string) {
  const emoji = leadingFlag.exec(name)?.[1]
  if (!emoji) return
  const code = Array.from(emoji, (part) =>
    String.fromCharCode(97 + (part.codePointAt(0)! - 0x1f1e6)),
  ).join('')
  return flagImages[`../node_modules/flag-icons/flags/4x3/${code}.svg`]
}
const nameOrder = new Intl.Collator('ru', {
  sensitivity: 'base',
  numeric: true,
})
const pageSize = 36
const limit = ref(pageSize)
const importer = ref(false)
const importMode = ref('link')
const editing = ref(false)
const configuration = ref('')
const editingId = ref('')
const pending = ref('')
const probing = ref(new Set<string>())
const batch = ref(false)
const completed = ref(0)
const total = ref(0)
const selectedDelete = ref<{ id: string; name: string; sub: boolean }>()
const confirmDelete = ref(false)
const visible = computed(() => {
  const term = search.value.trim().toLowerCase()
  const filtered = (data.value?.profiles || []).filter(
    (p) =>
      (!filter.value || p.sourceId === filter.value) &&
      [p.name, p.kind, p.host].some((value) =>
        value.toLowerCase().includes(term),
      ),
  )
  if (sort.value === 'name') {
    filtered.sort((a, b) => nameOrder.compare(a.name, b.name))
  } else if (sort.value === 'latency') {
    const latency = (p: Profile) => {
      const result = ui.probes[p.id]
      return result && !result.error ? result.latency : Number.MAX_SAFE_INTEGER
    }
    filtered.sort((a, b) => latency(a) - latency(b))
  }
  return filtered
})
const groups = computed(() => {
  const bySource = new Map<string, Profile[]>()
  for (const profile of visible.value) {
    const profiles = bySource.get(profile.sourceId) ?? []
    profiles.push(profile)
    bySource.set(profile.sourceId, profiles)
  }
  let remaining = limit.value
  return [undefined, ...(data.value?.subscriptions ?? [])].flatMap((sub) => {
    const id = sub?.id ?? ''
    if (filter.value && filter.value !== id) return []
    const matches = bySource.get(id) ?? []
    const profiles = matches.slice(0, remaining)
    remaining -= profiles.length
    return profiles.length || (sub && !search.value.trim() && !matches.length)
      ? [{ sub, profiles }]
      : []
  })
})
watch([search, filter, sort], () => {
  limit.value = pageSize
})
const { defineField, errors, handleSubmit, resetForm, isSubmitting } = useForm({
  validationSchema: toTypedSchema(
    z.object({
      name: z.string().max(100, 'Не более 100 символов'),
      input: z
        .string()
        .trim()
        .min(1, 'Вставьте ссылку или конфигурацию')
        .max(4194304, 'Размер больше 4 МБ'),
    }),
  ),
  initialValues: { name: '', input: '' },
})
const [name] = defineField('name')
const [input] = defineField('input')
const submit = handleSubmit(async (body) => {
  if (importMode.value === 'json') {
    try {
      JSON.parse(body.input)
    } catch {
      ui.notify('Проверь синтаксис JSON', true)
      return
    }
  }
  if (
    await run(
      async () =>
        unwrap(
          await api.POST('/api/import', { body: { ...body, refreshHours: 0 } }),
        ),
      'Профили импортированы',
    )
  ) {
    importer.value = false
    resetForm()
  }
})
async function change(
  id: string,
  action: () => Promise<unknown>,
  success?: string,
) {
  if (pending.value) return false
  pending.value = id
  try {
    return await run(action, success)
  } finally {
    pending.value = ''
  }
}
async function activate(p: Profile) {
  await change(
    p.id,
    async () => {
      const result = unwrap(
        await api.POST('/api/profiles/{id}/activate', {
          params: { path: { id: p.id } },
        }),
      )
      query.setQueryData(['status'], result)
    },
    'Выбран профиль «' + p.name + '»',
  )
}
async function clearActiveProfile() {
  await change(
    'clear-profile',
    async () => {
      const result = unwrap(await api.DELETE('/api/xray/profile'))
      query.setQueryData(['status'], result)
      void query.invalidateQueries({ queryKey: ['router'] })
    },
    'VPN-профиль снят',
  )
}
async function refresh(id: string) {
  if (
    await change(
      id,
      async () =>
        unwrap(
          await api.POST('/api/subscriptions/{id}/refresh', {
            params: { path: { id } },
          }),
        ),
      'Подписка обновлена',
    )
  ) {
    for (const p of data.value?.profiles || [])
      if (p.sourceId === id) delete ui.probes[p.id]
  }
}
async function openEditor(p: Profile) {
  await change(p.id, async () => {
    const result = unwrap(
      await api.GET('/api/profiles/{id}/config', {
        params: { path: { id: p.id } },
      }),
    )
    configuration.value = JSON.stringify(JSON.parse(result.input), null, 2)
    editingId.value = p.id
    editing.value = true
  })
}
async function saveConfig() {
  const id = editingId.value
  if (
    await change(
      id,
      async () =>
        unwrap(
          await api.PUT('/api/profiles/{id}/config', {
            params: { path: { id } },
            body: { input: configuration.value },
          }),
        ),
      'Конфигурация сохранена',
    )
  ) {
    editing.value = false
    delete ui.probes[id]
  }
}
function askDelete(id: string, label: string, sub = false) {
  selectedDelete.value = { id, name: label, sub }
  confirmDelete.value = true
}
async function remove() {
  const item = selectedDelete.value
  if (!item) return
  const options = { params: { path: { id: item.id } } }
  if (
    await change(
      item.id,
      async () =>
        unwrap(
          await api.DELETE(
            item.sub ? '/api/subscriptions/{id}' : '/api/profiles/{id}',
            options,
          ),
        ),
      'Удалено',
    )
  ) {
    if (filter.value === item.id) filter.value = ''
    confirmDelete.value = false
  }
}
async function check(ids: string[]) {
  const fresh = ids.filter((id) => !probing.value.has(id))
  if (!fresh.length) return
  for (const id of fresh) probing.value.add(id)
  try {
    const results = unwrap(
      await api.POST('/api/probe', { body: { ids: fresh } }),
    )
    for (const result of results) ui.probes[result.id] = result
  } catch (error) {
    ui.notify(error instanceof Error ? error.message : 'Сбой проверки', true)
  } finally {
    for (const id of fresh) probing.value.delete(id)
  }
}
async function checkAll() {
  const ids = visible.value.filter((p) => p.available).map((p) => p.id)
  batch.value = true
  total.value = ids.length
  completed.value = 0
  try {
    for (let index = 0; index < ids.length; index += 4) {
      const group = ids.slice(index, index + 4)
      await check(group)
      completed.value += group.length
    }
  } finally {
    batch.value = false
  }
}
</script>

<template>
  <header class="page-header">
    <div>
      <h1>VPN</h1>
    </div>
    <div class="actions">
      <button
        v-if="status?.activeId"
        class="button"
        :disabled="!!pending"
        @click="clearActiveProfile"
      >
        Снять VPN-профиль
      </button>
      <Button variant="primary" @click="importer = true">
        <Plus :size="18" /> Импортировать
      </Button>
    </div>
  </header>
  <p v-if="isError" class="inline-error" role="alert">
    Не удалось загрузить список подключений.
  </p>
  <section class="panel listing">
    <div class="section-header list-header">
      <div>
        <h2>
          Профили <span class="count">{{ data?.profiles.length ?? '—' }}</span>
        </h2>
      </div>
      <button
        class="button"
        :disabled="batch || !visible.some((p) => p.available)"
        @click="checkAll"
      >
        <Gauge :size="17" />
        {{ batch ? completed + ' / ' + total : 'Проверить все' }}
      </button>
    </div>
    <div v-if="data?.profiles.length" class="filters">
      <label class="search"
        ><Search :size="17" /><input
          v-model="search"
          aria-label="Поиск"
          placeholder="Поиск профилей"
      /></label>
      <select
        v-if="data.subscriptions.length"
        v-model="filter"
        aria-label="Подписка"
      >
        <option value="">Все источники</option>
        <option v-for="sub in data.subscriptions" :key="sub.id" :value="sub.id">
          {{ sub.name }}
        </option>
      </select>
      <select v-model="sort" aria-label="Сортировка профилей">
        <option value="subscription">Как в подписке</option>
        <option value="name">По названию</option>
        <option value="latency">По задержке</option>
      </select>
    </div>
    <div v-if="isPending || !visible.length" class="empty">
      <Network :size="30" /><strong>{{
        isPending
          ? 'Загрузка…'
          : data?.profiles.length
            ? 'Ничего не найдено'
            : 'Здесь будут ваши подключения'
      }}</strong>
      <p v-if="!isPending && !data?.profiles.length">
        Добавьте ссылку, подписку или JSON-конфигурацию Xray.
      </p>
    </div>
    <section
      v-for="{ sub, profiles } in groups"
      :key="sub?.id ?? 'standalone'"
      class="vpn-group"
    >
      <div v-if="sub" class="profile-row vpn-group-header">
        <span class="profile-icon"><Link2 :size="18" /></span>
        <div class="profile-info">
          <strong>{{ sub.name }}</strong><small
            >{{
              sub.updatedAt ? new Date(sub.updatedAt).toLocaleString('ru') : '—'
            }} </small
          ><small v-if="sub.error" class="failed">{{ sub.error }}</small>
        </div>
        <div class="row-actions">
          <button class="button" :disabled="!!pending" @click="refresh(sub.id)">
            <RotateCcw :size="16" :class="{ pulse: pending === sub.id }" />
            <span>Обновить</span>
          </button>
          <button
            class="icon-button"
            :disabled="!!pending"
            title="Удалить подписку"
            aria-label="Удалить подписку"
            @click="askDelete(sub.id, sub.name, true)"
          >
            <Trash2 :size="17" />
          </button>
        </div>
      </div>
      <h2 v-else class="vpn-standalone">Отдельные профили</h2>
      <div v-for="p in profiles" :key="p.id" class="profile-row">
        <span
          class="profile-icon"
          :class="{ selected: status?.activeId === p.id }"
          ><img
            v-if="flag(p.name)"
            :src="flag(p.name)"
            class="vpn-flag"
            alt=""
            decoding="async" /><Zap v-else :size="18"
        /></span>
        <div class="profile-info">
          <strong>{{
            (flag(p.name) && p.name.replace(leadingFlag, '')) || p.name
          }}</strong><small
            >{{ p.kind }}<template v-if="p.host"> · {{ p.host }}</template
            ><template v-if="!p.available">
              · удалён из подписки</template
            ></small
          >
        </div>
        <span v-if="status?.activeId === p.id" class="status-pill running"
          ><i />Выбран</span
        >
        <span
          v-if="ui.probes[p.id]"
          class="latency"
          :class="{ failed: ui.probes[p.id].error }"
          :title="ui.probes[p.id].error"
          >{{
            ui.probes[p.id].error
              ? 'Нет ответа'
              : ui.probes[p.id].latency + ' мс'
          }}</span
        >
        <div class="row-actions">
          <button
            class="icon-button"
            :disabled="probing.has(p.id)"
            title="Проверить задержку"
            aria-label="Проверить задержку"
            @click="check([p.id])"
          >
            <Gauge :size="18" :class="{ pulse: probing.has(p.id) }" />
          </button>
          <button
            class="icon-button"
            :disabled="!!pending"
            title="Редактировать JSON"
            aria-label="Редактировать JSON"
            @click="openEditor(p)"
          >
            <FileJson :size="18" />
          </button>
          <button
            class="icon-button"
            :disabled="!!pending || status?.activeId === p.id"
            title="Удалить профиль"
            aria-label="Удалить профиль"
            @click="askDelete(p.id, p.name)"
          >
            <Trash2 :size="17" />
          </button>
          <button
            class="icon-button connect"
            :disabled="!!pending || !p.available || status?.activeId === p.id"
            title="Выбрать профиль"
            aria-label="Выбрать профиль"
            @click="activate(p)"
          >
            <ChevronRight :size="19" />
          </button>
        </div>
      </div>
    </section>
    <div v-if="limit < visible.length" class="list-footer">
      <button class="button" @click="limit += pageSize">Показать ещё</button>
      <small>{{ limit }} из {{ visible.length }}</small>
    </div>
  </section>
  <Modal
    v-model="importer"
    title="Импорт подключений"
    description="VLESS, Hysteria2, SOCKS5, JSON Xray или URL подписки"
  >
    <form class="form" @submit="submit">
      <label
        >Формат
        <select v-model="importMode">
          <option value="link">Ссылка или строка подключения</option>
          <option value="json">JSON Xray</option>
        </select>
      </label>
      <label
        >Название <input v-model="name" placeholder="Необязательно" /><small
          v-if="errors.name"
          class="failed"
          >{{ errors.name }}</small
        ></label
      >
      <label
        >{{
          importMode === 'json'
            ? 'Конфигурация JSON'
            : 'Ссылка или строка подключения'
        }}
        <textarea
          v-model="input"
          rows="5"
          :placeholder="
            importMode === 'json'
              ? '{ &quot;outbounds&quot;: […] }'
              : 'https://… или vless://…'
          "
        /><small v-if="errors.input" class="failed">{{
          errors.input
        }}</small></label
      >
      <div class="actions">
        <Button variant="primary" type="submit" :disabled="isSubmitting">
          {{ isSubmitting ? 'Импорт…' : 'Импортировать' }}
        </Button>
      </div>
    </form>
  </Modal>
  <Modal
    v-model="editing"
    title="Конфигурация Xray"
    description="Измените JSON выбранного профиля"
  >
    <JsonEditor v-if="editing" v-model="configuration" />
    <div class="actions dialog-actions">
      <Button variant="primary" :disabled="!!pending" @click="saveConfig">
        Сохранить
      </Button>
    </div>
  </Modal>
  <Modal
    v-model="confirmDelete"
    title="Удаление"
    :description="'Удалить «' + (selectedDelete?.name || '') + '»?'"
  >
    <p v-if="selectedDelete?.sub" class="dialog-copy">
      Профили этой подписки также будут удалены.
    </p>
    <div class="actions dialog-actions">
      <button class="button" @click="confirmDelete = false">Отмена</button
      ><Button variant="destructive" :disabled="!!pending" @click="remove">
        Удалить
      </Button>
    </div>
  </Modal>
</template>
