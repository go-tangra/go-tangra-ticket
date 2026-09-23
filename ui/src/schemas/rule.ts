import { z } from 'zod'
import { nonEmpty, optionalString } from '@freya/ui/forms'
import { PRIORITIES, PRIORITY_LABELS, STATUSES, STATUS_LABELS } from './ticket'
import type { RuleAction, RuleActionType, RuleCondition, RuleInput } from '@/api/types'

// The rule-builder vocabulary. It MUST match the CEL engine
// (services/ticket/internal/rules/engine.go).

export type FieldType = 'text' | 'number' | 'bool'
export interface Option { title: string; value: string }

export const RULE_FIELDS: (Option & { type: FieldType })[] = [
  { value: 'subject', title: 'Subject', type: 'text' },
  { value: 'body', title: 'Body', type: 'text' },
  { value: 'from', title: 'Sender email', type: 'text' },
  { value: 'fromName', title: 'Sender name', type: 'text' },
  { value: 'fromDomain', title: 'Sender domain', type: 'text' },
  { value: 'recipient', title: 'Recipient', type: 'text' },
  { value: 'hasAttachments', title: 'Has attachments', type: 'bool' },
  { value: 'spamScore', title: 'Spam score', type: 'number' },
]

export const TEXT_OPERATORS: Option[] = [
  { value: 'contains', title: 'contains' },
  { value: 'not_contains', title: 'does not contain' },
  { value: 'equals', title: 'equals' },
  { value: 'not_equals', title: 'does not equal' },
  { value: 'starts_with', title: 'starts with' },
  { value: 'ends_with', title: 'ends with' },
  { value: 'matches', title: 'matches pattern (RE2)' },
]
export const NUMERIC_OPERATORS: Option[] = [
  { value: 'gt', title: 'greater than' },
  { value: 'gte', title: 'at least' },
  { value: 'lt', title: 'less than' },
  { value: 'lte', title: 'at most' },
  { value: 'eq', title: 'equals' },
  { value: 'neq', title: 'does not equal' },
]
export const BOOL_OPERATORS: Option[] = [
  { value: 'equals', title: 'is' },
  { value: 'not_equals', title: 'is not' },
]
export const BOOL_VALUES: Option[] = [
  { value: 'true', title: 'yes' },
  { value: 'false', title: 'no' },
]
export const MATCH_OPTIONS: Option[] = [
  { value: 'all', title: 'All conditions (AND)' },
  { value: 'any', title: 'Any condition (OR)' },
]
export const ACTION_TYPES: (Option & { value: RuleActionType })[] = [
  { value: 'tag', title: 'Add tags' },
  { value: 'assign', title: 'Assign to' },
  { value: 'status', title: 'Set status' },
  { value: 'priority', title: 'Set priority' },
  { value: 'drop', title: 'Drop the message' },
]
export const TAG_KIND_OPTIONS: Option[] = [
  { value: 'tag', title: 'Tag' },
  { value: 'category', title: 'Category' },
]

/** Warning shown next to a drop action. */
export const DROP_WARNING = 'Matching messages are discarded: no ticket is created and the sender is not told.'

export function fieldType(field: string): FieldType {
  return RULE_FIELDS.find((f) => f.value === field)?.type ?? 'text'
}

export function operatorsFor(field: string): Option[] {
  const t = fieldType(field)
  return t === 'number' ? NUMERIC_OPERATORS : t === 'bool' ? BOOL_OPERATORS : TEXT_OPERATORS
}

/** A fresh condition row for field (operator and value reset to fit its type). */
export function blankCondition(field = 'subject'): RuleCondition {
  const t = fieldType(field)
  return { field, operator: operatorsFor(field)[0]!.value, value: t === 'bool' ? 'true' : t === 'number' ? '5' : '' }
}

/** A fresh action row of type. */
export function blankAction(type: RuleActionType = 'tag'): RuleAction {
  switch (type) {
    case 'tag': return { type, tag_kind: 'tag', tag_names: [] }
    case 'assign': return { type, assignee_id: '' }
    case 'status': return { type, status: 'in_progress' }
    case 'priority': return { type, priority: 'high' }
    default: return { type: 'drop' }
  }
}

const allFields = RULE_FIELDS.map((f) => f.value)

export const conditionSchema = z.object({
  field: z.string().refine((f) => allFields.includes(f), { message: 'Choose a field.' }),
  operator: z.string(),
  value: z.string().max(1000),
}).superRefine((c, ctx) => {
  const t = fieldType(c.field)
  if (!operatorsFor(c.field).some((o) => o.value === c.operator)) ctx.addIssue({ code: 'custom', path: ['operator'], message: 'Choose an operator for this field.' })
  if (t === 'number' && (c.value.trim() === '' || !Number.isFinite(Number(c.value)))) ctx.addIssue({ code: 'custom', path: ['value'], message: 'Enter a number.' })
  if (t === 'bool' && !['true', 'false'].includes(c.value)) ctx.addIssue({ code: 'custom', path: ['value'], message: 'Choose yes or no.' })
  if (t === 'text' && c.value === '' && !['equals', 'not_equals'].includes(c.operator)) ctx.addIssue({ code: 'custom', path: ['value'], message: 'Enter a value.' })
})

