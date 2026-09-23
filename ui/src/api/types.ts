// Domain types mirror the ticket OpenAPI responses (api/openapi/ticket.yaml)
// and the store models (internal/store/models.go). The raw HTML body is never
// returned (only has_html); message bodies come sanitised from /tickets/{id}/body.
// Response projections use optional (`?:`) fields; inputs/filters use explicit
// `T | undefined` to satisfy exactOptionalPropertyTypes.

export type TicketStatus = 'open' | 'in_progress' | 'pending' | 'resolved' | 'closed'
export type TicketPriority = 'low' | 'normal' | 'high' | 'urgent'
export type TicketSource = 'manual' | 'email'
export type TagKind = 'tag' | 'category'
export type HistoryField = 'status' | 'priority' | 'assignee'
export type ActorKind = 'agent' | 'rule' | 'inbound' | 'system'

export interface Tag {
  id: string
  name: string
  kind: TagKind
  color?: string
  description?: string
  created_at?: string
}

export interface Attachment {
  id: string
  comment_id?: string
  filename: string
  content_type: string
  size: number
  content_id?: string
  inline?: boolean
  created_at?: string
}

export interface Ticket {
  id: string
  tenant_id?: string
  subject: string
  description?: string
  status: TicketStatus
  priority: TicketPriority
  source: TicketSource
  requester_email?: string
  requester_name?: string
  recipient?: string
  mailbox_id?: string
  assignee_id?: string
  assignee_name?: string
  comment_count: number
  has_html?: boolean
  tags?: Tag[]
  attachments?: Attachment[]
  created_by?: string
  created_at: string
  updated_at: string
  resolved_at?: string
}

export interface TicketPage {
  items: Ticket[]
  total: number
}

export interface HistoryEntry {
  id: string
  field: HistoryField
  old_value: string
  new_value: string
  actor_kind: ActorKind
  actor_id?: string
  created_at: string
}

export interface AssignableUser {
  id: string
  name: string
  email?: string
}

/** GET /tickets query. assignee_id "none" selects unassigned tickets. */
export interface TicketFilter {
  status?: string | undefined
  priority?: string | undefined
  assignee_id?: string | undefined
  tag_id?: string | undefined
  query?: string | undefined
  page?: number | undefined
  page_size?: number | undefined
}

export interface TicketCreate {
  subject: string
  description?: string | undefined
  priority?: TicketPriority | undefined
  requester_email?: string | undefined
  requester_name?: string | undefined
  assignee_id?: string | undefined
}

export interface TicketUpdate {
  subject?: string | undefined
  description?: string | undefined
  priority?: TicketPriority | undefined
}

export type AuthorKind = 'agent' | 'requester' | 'system'
export type Delivery = 'none' | 'sent' | 'failed'

/** One conversation entry (oldest first). */
export interface Comment {
  id: string
  ticket_id: string
  body: string
  internal: boolean
  author_kind: AuthorKind
  author_id?: string
  author_name?: string
  author_email?: string
  message_id?: string
  delivery: Delivery
  created_at: string
}

/** GET /tickets/{id}/body: sanitised HTML (never raw) plus the text body. */
export interface MessageBody {
  html_sanitized: string
  text: string
}

/** An inbound support address routed to this tenant. */
export interface Mailbox {
  id: string
  address: string
  display_name: string
  active: boolean
  auto_ack: boolean
  auto_ack_template?: string
  created_at?: string
  updated_at?: string
}

export interface MailboxInput {
  address?: string | undefined
  display_name?: string | undefined
  active?: boolean | undefined
  auto_ack?: boolean | undefined
  auto_ack_template?: string | undefined
}

/** POST/PUT /tags (kind is fixed after creation). */
export interface TagInput {
  name?: string | undefined
  kind?: TagKind | undefined
  color?: string | undefined
  description?: string | undefined
}

export type RuleMatch = 'all' | 'any'
export type RuleActionType = 'tag' | 'assign' | 'status' | 'priority' | 'drop'

/** One rule condition: field / operator / value (always a string). */
export interface RuleCondition {
  field: string
  operator: string
  value: string
}

/** One rule action; only the fields of its type are meaningful. */
export interface RuleAction {
  type: RuleActionType
  tag_kind?: TagKind | undefined
  tag_names?: string[] | undefined
  assignee_id?: string | undefined
  status?: TicketStatus | undefined
  priority?: TicketPriority | undefined
}

/** An ordered CEL triage rule (version bumps on every update). */
export interface Rule {
  id: string
  name: string
  enabled: boolean
  sort_order: number
  match: RuleMatch
  conditions: RuleCondition[]
  expression?: string
  actions: RuleAction[]
  version: number
  created_at?: string
  updated_at?: string
}

/** POST/PUT /rules body (the whole rule). */
export interface RuleInput {
  name: string
  enabled?: boolean | undefined
  sort_order?: number | undefined
  match?: RuleMatch | undefined
  conditions?: RuleCondition[] | undefined
  expression?: string | undefined
  actions: RuleAction[]
}

/** The dry-run message of POST /rules/test. */
export interface RuleSample {
  subject?: string | undefined
  body?: string | undefined
  from?: string | undefined
  from_name?: string | undefined
  recipient?: string | undefined
  has_attachments?: boolean | undefined
  spam_score?: number | undefined
}

export interface RuleTestResult {
  matched: boolean
  actions: RuleAction[]
}

/** One UTC day bucket of a per-day series (YYYY-MM-DD). */
export interface DayCount {
  day: string
  count: number
}

/** Open work of one assignee. */
export interface AssigneeCount {
  assignee_id: string
  assignee_name?: string
  count: number
}

/** GET /stats?days=N: the tenant dashboard aggregates. */
export interface TicketStats {
  total: number
  by_status: Partial<Record<TicketStatus, number>>
  by_priority: Partial<Record<TicketPriority, number>>
  by_assignee: AssigneeCount[]
  unassigned_open: number
  created_per_day: DayCount[]
  resolved_per_day: DayCount[]
}

/** POST /backup/import summary. */
export interface BackupResult {
  tenant_id: string
  mode: 'skip' | 'overwrite'
  imported: Record<string, number>
  skipped: Record<string, number>
  deleted: number
}

/** A ticket event relayed by GET /stream (ids and metadata only). */
export interface TicketEvent {
  ticket_id: string
  subject?: string
  status?: TicketStatus
  priority?: TicketPriority
  assignee_id?: string
  source?: TicketSource
  actor_kind?: string
}
