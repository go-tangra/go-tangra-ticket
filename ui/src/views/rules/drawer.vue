<script setup lang="ts">
// Rule builder drawer: name / enabled / sort order / match, typed condition
// rows (the operator and value inputs follow the field's type), action rows
// (tag, assign, status, priority, drop — with a warning), an advanced CEL
// expression that overrides the conditions, and a "Test" panel that dry-runs
// the unsaved rule against a sample message. Validation runs client-side with
// the rule schema; the server's invalid_rule reason (e.g. a compiler message)
// is shown verbatim.
import { computed, reactive, ref, watch } from 'vue'
import { UiDrawer, UiAlert, UiButton, UiBadge, UiInput, UiNumberInput, UiSection, UiSelect, UiSwitch, UiTextarea } from '@go-tangra/ui'
import {
  ACTION_TYPES, BOOL_VALUES, DROP_WARNING, MATCH_OPTIONS, PRIORITIES, PRIORITY_LABELS, RULE_FIELDS, STATUSES, STATUS_LABELS, TAG_KIND_OPTIONS,
  actionSummary, blankAction, blankCondition, fieldType, operatorsFor, ruleSchema, splitNames, toRuleInput,
} from '@/schemas'
import type { Rule, RuleAction, RuleActionType, RuleCondition, RuleInput, RuleSample, RuleTestResult } from '@/api/types'
import { useRules } from '@/stores/rules'
import { useTickets } from '@/stores/tickets'
import { ruleErrorText } from './errors'

const props = defineProps<{ modelValue: boolean; rule: Rule | null }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void; (e: 'saved', r: Rule): void }>()

const store = useRules()
const tickets = useTickets()

interface ActionRow extends RuleAction { names: string }
interface Draft {
  name: string
  enabled: boolean
  sort_order: number
  match: 'all' | 'any'
  conditions: RuleCondition[]
  expression: string
  actions: ActionRow[]
}
const draft = reactive<Draft>({ name: '', enabled: true, sort_order: 0, match: 'all', conditions: [], expression: '', actions: [] })
const advanced = ref(false)
const issues = ref<string[]>([])
const saving = ref(false)
const sample = reactive<Required<Pick<RuleSample, 'subject' | 'from' | 'from_name' | 'recipient' | 'body' | 'has_attachments'>> & { spam_score: number }>({
  subject: '', from: '', from_name: '', recipient: '', body: '', has_attachments: false, spam_score: 0,
})
const result = ref<RuleTestResult | null>(null)
const testing = ref(false)
const testError = ref('')

const row = (a: RuleAction): ActionRow => ({ ...a, names: (a.tag_names ?? []).join(', ') })

function reset(): void {
  const r = props.rule
  draft.name = r?.name ?? ''
  draft.enabled = r?.enabled ?? true
  draft.sort_order = r?.sort_order ?? (store.items.length ? Math.max(...store.items.map((x) => x.sort_order)) + 10 : 10)
  draft.match = r?.match ?? 'all'
  draft.conditions = r ? r.conditions.map((c) => ({ ...c })) : [blankCondition()]
  draft.expression = r?.expression ?? ''
  draft.actions = r ? r.actions.map(row) : [row(blankAction('tag'))]
  advanced.value = !!r?.expression
  issues.value = []
  result.value = null
  testError.value = ''
}
watch(() => [props.modelValue, props.rule], () => {
  if (props.modelValue) {
    reset()
    if (!tickets.users.length) void tickets.assignableUsers()
  }
}, { immediate: true })

// --- conditions ---
function setField(c: RuleCondition, field: unknown): void {
  const next = blankCondition(String(field))
  const keepText = fieldType(c.field) === 'text' && fieldType(next.field) === 'text'
  Object.assign(c, keepText ? { field: next.field } : next)
}
const addCondition = () => draft.conditions.push(blankCondition())
const removeCondition = (i: number) => draft.conditions.splice(i, 1)

// --- actions ---
function setType(i: number, t: unknown): void {
  draft.actions[i] = row(blankAction(t as RuleActionType))
}
const addAction = () => draft.actions.push(row(blankAction('tag')))
const removeAction = (i: number) => draft.actions.splice(i, 1)

const fieldOptions = RULE_FIELDS.map(({ title, value }) => ({ title, value }))
const statusOptions = STATUSES.map((s) => ({ title: STATUS_LABELS[s], value: s }))
const priorityOptions = PRIORITIES.map((p) => ({ title: PRIORITY_LABELS[p], value: p }))
const userOptions = computed(() => tickets.users.map((u) => ({ title: u.name || u.id, value: u.id })))
const userName = (id: string) => tickets.users.find((u) => u.id === id)?.name || id

