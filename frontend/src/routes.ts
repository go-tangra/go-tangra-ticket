import type { RouteRecordRaw } from 'vue-router';

const routes: RouteRecordRaw[] = [
  {
    path: '/ticket',
    name: 'Ticket',
    component: () => import('shell/app-layout'),
    redirect: '/ticket/tickets',
    meta: {
      order: 2070,
      icon: 'lucide:ticket',
      title: 'ticket.menu.ticket',
      keepAlive: true,
      authority: ['platform:admin', 'tenant:manager'],
    },
    children: [
      {
        path: 'tickets',
        name: 'TicketList',
        meta: {
          icon: 'lucide:list',
          title: 'ticket.menu.tickets',
          authority: ['platform:admin', 'tenant:manager'],
        },
        component: () => import('./views/tickets/index.vue'),
      },
      {
        path: 'tags',
        name: 'TicketTags',
        meta: {
          icon: 'lucide:tags',
          title: 'ticket.menu.tags',
          authority: ['platform:admin', 'tenant:manager'],
        },
        component: () => import('./views/tags/index.vue'),
      },
      {
        path: 'rules',
        name: 'TicketRules',
        meta: {
          icon: 'lucide:filter',
          title: 'ticket.menu.rules',
          authority: ['platform:admin', 'tenant:manager'],
        },
        component: () => import('./views/rules/index.vue'),
      },
    ],
  },
];

export default routes;
