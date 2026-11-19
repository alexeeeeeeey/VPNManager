<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'
import { useQueryClient } from '@tanstack/vue-query'
import {
  CircleAlert,
  CircleCheck,
  LayoutDashboard,
  LogOut,
  Globe2,
  Network,
  Route,
  ScrollText,
  Settings,
  Terminal,
  Users,
  X,
} from 'lucide-vue-next'
import { useUI, type Status } from './client'

const ui = useUI()
const query = useQueryClient()
const pages = [
  { to: '/', label: 'Обзор', icon: LayoutDashboard },
  { to: '/dns', label: 'DNS', icon: Globe2 },
  { to: '/connections', label: 'VPN', icon: Network },
  { to: '/zapret', label: 'Zapret2', icon: Terminal },
  {
    to: '/routing',
    label: 'Маршрутизация',
    shortLabel: 'Правила',
    icon: Route,
  },
  { to: '/devices', label: 'Устройства', shortLabel: 'Клиенты', icon: Users },
  { to: '/logs', label: 'Журнал', icon: ScrollText },
  { to: '/settings', label: 'Настройки', icon: Settings },
]
let socket: WebSocket | undefined
let retry: ReturnType<typeof setTimeout>
let disposed = false
function connect() {
  if (disposed) return
  socket = new WebSocket(
    new URL('/api/live', location.origin.replace(/^http/, 'ws')),
  )
  socket.onmessage = (event) => {
    const data = JSON.parse(event.data) as Status
    ui.live = true
    query.setQueryData(['status'], data)
  }
  socket.onclose = () => {
    ui.live = false
    if (!disposed) retry = setTimeout(connect, 3000)
  }
  socket.onerror = () => socket?.close()
}
onMounted(connect)
onUnmounted(() => {
  disposed = true
  clearTimeout(retry)
  socket?.close()
})
</script>

<template>
  <div class="shell">
    <aside class="sidebar">
      <RouterLink class="brand" to="/"> VPN MANAGER </RouterLink>
      <nav aria-label="Разделы">
        <RouterLink
          v-for="page in pages"
          :key="page.to"
          :to="page.to"
          class="nav-item"
        >
          <component :is="page.icon" :size="18" />
          <span :class="{ 'nav-label-wide': page.shortLabel }">{{
            page.label
          }}</span>
          <span v-if="page.shortLabel" class="nav-label-short">{{
            page.shortLabel
          }}</span>
        </RouterLink>
      </nav>
      <form class="sidebar-logout" action="/logout" method="post">
        <button class="nav-item w-full" type="submit">
          <LogOut :size="18" /> Выход
        </button>
      </form>
    </aside>
    <main class="main"><RouterView /></main>
    <Transition>
      <div
        v-if="ui.notice"
        class="toast"
        :class="{ danger: ui.notice.error }"
        role="status"
      >
        <CircleAlert v-if="ui.notice.error" :size="20" /><CircleCheck
          v-else
          :size="20"
        />
        <span>{{ ui.notice.text }}</span>
        <button
          class="icon-button"
          aria-label="Закрыть уведомление"
          @click="ui.notice = undefined"
        >
          <X :size="18" />
        </button>
      </div>
    </Transition>
  </div>
</template>
