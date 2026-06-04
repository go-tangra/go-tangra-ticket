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
    ],
  },
];

export default routes;
