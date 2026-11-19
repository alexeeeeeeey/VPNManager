import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { VueQueryPlugin } from '@tanstack/vue-query'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import './style.css'
import { applyAccent, savedAccent } from './accent'

applyAccent(savedAccent())

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: () => import('./Dashboard.vue') },
    {
      path: '/connections',
      component: () => import('./Connections.vue'),
    },
    { path: '/logs', component: () => import('./Logs.vue') },
    { path: '/dns', component: () => import('./DNSSettings.vue') },
    { path: '/zapret', component: () => import('./ZapretSettings.vue') },
    { path: '/routing', component: () => import('./RoutingSettings.vue') },
    { path: '/devices', component: () => import('./Devices.vue') },
    { path: '/settings', component: () => import('./PanelSettings.vue') },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

createApp(App).use(createPinia()).use(VueQueryPlugin).use(router).mount('#app')
