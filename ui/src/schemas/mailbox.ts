import { z } from 'zod'
import { email, optionalString } from '@go-tangra/ui/forms'

/** POST/PUT /mailboxes: an inbound support address routed to this tenant. */
export const mailboxSchema = z.object({
  address: email,
  display_name: optionalString(200),
  active: z.boolean().default(true),
  auto_ack: z.boolean().default(false),
  auto_ack_template: optionalString(8192),
})
export type MailboxFormInput = z.output<typeof mailboxSchema>

/** Help text for the acknowledgement template. */
export const AUTO_ACK_HINT = '{{name}} is replaced with the requester name; the ticket reference line is always appended.'
