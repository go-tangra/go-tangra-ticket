import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { abilitiesPlugin } from '@casl/vue'
import { createMongoAbility } from '@casl/ability'
import { useConfirm } from '@freya/ui'
import Rules from '@/views/rules/index.vue'
import RuleDrawer from '@/views/rules/drawer.vue'
import {
  actionSummary, blankAction, blankCondition, cleanAction, conditionSummary, fieldType, operatorsFor, ruleConditionSummary, ruleSchema, splitNames, toRuleInput,
  NUMERIC_OPERATORS, TEXT_OPERATORS,
} from '@/schemas'
import { useRules } from '@/stores/rules'
import type { Rule } from '@/api/types'

type Call = { url: string; method: string; body: unknown }
function fetchMock(handler: (url: string, method: string, body: unknown) => { status?: number; body?: unknown }) {
  const calls: Call[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    const method = init.method ?? 'GET'
    const body = init.body ? JSON.parse(String(init.body)) : undefined
    calls.push({ url, method, body })
    const res = handler(url, method, body)
    const status = res.status ?? 200
    return new Response(status === 204 ? null : JSON.stringify(res.body ?? {}), { status, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}
const withAbility = (rules: { action: string; subject: string }[]) => ({ plugins: [[abilitiesPlugin, createMongoAbility(rules), { useGlobalProperties: true }]] as never })
const ADMIN = [{ action: 'manage', subject: 'TicketRule' }]

const invoices: Rule = {
  id: 'r1', name: 'Invoices', enabled: true, sort_order: 10, match: 'all', version: 1,
  conditions: [{ field: 'subject', operator: 'contains', value: 'invoice' }, { field: 'spamScore', operator: 'lt', value: '5' }],
  actions: [{ type: 'tag', tag_kind: 'category', tag_names: ['Billing'] }, { type: 'assign', assignee_id: 'u1' }],
}
const spam: Rule = { id: 'r2', name: 'Spam', enabled: false, sort_order: 1, match: 'all', version: 3, conditions: [], expression: 'spamScore > 5.0', actions: [{ type: 'drop' }] }

describe('rule vocabulary and schema', () => {
  it('operators and blank rows follow the field type', () => {
    expect(fieldType('spamScore')).toBe('number')
    expect(fieldType('hasAttachments')).toBe('bool')
    expect(fieldType('fromDomain')).toBe('text')
    expect(operatorsFor('spamScore')).toBe(NUMERIC_OPERATORS)
    expect(operatorsFor('subject')).toBe(TEXT_OPERATORS)
    expect(blankCondition('hasAttachments')).toEqual({ field: 'hasAttachments', operator: 'equals', value: 'true' })
    expect(blankCondition('spamScore')).toEqual({ field: 'spamScore', operator: 'gt', value: '5' })
    expect(blankAction('status')).toEqual({ type: 'status', status: 'in_progress' })
    expect(blankAction('drop')).toEqual({ type: 'drop' })
    expect(splitNames(' a, ,b ,')).toEqual(['a', 'b'])
  })

  it('validates typed conditions, actions and the conditions-or-expression rule', () => {
    const ok = { name: ' R ', conditions: [{ field: 'subject', operator: 'contains', value: 'x' }], actions: [{ type: 'drop' }] }
    const parsed = ruleSchema.parse(ok)
    expect(parsed).toMatchObject({ name: 'R', enabled: true, sort_order: 0, match: 'all' })
    const bad = [
      { ...ok, name: '' },
      { ...ok, actions: [] },
      { ...ok, conditions: [] },
      { ...ok, conditions: [{ field: 'spamScore', operator: 'gt', value: 'abc' }] },
      { ...ok, conditions: [{ field: 'spamScore', operator: 'contains', value: '5' }] },
      { ...ok, conditions: [{ field: 'hasAttachments', operator: 'equals', value: 'maybe' }] },
      { ...ok, conditions: [{ field: 'subject', operator: 'contains', value: '' }] },
      { ...ok, conditions: [{ field: 'bogus', operator: 'contains', value: 'x' }] },
      { ...ok, actions: [{ type: 'tag', tag_names: [' '] }] },
      { ...ok, actions: [{ type: 'assign', assignee_id: '' }] },
      { ...ok, actions: [{ type: 'status' }] },
      { ...ok, actions: [{ type: 'priority' }] },
      { ...ok, expression: 'x'.repeat(4097) },
    ]
    for (const b of bad) expect(ruleSchema.safeParse(b).success).toBe(false)
    expect(ruleSchema.safeParse({ ...ok, conditions: [], expression: 'spamScore > 5.0' }).success).toBe(true)
    expect(ruleSchema.safeParse({ ...ok, conditions: [{ field: 'from', operator: 'equals', value: '' }] }).success).toBe(true)
  })

  it('request bodies carry only the fields of each action type', () => {
    expect(cleanAction({ type: 'assign', assignee_id: 'u1', status: 'open', tag_names: ['x'] })).toEqual({ type: 'assign', assignee_id: 'u1' })
    expect(cleanAction({ type: 'tag', tag_names: [' a ', ''] })).toEqual({ type: 'tag', tag_kind: 'tag', tag_names: ['a'] })
    const body = toRuleInput(ruleSchema.parse({ name: 'R', expression: 'true', actions: [{ type: 'priority', priority: 'high', assignee_id: 'x' }] }))
    expect(body).toEqual({ name: 'R', enabled: true, sort_order: 0, match: 'all', conditions: [], expression: 'true', actions: [{ type: 'priority', priority: 'high' }] })
  })

  it('summaries read as sentences', () => {
    expect(conditionSummary({ field: 'subject', operator: 'contains', value: 'invoice' })).toBe('Subject contains “invoice”')
    expect(conditionSummary({ field: 'hasAttachments', operator: 'equals', value: 'false' })).toBe('Has attachments is no')
    expect(conditionSummary({ field: 'spamScore', operator: 'gte', value: '7' })).toBe('Spam score at least 7')
    expect(ruleConditionSummary(invoices)).toBe('Subject contains “invoice” AND Spam score less than 5')
    expect(ruleConditionSummary({ ...invoices, match: 'any' })).toContain(' OR ')
    expect(ruleConditionSummary(spam)).toBe('Expression: spamScore > 5.0')
    expect(actionSummary(invoices.actions[0]!)).toBe('Category Billing')
    expect(actionSummary(invoices.actions[1]!, () => 'Ada')).toBe('Assign to Ada')
    expect(actionSummary({ type: 'status', status: 'pending' })).toBe('Status Pending')
    expect(actionSummary({ type: 'priority', priority: 'urgent' })).toBe('Priority Urgent')
    expect(actionSummary({ type: 'drop' })).toBe('Drop')
  })
})

function api(state: { items: Rule[] }) {
  return (url: string, method: string, body: unknown): { status?: number; body?: unknown } => {
    const path = url.replace(/^\/api\/ticket\/v1\//, '').split('?')[0]!
    if (path === 'assignable-users') return { body: { items: [{ id: 'u1', name: 'Ada' }] } }
    if (path === 'rules' && method === 'GET') return { body: { items: state.items } }
    if (path === 'rules/test') {
      const { rule, sample } = body as { rule: { actions: unknown[]; expression: string }; sample: { subject?: string } }
      if (rule.expression === 'nope(') return { status: 422, body: { reason: 'invalid_rule', detail: { field: 'expression', message: 'Syntax error: mismatched input' } } }
      const matched = (sample.subject ?? '').toLowerCase().includes('invoice')
      return { body: { matched, actions: matched ? rule.actions : [] } }
    }
    if (path === 'rules' && method === 'POST') {
      const b = body as Rule
      if (b.expression === 'subject') return { status: 422, body: { reason: 'invalid_rule', detail: { field: 'expression', message: 'must evaluate to true or false, not string' } } }
      const r = { ...b, id: 'r9', version: 1 } as Rule
      state.items = [...state.items, r]
      return { status: 201, body: r }
    }
    if (path.startsWith('rules/') && method === 'PUT') {
      const cur = state.items.find((r) => path.endsWith(r.id))!
      return { body: { ...cur, ...(body as object), version: cur.version + 1 } }
    }
    if (path.startsWith('rules/') && method === 'DELETE') return { status: 204 }
    return { status: 404, body: { reason: 'not_found' } }
  }
}

const panel = () => document.body.querySelector('[data-test="rule-drawer"]')!
function input(sel: string, value: string): void {
  const el = panel().querySelector<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>(sel)!
  el.value = value
  el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? 'change' : 'input'))
}
const click = (test: string) => panel().querySelector<HTMLButtonElement>(`[data-test="${test}"]`)!.click()

describe('rules', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.cookie = '__Host-csrf=tok; Secure; Path=/'
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it('store: list, create keeps sort order, toggle sends the whole rule, delete, dry run', async () => {
    const calls = fetchMock(api({ items: [spam, invoices] }))
    const s = useRules()
    await s.list()
    await s.create({ name: 'Early', sort_order: 0, expression: 'true', actions: [{ type: 'drop' }] })
    expect(s.items.map((r) => r.name)).toEqual(['Early', 'Spam', 'Invoices'])
    const up = await s.setEnabled(spam, true)
    expect(up.enabled).toBe(true)
    expect(calls.at(-1)).toMatchObject({ method: 'PUT', url: '/api/ticket/v1/rules/r2', body: { name: 'Spam', enabled: true, expression: 'spamScore > 5.0', actions: [{ type: 'drop' }] } })
    await s.remove('r1')
    expect(s.items.some((r) => r.id === 'r1')).toBe(false)
    const res = await s.test({ name: 'x', actions: [{ type: 'drop' }] }, { subject: 'Invoice' })
    expect(res.matched).toBe(true)
  })

  it('list: summaries, enable toggle, delete asks first; no writes without rules:manage', async () => {
    const calls = fetchMock(api({ items: [spam, invoices] }))
    const w = mount(Rules, { global: withAbility(ADMIN), attachTo: document.body })
    await flushPromises()
    const row = w.find('[data-test="rule-row-r1"]').text()
    expect(row).toContain('Invoices')
    expect(row).toContain('Category Billing')
    expect(row).toContain('Assign to Ada')
    expect(w.find('[data-test="rule-row-r2"]').text()).toContain('Drop')
    expect(w.find('[style]').exists()).toBe(false)

    const toggle = w.find<HTMLInputElement>('[data-test="rule-toggle-r2"] input, input[data-field="rule-enabled-r2"], #rule-enabled-r2')
    toggle.element.checked = true
    await toggle.trigger('change')
    await flushPromises()
    expect(calls.at(-1)).toMatchObject({ method: 'PUT', url: '/api/ticket/v1/rules/r2', body: { enabled: true } })

    const confirm = useConfirm()
    await w.find('[data-test="rule-delete-r1"]').trigger('click')
    await flushPromises()
    confirm.answer(true)
    await flushPromises()
    expect(calls.at(-1)).toMatchObject({ method: 'DELETE', url: '/api/ticket/v1/rules/r1' })
    w.unmount()

    fetchMock(api({ items: [invoices] }))
    const ro = mount(Rules, { global: withAbility([]), attachTo: document.body })
    await flushPromises()
    expect(ro.find('[data-test="rule-new"]').exists()).toBe(false)
    expect(ro.find('[data-test="rule-edit-r1"]').exists()).toBe(false)
    ro.unmount()
  })

  it('builder: typed rows, drop warning, client validation, save, server invalid_rule shown', async () => {
    const calls = fetchMock(api({ items: [] }))
    const w = mount(RuleDrawer, { props: { modelValue: true, rule: null }, global: withAbility(ADMIN), attachTo: document.body })
    await flushPromises()
    // empty name and tag names: refused client-side, nothing sent
    click('rule-save')
    await flushPromises()
    expect(panel().querySelector('[data-test="rule-issues"]')!.textContent).toContain('name')
    expect(calls.some((c) => c.method === 'POST')).toBe(false)

    input('#rule-name', 'Invoices')
    input('#cond-value-0', 'invoice')
    input('#cond-field-0', 'spamScore')
    await flushPromises()
    expect(panel().querySelector<HTMLSelectElement>('#cond-op-0')!.value).toBe('gt')
    input('#cond-field-0', 'subject')
    await flushPromises()
    input('#cond-value-0', 'invoice')
    click('rule-condition-add')
    await flushPromises()
    input('#cond-field-1', 'hasAttachments')
    await flushPromises()
    expect(panel().querySelector<HTMLSelectElement>('#cond-value-1')!.value).toBe('true')
    input('#act-kind-0', 'category')
    input('#act-names-0', 'Billing, Finance')
    click('rule-action-add')
    await flushPromises()
    input('#act-type-1', 'drop')
    await flushPromises()
    expect(panel().querySelector('[data-test="rule-drop-warning"]')!.textContent).toContain('no ticket is created')
    input('#act-type-1', 'assign')
    await flushPromises()
    input('#act-assignee-1', 'u1')
    await flushPromises()
    click('rule-save')
    await flushPromises()
    const post = calls.find((c) => c.method === 'POST' && c.url.endsWith('/rules'))
    expect(post?.body).toEqual({
      name: 'Invoices', enabled: true, sort_order: 10, match: 'all', expression: '',
      conditions: [{ field: 'subject', operator: 'contains', value: 'invoice' }, { field: 'hasAttachments', operator: 'equals', value: 'true' }],
      actions: [{ type: 'tag', tag_kind: 'category', tag_names: ['Billing', 'Finance'] }, { type: 'assign', assignee_id: 'u1' }],
    })
    expect(w.emitted('saved')).toBeTruthy()
    expect(w.emitted('update:modelValue')?.at(-1)).toEqual([false])
    w.unmount()

    // advanced expression refused by the server: the compiler's reason is shown
    const w2 = mount(RuleDrawer, { props: { modelValue: true, rule: null }, global: withAbility(ADMIN), attachTo: document.body })
    await flushPromises()
    input('#rule-name', 'Bad')
    click('rule-advanced')
    await flushPromises()
    input('#rule-expression', 'subject')
    input('#act-type-0', 'drop')
    await flushPromises()
    click('rule-save')
    await flushPromises()
    expect(panel().querySelector('[data-test="rule-issues"]')!.textContent).toContain('expression: must evaluate to true or false')
    expect(w2.emitted('saved')).toBeFalsy()
    w2.unmount()
  })

  it('builder: edit an existing rule and dry-run it', async () => {
    const calls = fetchMock(api({ items: [invoices] }))
    await useRules().list()
    const w = mount(RuleDrawer, { props: { modelValue: true, rule: invoices }, global: withAbility(ADMIN), attachTo: document.body })
    await flushPromises()
    expect(panel().querySelector<HTMLInputElement>('#rule-name')!.value).toBe('Invoices')
    expect(panel().querySelector<HTMLInputElement>('#act-names-0')!.value).toBe('Billing')

    input('#sample-subject', 'Your invoice 42')
    click('rule-test-run')
    await flushPromises()
    const test = calls.find((c) => c.url.endsWith('/rules/test'))!
    expect(test.body).toMatchObject({ rule: { name: 'Invoices' }, sample: { subject: 'Your invoice 42', spam_score: 0, has_attachments: false } })
    expect(panel().querySelector('[data-test="rule-test-result"]')!.textContent).toBe('Matches')
    expect(panel().textContent).toContain('Category Billing')

    input('#sample-subject', 'hello')
    click('rule-test-run')
    await flushPromises()
    expect(panel().querySelector('[data-test="rule-test-result"]')!.textContent).toBe('No match')

    click('rule-advanced')
    await flushPromises()
    input('#rule-expression', 'nope(')
    click('rule-test-run')
    await flushPromises()
    expect(panel().querySelector('[data-test="rule-test-error"]')!.textContent).toContain('expression: Syntax error')

    click('rule-advanced')
    await flushPromises()
    click('rule-condition-remove-1')
    click('rule-action-remove-1')
    await flushPromises()
    click('rule-save')
    await flushPromises()
    expect(calls.at(-1)).toMatchObject({ method: 'PUT', url: '/api/ticket/v1/rules/r1', body: { conditions: [{ field: 'subject' }], actions: [{ type: 'tag' }] } })
    w.unmount()
  })
})
