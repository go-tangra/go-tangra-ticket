import { useAccessStore } from 'shell/vben/stores';

import {
  createTicketCommentServiceClient,
  createTicketRuleServiceClient,
  createTicketServiceClient,
  createTicketTagServiceClient,
  createTicketUserServiceClient,
} from '../generated/api/ticket/service/v1';

// The admin gateway mounts the module under this prefix; the generated
// client supplies the per-RPC path (e.g. "v1/tickets").
const MODULE_BASE_URL = '/admin/v1/modules/ticket';

type RequestType = { path: string; method: string; body: string | null };

async function handler(req: RequestType): Promise<unknown> {
  const accessStore = useAccessStore();
  const token = (accessStore as any).accessToken;

  const response = await fetch(`${MODULE_BASE_URL}/${req.path}`, {
    method: req.method,
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: req.body,
  });

  if (!response.ok) {
    let message = `HTTP error! status: ${response.status}`;
    try {
      const errorBody = await response.json();
      if (errorBody?.message) message = errorBody.message;
    } catch {
      // non-JSON error body
    }
    throw new Error(message);
  }

  const text = await response.text();
  return text ? JSON.parse(text) : {};
}

export const ticketService = createTicketServiceClient(handler);
export const commentService = createTicketCommentServiceClient(handler);
export const userService = createTicketUserServiceClient(handler);
export const tagService = createTicketTagServiceClient(handler);
export const ruleService = createTicketRuleServiceClient(handler);

// Re-export generated types for convenience.
export type {
  Ticket,
  TicketComment,
  TicketStatus,
  TicketPriority,
  TicketTag,
  TicketRule,
  RuleCondition,
  RuleAction,
  RuleInput,
  AssignableUser,
  ListTicketsRequest,
  ListTicketsResponse,
  ListCommentsResponse,
  ListAssignableUsersResponse,
  ListTagsResponse,
  ListRulesResponse,
} from '../generated/api/ticket/service/v1';
