import { defineStore } from 'pinia';

import {
  commentService,
  ticketService,
  userService,
  type AssignableUser,
  type ListTicketsRequest,
  type ListTicketsResponse,
  type Ticket,
  type TicketComment,
  type TicketStatus,
} from '../api/client';

export const useTicketStore = defineStore('ticket-ticket', () => {
  async function listTickets(params?: ListTicketsRequest): Promise<ListTicketsResponse> {
    return await ticketService.ListTickets(params ?? {});
  }

  async function getTicket(id: string): Promise<Ticket | undefined> {
    return (await ticketService.GetTicket({ id })).ticket;
  }

  async function assignTicket(id: string, assigneeId: number): Promise<Ticket | undefined> {
    return (await ticketService.AssignTicket({ id, assigneeId })).ticket;
  }

  async function updateStatus(id: string, status: TicketStatus): Promise<Ticket | undefined> {
    return (await ticketService.UpdateTicketStatus({ id, status })).ticket;
  }

  async function deleteTicket(id: string): Promise<void> {
    await ticketService.DeleteTicket({ id });
  }

  async function listComments(ticketId: string): Promise<TicketComment[]> {
    return (await commentService.ListComments({ ticketId })).comments ?? [];
  }

  async function addComment(
    ticketId: string,
    body: string,
    internal: boolean,
  ): Promise<TicketComment | undefined> {
    return (await commentService.CreateComment({ ticketId, body, internal })).comment;
  }

  async function replyTicket(
    ticketId: string,
    body: string,
  ): Promise<TicketComment | undefined> {
    return (await commentService.ReplyTicket({ ticketId, body })).comment;
  }

  async function listAssignableUsers(): Promise<AssignableUser[]> {
    return (await userService.ListAssignableUsers({})).users ?? [];
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
    replyTicket,
    listAssignableUsers,
  };
});