/** Validates the draft into a request body (issues listed when invalid). */
function body(): RuleInput | null {
  const parsed = ruleSchema.safeParse({
    name: draft.name, enabled: draft.enabled, sort_order: draft.sort_order, match: draft.match,
    conditions: advanced.value && draft.expression.trim() ? draft.conditions.filter((c) => c.value !== '' || fieldType(c.field) !== 'text') : draft.conditions,
    expression: advanced.value ? draft.expression : '',
    actions: draft.actions.map(({ names, ...a }) => (a.type === 'tag' ? { ...a, tag_names: splitNames(names) } : a)),
  })
  if (!parsed.success) {
    issues.value = parsed.error.issues.map((i) => (i.path.length ? i.path.join('.') + ': ' : '') + i.message)
    return null
  }
  issues.value = []
  return toRuleInput(parsed.data)
}

async function save(): Promise<void> {
  const b = body()
  if (!b) return
  saving.value = true
  try {
    const r = props.rule ? await store.update(props.rule.id, b) : await store.create(b)
    emit('saved', r)
    emit('update:modelValue', false)
  } catch (e) {
    issues.value = [ruleErrorText(e)]
  } finally {
    saving.value = false
  }
}

async function runTest(): Promise<void> {
  const b = body()
  result.value = null
  testError.value = ''
  if (!b) return
  testing.value = true
  try {
    result.value = await store.test(b, { ...sample, spam_score: Number(sample.spam_score) || 0 })
  } catch (e) {
    testError.value = ruleErrorText(e)
  } finally {
    testing.value = false
  }
}
</script>