export const actionSchema = z.object({
  type: z.enum(['tag', 'assign', 'status', 'priority', 'drop']),
  tag_kind: z.enum(['tag', 'category']).optional(),
  tag_names: z.array(z.string().trim().max(100)).max(20).optional(),
  assignee_id: z.string().max(64).optional(),
  status: z.enum(STATUSES).optional(),
  priority: z.enum(PRIORITIES).optional(),
}).superRefine((a, ctx) => {
  if (a.type === 'tag' && !(a.tag_names ?? []).some((n) => n.trim() !== '')) ctx.addIssue({ code: 'custom', path: ['tag_names'], message: 'Name at least one tag.' })
  if (a.type === 'assign' && !a.assignee_id) ctx.addIssue({ code: 'custom', path: ['assignee_id'], message: 'Choose an assignee.' })
  if (a.type === 'status' && !a.status) ctx.addIssue({ code: 'custom', path: ['status'], message: 'Choose a status.' })
  if (a.type === 'priority' && !a.priority) ctx.addIssue({ code: 'custom', path: ['priority'], message: 'Choose a priority.' })
})

/** POST/PUT /rules: conditions (or an advanced expression) and at least one action. */
export const ruleSchema = z.object({
  name: nonEmpty(200),
  enabled: z.boolean().default(true),
  sort_order: z.coerce.number().int().min(-1_000_000).max(1_000_000).default(0),
  match: z.enum(['all', 'any']).default('all'),
  conditions: z.array(conditionSchema).max(50).default([]),
  expression: optionalString(4096),
  actions: z.array(actionSchema).min(1, 'Add at least one action.').max(20),
}).superRefine((r, ctx) => {
  if (!r.expression && r.conditions.length === 0) ctx.addIssue({ code: 'custom', path: ['conditions'], message: 'Add a condition or an advanced expression.' })
})
export type RuleFormInput = z.input<typeof ruleSchema>

/** Only the fields of an action's own type (the API drops the rest anyway). */
export function cleanAction(a: RuleAction): RuleAction {
  switch (a.type) {
    case 'tag': return { type: 'tag', tag_kind: a.tag_kind ?? 'tag', tag_names: (a.tag_names ?? []).map((n) => n.trim()).filter(Boolean) }
    case 'assign': return { type: 'assign', assignee_id: a.assignee_id }
    case 'status': return { type: 'status', status: a.status }
    case 'priority': return { type: 'priority', priority: a.priority }
    default: return { type: 'drop' }
  }
}

/** The request body of a validated form. */
export function toRuleInput(v: z.output<typeof ruleSchema>): RuleInput {
  return {
    name: v.name, enabled: v.enabled, sort_order: v.sort_order, match: v.match,
    conditions: v.conditions.map((c) => ({ field: c.field, operator: c.operator, value: c.value })),
    expression: v.expression ?? '',
    actions: v.actions.map((a) => cleanAction(a as RuleAction)),
  }
}

/** Splits comma-separated tag names. */
export function splitNames(s: string): string[] {
  return s.split(',').map((n) => n.trim()).filter(Boolean)
}

const label = (list: Option[], v: string) => list.find((o) => o.value === v)?.title ?? v

/** "Subject contains “invoice”" */
export function conditionSummary(c: RuleCondition): string {
  const t = fieldType(c.field)
  const value = t === 'bool' ? label(BOOL_VALUES, c.value) : t === 'number' ? c.value : `“${c.value}”`
  return `${label(RULE_FIELDS, c.field)} ${label(operatorsFor(c.field), c.operator)} ${value}`
}

/** "Tag Billing, Late" / "Assign to Ada" / "Drop". */
export function actionSummary(a: RuleAction, userName: (id: string) => string = (id) => id): string {
  switch (a.type) {
    case 'tag': return `${a.tag_kind === 'category' ? 'Category' : 'Tag'} ${(a.tag_names ?? []).join(', ')}`
    case 'assign': return `Assign to ${a.assignee_id ? userName(a.assignee_id) : '—'}`
    case 'status': return `Status ${a.status ? STATUS_LABELS[a.status] : '—'}`
    case 'priority': return `Priority ${a.priority ? PRIORITY_LABELS[a.priority] : '—'}`
    default: return 'Drop'
  }
}

/** The "when" side of a rule list row. */
export function ruleConditionSummary(r: { match: string; conditions: RuleCondition[]; expression?: string | undefined }): string {
  if (r.expression) return `Expression: ${r.expression}`
  return r.conditions.map(conditionSummary).join(r.match === 'any' ? ' OR ' : ' AND ')
}
