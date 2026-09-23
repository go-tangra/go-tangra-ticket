import { z } from 'zod'
import { nonEmpty, optionalString } from '@freya/ui/forms'

export const STATUSES = ['open', 'in_progress', 'pending', 'resolved', 'closed'] as const
export const PRIORITIES = ['low', 'normal', 'high', 'urgent'] as const

/** Human labels for the lifecycle values. */
export const STATUS_LABELS: Record<(typeof STATUSES)[number], string> = {
  open: 'Open', in_progress: 'In progress', pending: 'Pending', resolved: 'Resolved', closed: 'Closed',
}
export const PRIORITY_LABELS: Record<(typeof PRIORITIES)[number], string> = {
  low: 'Low', normal: 'Normal', high: 'High', urgent: 'Urgent',
}

/** Badge colours (text always carries the value too). */
export const STATUS_COLORS = { open: 'info', in_progress: 'primary', pending: 'warning', resolved: 'success', closed: 'neutral' } as const
export const PRIORITY_COLORS = { low: 'neutral', normal: 'info', high: 'warning', urgent: 'error' } as const

/** A select that may be cleared: '' means "not set". */
const blankToUndefined = (v: unknown) => (v === '' || v === null ? undefined : v)

/** Subject: one header line (RFC 5322 limit), no line breaks. */
const subject = nonEmpty(998).refine((s) => !/[\r\n]/.test(s), { message: 'Must be a single line.' })

/** POST /tickets (manual ticket). */
export const ticketSchema = z.object({
  subject,
  description: optionalString(1_048_576),
  priority: z.preprocess(blankToUndefined, z.enum(PRIORITIES).optional()),
  requester_name: optionalString(200),
  requester_email: optionalString(320).pipe(z.email().optional()),
  assignee_id: optionalString(64),
})
export type TicketInput = z.output<typeof ticketSchema>

/** PUT /tickets/{id}: subject and description (priority saves from its select). */
export const ticketUpdateSchema = z.object({
  subject,
  description: optionalString(1_048_576),
})
export type TicketUpdateInput = z.output<typeof ticketUpdateSchema>

/** GET /tickets filter bar. */
export const ticketFilterSchema = z.object({
  query: z.string().trim().max(200).optional(),
  status: z.preprocess(blankToUndefined, z.enum(STATUSES).optional()),
  priority: z.preprocess(blankToUndefined, z.enum(PRIORITIES).optional()),
  assignee_id: z.preprocess(blankToUndefined, z.string().max(64).optional()),
  tag_id: z.preprocess(blankToUndefined, z.string().max(64).optional()),
})
export type TicketFilterInput = z.output<typeof ticketFilterSchema>
