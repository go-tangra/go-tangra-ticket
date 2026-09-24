import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, describe } from '@/api/client'
import type { Rule, RuleInput, RuleSample, RuleTestResult } from '@/api/types'

const bySortName = (a: Rule, b: Rule) => a.sort_order - b.sort_order || a.name.localeCompare(b.name)

export const useRules = defineStore('ticket-rules', () => {
  const items = ref<Rule[]>([])
  const loading = ref(false)
  const error = ref('')

  /** Loads every rule in evaluation order. */
  async function list(): Promise<void> {
    loading.value = true
    error.value = ''
    try {
      const res = await api<{ items: Rule[] }>('GET', 'rules')
      items.value = res.items ?? []
    } catch (e) {
      error.value = describe(e)
    } finally {
      loading.value = false
    }
  }

  function put(r: Rule): Rule {
    items.value = [...items.value.filter((x) => x.id !== r.id), r].sort(bySortName)
    return r
  }

  async function create(body: RuleInput): Promise<Rule> {
    return put(await api<Rule>('POST', 'rules', body))
  }

  async function update(id: string, body: RuleInput): Promise<Rule> {
    return put(await api<Rule>('PUT', 'rules/' + id, body))
  }

  /** Flips enabled (a rule update: the whole rule is sent back). */
  async function setEnabled(r: Rule, enabled: boolean): Promise<Rule> {
    return update(r.id, { name: r.name, enabled, sort_order: r.sort_order, match: r.match, conditions: r.conditions, expression: r.expression ?? '', actions: r.actions })
  }

  async function remove(id: string): Promise<void> {
    await api('DELETE', 'rules/' + id)
    items.value = items.value.filter((x) => x.id !== id)
  }

  /** Dry run: evaluates an unsaved rule against a sample message. */
  async function test(rule: RuleInput, sample: RuleSample): Promise<RuleTestResult> {
    return api<RuleTestResult>('POST', 'rules/test', { rule, sample })
  }

  return { items, loading, error, list, create, update, setEnabled, remove, test }
})
