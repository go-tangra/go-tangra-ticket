// The ticket API through the gateway: the kit client bound to this module's base.
import { createApi, ApiError, csrfToken, describe, type Method, type RequestOptions } from '@freya/ui/api'
import { registerReasons } from '@freya/ui/forms'
import type { paths } from './schema.d'

export { ApiError, csrfToken, describe }
export type { Method, RequestOptions }

// Path names are checked against the OpenAPI contract at compile time.
export type ApiPath = keyof paths
export const BASE = '/api/ticket/v1'

// Ticket-specific refusal reasons (closed vocabulary, api/openapi/ticket.yaml).
registerReasons({
  invalid_status: 'That is not a ticket status.',
  invalid_assignee: 'That person cannot work tickets in this tenant.',
  ticket_not_found: 'The ticket no longer exists.',
  directory_unavailable: 'The user directory is unavailable; try again shortly.',
  comment_not_found: 'The comment no longer exists.',
  reply_unavailable: 'Replies need a requester address and a configured outbound mail relay.',
  delivery_failed: 'The reply was saved but could not be delivered by email.',
  invalid_rule: 'The rule is not valid.',
})

export const api = createApi({ base: BASE })
export const upload = api.upload
export const fileUrl = api.fileUrl
