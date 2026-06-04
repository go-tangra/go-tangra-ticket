import { defineStore } from 'pinia';

import {
  CommentService,
  TicketService,
  UserService,
  type AssignableUser,
  type ListTicketsParams,
  type ListTicketsResponse,
  type Ticket,
  type TicketComment,
  type TicketStatus,
} from '../api/services';

export const useTicketStore = defineStore('ticket-ticket', () => {
  async function listTickets(params?: ListTicketsParams): Promise<ListTicketsResponse> {
    return await TicketService.list(params);
  }

  async function getTicket(id: string): Promise<Ticket> {
    return (await TicketService.get(id)).ticket;
  }

  async function assignTicket(id: string, assigneeId: number): Promise<Ticket> {
    return (await TicketService.assign(id, assigneeId)).ticket;
  }

  async function updateStatus(id: string, status: TicketStatus): Promise<Ticket> {
    return (await TicketService.updateStatus(id, status)).ticket;
  }

  async function deleteTicket(id: string): Promise<void> {
    await TicketService.remove(id);
  }

  async function listComments(ticketId: string): Promise<TicketComment[]> {
    return (await CommentService.list(ticketId)).comments ?? [];
  }

  async function addComment(ticketId: string, body: string, internal: boolean): Promise<TicketComment> {
    return (await CommentService.create(ticketId, body, internal)).comment;
  }

  async function listAssignableUsers(): Promise<AssignableUser[]> {
    return (await UserService.listAssignable()).users ?? [];
  }

  function $reset() {}

  return {
    $reset,
    listTickets,
    getTicket,
    assignTicket,
    updateStatus,
    deleteTicket,
    listComments,
    addComment,
    listAssignableUsers,
  };
});
