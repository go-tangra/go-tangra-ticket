import type { RouteRecordRaw } from 'vue-router'
import '@/main.css'

// Routes mounted by the platform shell under their own error boundary.
export const routes: RouteRecordRaw[] = [
  { path: '/ticket', name: 'ticket-tickets', component: () => import('@/views/tickets/index.vue'), meta: { module: 'ticket' } },
  { path: '/ticket/dashboard', name: 'ticket-dashboard', component: () => import('@/views/dashboard/index.vue'), meta: { module: 'ticket' } },
  { path: '/ticket/rules', name: 'ticket-rules', component: () => import('@/views/rules/index.vue'), meta: { module: 'ticket' } },
  { path: '/ticket/tags', name: 'ticket-tags', component: () => import('@/views/tags/index.vue'), meta: { module: 'ticket' } },
  { path: '/ticket/mailboxes', name: 'ticket-mailboxes', component: () => import('@/views/mailboxes/index.vue'), meta: { module: 'ticket' } },
]
export default routes