<template>
  <UiDrawer :model-value="modelValue" :title="rule ? 'Edit rule' : 'New rule'" size="xl" data-test="rule-drawer" @update:model-value="emit('update:modelValue', $event)">
    <UiAlert v-if="issues.length" kind="error" class="mb-4" data-test="rule-issues">
      <ul class="list-inside list-disc">
        <li v-for="m in issues" :key="m">{{ m }}</li>
      </ul>
    </UiAlert>

    <div class="flex flex-col gap-6">
      <div class="grid grid-cols-1 gap-3 md:grid-cols-12">
        <div class="md:col-span-6"><UiInput id="rule-name" v-model="draft.name" label="Name" required data-test="rule-name" /></div>
        <div class="md:col-span-3"><UiNumberInput id="rule-sort" v-model="draft.sort_order" label="Sort order" hint="Lower runs first." :min="-1000000" :max="1000000" /></div>
        <div class="flex items-end md:col-span-3"><UiSwitch id="rule-enabled" v-model="draft.enabled" label="Enabled" /></div>
      </div>

      <UiSection title="When" description="New inbound messages only; replies to existing tickets never run rules.">
        <template v-if="!advanced">
          <div class="max-w-xs"><UiSelect id="rule-match" v-model="draft.match" label="Match" :options="MATCH_OPTIONS" :clearable="false" size="sm" /></div>
          <ol class="flex flex-col gap-2" data-test="rule-conditions">
            <li v-for="(c, i) in draft.conditions" :key="i" class="grid grid-cols-1 items-end gap-2 rounded-box border border-base-300 p-2 md:grid-cols-12" :data-test="'rule-condition-' + i">
              <div class="md:col-span-3"><UiSelect :id="'cond-field-' + i" label="Field" :model-value="c.field" :options="fieldOptions" :clearable="false" size="sm" @update:model-value="setField(c, $event)" /></div>
              <div class="md:col-span-3"><UiSelect :id="'cond-op-' + i" v-model="c.operator" label="Operator" :options="operatorsFor(c.field)" :clearable="false" size="sm" /></div>
              <div class="md:col-span-5">
                <UiSelect v-if="fieldType(c.field) === 'bool'" :id="'cond-value-' + i" v-model="c.value" label="Value" :options="BOOL_VALUES" :clearable="false" size="sm" />
                <UiInput v-else-if="fieldType(c.field) === 'number'" :id="'cond-value-' + i" v-model="c.value" label="Value" type="number" step="any" size="sm" />
                <UiInput v-else :id="'cond-value-' + i" v-model="c.value" label="Value" size="sm" :placeholder="c.operator === 'matches' ? '(?i)invoice [0-9]+' : ''" />
              </div>
              <div class="md:col-span-1"><UiButton size="sm" variant="text" color="error" icon="mdi-delete-outline" icon-only label="Remove condition" :data-test="'rule-condition-remove-' + i" @click="removeCondition(i)" /></div>
            </li>
          </ol>
          <div><UiButton size="sm" variant="soft" icon="mdi-plus" data-test="rule-condition-add" @click="addCondition">Add condition</UiButton></div>
        </template>
        <UiTextarea v-else id="rule-expression" v-model="draft.expression" label="Expression (CEL)" :rows="4" hint="Overrides the conditions. Fields: subject, body, from, fromName, recipient, fromDomain (strings), hasAttachments (bool), spamScore (double)." data-test="rule-expression" />
        <div><UiButton size="xs" variant="text" :icon="advanced ? 'mdi-filter-outline' : 'mdi-console'" data-test="rule-advanced" @click="advanced = !advanced">{{ advanced ? 'Use conditions' : 'Advanced expression' }}</UiButton></div>
      </UiSection>

      <UiSection title="Then" description="Tags accumulate across matching rules; for assign, status and priority the first matching rule wins.">
        <ol class="flex flex-col gap-2" data-test="rule-actions">
          <li v-for="(a, i) in draft.actions" :key="i" class="grid grid-cols-1 items-end gap-2 rounded-box border border-base-300 p-2 md:grid-cols-12" :data-test="'rule-action-' + i">
            <div class="md:col-span-3"><UiSelect :id="'act-type-' + i" label="Action" :model-value="a.type" :options="ACTION_TYPES" :clearable="false" size="sm" @update:model-value="setType(i, $event)" /></div>
            <template v-if="a.type === 'tag'">
              <div class="md:col-span-3"><UiSelect :id="'act-kind-' + i" v-model="a.tag_kind" label="Kind" :options="TAG_KIND_OPTIONS" :clearable="false" size="sm" /></div>
              <div class="md:col-span-5"><UiInput :id="'act-names-' + i" v-model="a.names" label="Tag names" hint="Comma-separated; missing tags are created." size="sm" /></div>
            </template>
            <div v-else-if="a.type === 'assign'" class="md:col-span-8"><UiSelect :id="'act-assignee-' + i" v-model="a.assignee_id" label="Assignee" :options="userOptions" size="sm" /></div>
            <div v-else-if="a.type === 'status'" class="md:col-span-8"><UiSelect :id="'act-status-' + i" v-model="a.status" label="Status" :options="statusOptions" :clearable="false" size="sm" /></div>
            <div v-else-if="a.type === 'priority'" class="md:col-span-8"><UiSelect :id="'act-priority-' + i" v-model="a.priority" label="Priority" :options="priorityOptions" :clearable="false" size="sm" /></div>
            <p v-else class="text-sm text-warning md:col-span-8" data-test="rule-drop-warning">{{ DROP_WARNING }}</p>
            <div class="md:col-span-1"><UiButton size="sm" variant="text" color="error" icon="mdi-delete-outline" icon-only label="Remove action" :data-test="'rule-action-remove-' + i" @click="removeAction(i)" /></div>
          </li>
        </ol>
        <div><UiButton size="sm" variant="soft" icon="mdi-plus" data-test="rule-action-add" @click="addAction">Add action</UiButton></div>
      </UiSection>

      <UiSection title="Test" description="Dry-run the unsaved rule against a sample message; nothing is stored.">
        <div class="grid grid-cols-1 gap-2 md:grid-cols-2" data-test="rule-test">
          <UiInput id="sample-subject" v-model="sample.subject" label="Subject" size="sm" />
          <UiInput id="sample-from" v-model="sample.from" label="Sender email" size="sm" />
          <UiInput id="sample-from-name" v-model="sample.from_name" label="Sender name" size="sm" />
          <UiInput id="sample-recipient" v-model="sample.recipient" label="Recipient" size="sm" />
          <UiInput id="sample-spam" v-model="sample.spam_score" label="Spam score" type="number" step="any" size="sm" />
          <div class="flex items-end"><UiSwitch id="sample-attachments" v-model="sample.has_attachments" label="Has attachments" /></div>
          <div class="md:col-span-2"><UiTextarea id="sample-body" v-model="sample.body" label="Body" :rows="3" /></div>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <UiButton size="sm" variant="soft" icon="mdi-magnify-scan" :loading="testing" data-test="rule-test-run" @click="runTest">Run test</UiButton>
          <template v-if="result">
            <UiBadge :color="result.matched ? 'success' : 'neutral'" data-test="rule-test-result">{{ result.matched ? 'Matches' : 'No match' }}</UiBadge>
            <span v-for="(a, i) in result.actions" :key="i" class="text-sm">{{ actionSummary(a, userName) }}</span>
          </template>
          <span v-if="testError" class="text-sm text-error" data-test="rule-test-error">{{ testError }}</span>
        </div>
      </UiSection>
    </div>

    <template #actions>
      <UiButton variant="text" @click="emit('update:modelValue', false)">Cancel</UiButton>
      <UiButton icon="mdi-check" :loading="saving" data-test="rule-save" @click="save">Save</UiButton>
    </template>
  </UiDrawer>
</template>
