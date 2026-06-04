import type { TangraModule } from './sdk';

import routes from './routes';
import { useTicketStore } from './stores/ticket.state';
import enUS from './locales/en-US.json';

const ticketModule: TangraModule = {
  id: 'ticket',
  version: '1.0.0',
  routes,
  stores: {
    'ticket-ticket': useTicketStore,
  },
  locales: {
    'en-US': enUS,
  },
};

export default ticketModule;
