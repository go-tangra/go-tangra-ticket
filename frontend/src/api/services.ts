import { ticketApi } from './client';

// Enums are transcoded as their proto names by the admin gateway.
export type TicketStatus =
  | 'TICKET_STATUS_OPEN'
  | 'TICKET_STATUS_IN_PROGRESS'
  | 'TICKET_STATUS_PENDING'
  | 'TICKET_STATUS_RESOLVED'
  | 'TICKET_STATUS_CLOSED';

export type TicketPriority =
  | 'TICKET_PRIORITY_LOW'
  | 'TICKET_PRIORITY_NORMAL'
  | 'TICKET_PRIORITY_HIGH'
  | 'TICKET_PRIORITY_URGENT';

export interface Ticket {
  id: string;
  tenantId?: number;
  externalId?: string;
  subject: string;
  description?: string;
  status: TicketStatus;
  priority: TicketPriority;
  source?: string;
  requesterEmail?: string;
  requesterName?: string;
  recipient?: string;
  assigneeId?: number;
  assigneeName?: string;
  commentCount?: number;
  createBy?: number;
  createTime?: string;
  updateTime?: string;
}

export interface TicketComment {
  id: string;
  ticketId: string;
  body: string;
  internal?: boolean;
  authorId?: number;
  authorName?: string;
  authorEmail?: string;
  createTime?: string;
}

export interface AssignableUser {
  id: number;
  username: string;
  name?: string;
  email?: string;
}

export interface ListTicketsParams {
  page?: number;
  pageSize?: number;
  status?: TicketStatus;
  priority?: TicketPriority;
  assigneeId?: number;
  query?: string;
}

export interface ListTicketsResponse {
  tickets: Ticket[];
  total: number;
}

function qs(params?: Record<string, unknown>): string {
  if (!params) return '';
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== '') sp.set(k, String(v));
  }
  const s = sp.toString();
  return s ? `?${s}` : '';
}

export const TicketService = {
  list: (params?: ListTicketsParams) =>
    ticketApi.get<ListTicketsResponse>(`/tickets${qs(params as Record<string, unknown>)}`),
  get: (id: string) => ticketApi.get<{ ticket: Ticket }>(`/tickets/${id}`),
  create: (data: Partial<Ticket>) => ticketApi.post<{ ticket: Ticket }>('/tickets', data),
  update: (id: string, data: Partial<Ticket>) =>
    ticketApi.put<{ ticket: Ticket }>(`/tickets/${id}`, data),
  remove: (id: string) => ticketApi.delete<void>(`/tickets/${id}`),
  assign: (id: string, assigneeId: number) =>
    ticketApi.post<{ ticket: Ticket }>(`/tickets/${id}/assign`, { assigneeId }),
  updateStatus: (id: string, status: TicketStatus) =>
    ticketApi.post<{ ticket: Ticket }>(`/tickets/${id}/status`, { status }),
};

export const CommentService = {
  list: (ticketId: string) =>
    ticketApi.get<{ comments: TicketComment[]; total: number }>(`/tickets/${ticketId}/comments`),
  create: (ticketId: string, body: string, internal: boolean) =>
    ticketApi.post<{ comment: TicketComment }>(`/tickets/${ticketId}/comments`, { ticketId, body, internal }),
  remove: (id: string) => ticketApi.delete<void>(`/comments/${id}`),
};

export const UserService = {
  listAssignable: () => ticketApi.get<{ users: AssignableUser[] }>('/assignable-users'),
};
