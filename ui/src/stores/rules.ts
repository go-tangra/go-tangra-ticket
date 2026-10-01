import { defineStore } from 'pinia'
import { api } from '@/api/client'
import type { Page, Rule, RuleInput, RuleSample, RuleTestResult } from '@/api/types'
import { pagedList } from './paged'

/** Server-side sort fields of the rule list (evaluation order by default). */
export const RULE_SORTS = ['sort_order', 'name'] as const

export const useRules = defineStore('ticket-rules', () => {
  const page = pagedList<Rule>('rules')
  const { items, total, reload } = page

  async function create(body: RuleInput): Promise<Rule> {
    const r = await api<Rule>('POST', 'rules', body)
    void reload()
    return r
  }

  async function update(id: string, body: RuleInput): Promise<Rule> {
    const r = await api<Rule>('PUT', 'rules/' + id, body)
    items.value = items.value.map((x) => (x.id === id ? r : x))
    void reload()
    return r
  }

  /** Flips enabled (a rule update: the whole rule is sent back). */
  async function setEnabled(r: Rule, enabled: boolean): Promise<Rule> {
    return update(r.id, { name: r.name, enabled, sort_order: r.sort_order, match: r.match, conditions: r.conditions, expression: r.expression ?? '', actions: r.actions })
  }

  async function remove(id: string): Promise<void> {
    await api('DELETE', 'rules/' + id)
    items.value = items.value.filter((x) => x.id !== id)
    total.value = Math.max(0, total.value - 1)
    void reload()
  }

  /** The sort order after the last rule (10 for none), from the whole list, not just the loaded page. */
  async function nextSortOrder(): Promise<number> {
    const res = await api<Page<Rule>>('GET', 'rules', undefined, { query: { sort: 'sort_order', order: 'desc', page_size: 1 } })
    const top = res.items?.[0]
    return top ? top.sort_order + 10 : 10
  }

  /** Dry run: evaluates an unsaved rule against a sample message. */
  async function test(rule: RuleInput, sample: RuleSample): Promise<RuleTestResult> {
    return api<RuleTestResult>('POST', 'rules/test', { rule, sample })
  }

  return { ...page, create, update, setEnabled, remove, test, nextSortOrder }
})
