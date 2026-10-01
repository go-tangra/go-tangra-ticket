<script setup lang="ts">
// Triage rules in evaluation order: each row summarises its conditions (or
// expression) and actions, with an enable toggle; create/edit in the builder
// drawer; delete asks first.
import { computed, onMounted, ref, watch } from 'vue'
import { useAbility } from '@casl/vue'
import { UiPage, UiAlert, UiCard, UiButton, UiBadge, UiDataTable, UiSwitch, useConfirm, useListQuery, useToast, type Column } from '@go-tangra/ui'
import { RULE_SORTS, useRules } from '@/stores/rules'
import { useTickets } from '@/stores/tickets'
import { actionSummary, ruleConditionSummary } from '@/schemas'
import type { Rule } from '@/api/types'
import { ruleErrorText } from './errors'
import RuleDrawer from './drawer.vue'

const store = useRules()
const tickets = useTickets()
const ability = useAbility()
const confirm = useConfirm()
const toast = useToast()
const canManage = computed(() => ability.can('manage', 'TicketRule'))
const error = ref('')

// Server-paged; evaluation order (sort order) unless sorted by name.
const lq = useListQuery('rules', { sortable: [...RULE_SORTS], defaultSort: { key: 'sort_order', dir: 'asc' } })
async function load(): Promise<void> {
  const page = await store.list(lq.query.value)
  if (page !== null) lq.clampTo(page)
}
watch(lq.query, () => void load())

onMounted(() => {
  void load()
  void tickets.assignableUsers()
})
const userName = (id: string) => tickets.users.find((u) => u.id === id)?.name || id

const drawer = ref(false)
const editing = ref<Rule | null>(null)
function newRule(): void {
  editing.value = null
  drawer.value = true
}
function edit(r: Rule): void {
  editing.value = r
  drawer.value = true
}

async function toggle(r: Rule, enabled: unknown): Promise<void> {
  error.value = ''
  try {
    await store.setEnabled(r, Boolean(enabled))
  } catch (e) {
    error.value = ruleErrorText(e)
  }
}

async function remove(r: Rule): Promise<void> {
  if (!(await confirm.ask({ title: `Delete rule ${r.name}?`, text: 'New mail is no longer evaluated against it.', danger: true, confirmLabel: 'Delete' }))) return
  error.value = ''
  try {
    await store.remove(r.id)
    toast.show({ kind: 'success', title: 'Rule deleted' })
  } catch (e) {
    error.value = ruleErrorText(e)
  }
}

type Row = Rule & Record<string, unknown>
const columns: Column<Row>[] = [
  { key: 'sort_order', label: 'Order', width: 'sm', sortable: true },
  { key: 'name', label: 'Name', sortable: true },
  { key: 'conditions', label: 'When', hideOnStack: true, format: (r) => ruleConditionSummary(r) },
  { key: 'actions', label: 'Then', format: (r) => r.actions.map((a) => actionSummary(a, userName)).join('; ') },
  { key: 'enabled', label: 'Enabled', width: 'sm' },
]
const rows = computed(() => store.items as Row[])
</script>

<template>
  <UiPage title="Rules">
    <template #actions>
      <UiButton v-if="canManage" icon="mdi-plus" data-test="rule-new" @click="newRule">New rule</UiButton>
      <UiButton variant="text" icon="mdi-refresh" icon-only label="Refresh" @click="load()" />
    </template>
    <UiAlert v-if="error" kind="error" class="mb-3" data-test="rule-error">{{ error }}</UiAlert>
    <UiAlert v-if="store.error" kind="error" class="mb-3">{{ store.error }}</UiAlert>
    <UiCard :padded="false">
      <UiDataTable :items="rows" :columns="columns" :loading="store.loading" :total="store.total" :page="lq.page.value" :page-size="lq.pageSize.value" :sort="lq.sort.value" row-key="id" caption="Triage rules in evaluation order" empty-title="No rules" empty-text="Rules tag, assign, prioritise or drop new inbound mail." :row-attrs="(r) => ({ 'data-test': 'rule-row-' + r.id })" data-test="rules-table" @update:page="lq.setPage" @update:page-size="lq.setPageSize" @update:sort="lq.setSort">
        <template #cell-actions="{ row }">
          <span class="flex flex-wrap gap-1">
            <UiBadge v-for="(a, i) in row.actions" :key="i" size="xs" :color="a.type === 'drop' ? 'error' : 'neutral'">{{ actionSummary(a, userName) }}</UiBadge>
          </span>
        </template>
        <template #cell-enabled="{ row }">
          <UiSwitch :id="'rule-enabled-' + row.id" :model-value="row.enabled" :label="row.enabled ? 'On' : 'Off'" :disabled="!canManage" :data-test="'rule-toggle-' + row.id" @update:model-value="toggle(row, $event)" />
        </template>
        <template v-if="canManage" #actions="{ row }">
          <UiButton size="xs" variant="text" icon="mdi-pencil-outline" icon-only label="Edit rule" :data-test="'rule-edit-' + row.id" @click="edit(row)" />
          <UiButton size="xs" variant="text" color="error" icon="mdi-delete-outline" icon-only label="Delete rule" :data-test="'rule-delete-' + row.id" @click="remove(row)" />
        </template>
      </UiDataTable>
    </UiCard>

    <RuleDrawer v-model="drawer" :rule="editing" />
  </UiPage>
</template>
