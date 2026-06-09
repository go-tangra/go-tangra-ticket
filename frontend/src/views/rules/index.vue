<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue';

import {
  Button,
  Input,
  InputNumber,
  message as antMessage,
  Modal,
  Popconfirm,
  Select,
  Switch,
  Table,
} from 'ant-design-vue';

import { $t } from 'shell/locales';

import type {
  AssignableUser,
  RuleAction,
  RuleCondition,
  RuleInput,
  TicketRule,
} from '../../api/client';
import { useTagStore } from '../../stores/tag.state';
import { useTicketStore } from '../../stores/ticket.state';
import {
  humanizeEnum,
  priorityOptions,
  statusOptions,
} from '../tickets/helpers';
import {
  actionTypeOptions,
  booleanFields,
  booleanValueOptions,
  fieldLabel,
  fieldOptions,
  isNumericField,
  matchOptions,
  numericOperatorOptions,
  operatorLabel,
  operatorOptions,
  tagKindOptions,
} from './helpers';

const store = useTagStore();
const ticketStore = useTicketStore();

const rows = ref<TicketRule[]>([]);
const loading = ref(false);
const users = ref<AssignableUser[]>([]);

const modalOpen = ref(false);
const saving = ref(false);
const editingId = ref<string>('');

type FormState = {
  name: string;
  enabled: boolean;
  sortOrder: number;
  match: string;
  conditions: RuleCondition[];
  actions: RuleAction[];
};

const form = reactive<FormState>({
  name: '',
  enabled: true,
  sortOrder: 0,
  match: 'ALL',
  conditions: [newCondition()],
  actions: [newAction('tag')],
});

function newCondition(): RuleCondition {
  return { field: 'subject', operator: 'contains', value: '' };
}

function newAction(type: string): RuleAction {
  return {
    type,
    tagKind: 'TAG',
    tagNames: [],
    assigneeId: 0,
    status: 'TICKET_STATUS_OPEN',
    priority: 'TICKET_PRIORITY_NORMAL',
  };
}

const assigneeOptions = () => [
  { value: 0, label: $t('ticket.page.ticket.unassigned') },
  ...users.value.map((u) => ({ value: u.id, label: u.name || u.username })),
];

async function load() {
  loading.value = true;
  try {
    rows.value = await store.listRules();
  } catch (e: any) {
    antMessage.error(e?.message ?? 'Load failed');
  } finally {
    loading.value = false;
  }
}

async function loadUsers() {
  try {
    users.value = await ticketStore.listAssignableUsers();
  } catch {
    // best-effort: assign action still works by typing handled elsewhere
  }
}

function isBoolean(field?: string): boolean {
  return !!field && booleanFields.has(field);
}

function isNumeric(field?: string): boolean {
  return isNumericField(field);
}

function onFieldChange(cond: RuleCondition) {
  // Seed sensible operator/value defaults for the field's type.
  if (isBoolean(cond.field)) {
    if (cond.value !== 'true' && cond.value !== 'false') cond.value = 'true';
  } else if (isNumeric(cond.field)) {
    if (!numericOperatorOptions.some((o) => o.value === cond.operator)) {
      cond.operator = 'gt';
    }
    if (cond.value === '' || Number.isNaN(Number(cond.value))) cond.value = '0';
  } else {
    // string field
    if (!operatorOptions.some((o) => o.value === cond.operator)) {
      cond.operator = 'contains';
    }
  }
}

function openCreate() {
  editingId.value = '';
  form.name = '';
  form.enabled = true;
  form.sortOrder = (rows.value.length + 1) * 10;
  form.match = 'ALL';
  form.conditions = [newCondition()];
  form.actions = [newAction('tag')];
  modalOpen.value = true;
}

function actionsFromRule(row: TicketRule): RuleAction[] {
  if (row.actions && row.actions.length) {
    return row.actions.map((a) => ({ ...newAction(a.type || 'tag'), ...a }));
  }
  // Backward compat: legacy single tag action.
  if (row.tagNames && row.tagNames.length) {
    return [
      { ...newAction('tag'), tagKind: row.tagKind || 'TAG', tagNames: [...row.tagNames] },
    ];
  }
  return [newAction('tag')];
}

function openEdit(row: TicketRule) {
  editingId.value = row.id ?? '';
  form.name = row.name ?? '';
  form.enabled = row.enabled ?? true;
  form.sortOrder = row.sortOrder ?? 0;
  form.match = row.match || 'ALL';
  form.conditions =
    row.conditions && row.conditions.length
      ? row.conditions.map((c) => ({ ...c }))
      : [newCondition()];
  form.actions = actionsFromRule(row);
  modalOpen.value = true;
}

function addCondition() {
  form.conditions.push(newCondition());
}

function removeCondition(idx: number) {
  form.conditions.splice(idx, 1);
  if (form.conditions.length === 0) form.conditions.push(newCondition());
}

function addAction() {
  form.actions.push(newAction('tag'));
}

function removeAction(idx: number) {
  form.actions.splice(idx, 1);
  if (form.actions.length === 0) form.actions.push(newAction('tag'));
}

// Keep only actions that carry meaningful parameters.
function cleanActions(): RuleAction[] {
  const out: RuleAction[] = [];
  for (const a of form.actions) {
    if (a.type === 'tag') {
      if (a.tagNames && a.tagNames.length) {
        out.push({ type: 'tag', tagKind: a.tagKind, tagNames: a.tagNames });
      }
    } else if (a.type === 'assign') {
      out.push({ type: 'assign', assigneeId: a.assigneeId ?? 0 });
    } else if (a.type === 'status') {
      if (a.status) out.push({ type: 'status', status: a.status });
    } else if (a.type === 'priority') {
      if (a.priority) out.push({ type: 'priority', priority: a.priority });
    } else if (a.type === 'drop') {
      out.push({ type: 'drop' });
    }
  }
  return out;
}

async function save() {
  if (!form.name.trim()) {
    antMessage.warning($t('ticket.page.rule.nameRequired'));
    return;
  }
  const conditions = form.conditions.filter((c) => (c.value ?? '').trim() !== '');
  if (!conditions.length) {
    antMessage.warning($t('ticket.page.rule.conditionRequired'));
    return;
  }
  const actions = cleanActions();
  if (!actions.length) {
    antMessage.warning($t('ticket.page.rule.actionRequired'));
    return;
  }
  const rule: RuleInput = {
    name: form.name.trim(),
    enabled: form.enabled,
    sortOrder: form.sortOrder,
    match: form.match,
    conditions,
    expression: '',
    tagKind: '',
    tagNames: [],
    actions,
  };
  saving.value = true;
  try {
    if (editingId.value) {
      await store.updateRule(editingId.value, rule);
    } else {
      await store.createRule(rule);
    }
    antMessage.success($t('ticket.page.rule.saveSuccess'));
    modalOpen.value = false;
    load();
  } catch (e: any) {
    antMessage.error(e?.message ?? 'Save failed');
  } finally {
    saving.value = false;
  }
}

async function toggleEnabled(row: TicketRule, value: boolean) {
  try {
    await store.updateRule(row.id ?? '', {
      name: row.name,
      enabled: value,
      sortOrder: row.sortOrder,
      match: row.match,
      conditions: row.conditions ?? [],
      expression: row.expression ?? '',
      tagKind: '',
      tagNames: [],
      actions: actionsFromRule(row),
    });
    row.enabled = value;
  } catch (e: any) {
    antMessage.error(e?.message ?? 'Update failed');
    load();
  }
}

async function remove(row: TicketRule) {
  try {
    await store.deleteRule(row.id ?? '');
    antMessage.success($t('ticket.page.rule.deleteSuccess'));
    load();
  } catch (e: any) {
    antMessage.error(e?.message ?? 'Delete failed');
  }
}

function userName(id?: number): string {
  if (!id) return $t('ticket.page.ticket.unassigned');
  const u = users.value.find((x) => x.id === id);
  return u ? u.name || u.username : `#${id}`;
}

function conditionText(c: RuleCondition): string {
  if (isBoolean(c.field)) {
    return `${fieldLabel(c.field)} = ${c.value === 'false' ? 'No' : 'Yes'}`;
  }
  if (isNumeric(c.field)) {
    return `${fieldLabel(c.field)} ${operatorLabel(c.operator)} ${c.value}`;
  }
  return `${fieldLabel(c.field)} ${operatorLabel(c.operator)} "${c.value}"`;
}

function summarizeConditions(row: TicketRule): string {
  const conds = (row.conditions ?? [])
    .map(conditionText)
    .join(row.match === 'ANY' ? '  OR  ' : '  AND  ');
  return conds || '—';
}

function actionText(a: RuleAction): string {
  switch (a.type) {
    case 'tag':
      return `${$t('ticket.page.rule.actionTag')}: ${(a.tagNames ?? []).join(', ')}`;
    case 'assign':
      return `${$t('ticket.page.rule.actionAssign')}: ${userName(a.assigneeId)}`;
    case 'status':
      return `${$t('ticket.page.rule.actionStatus')}: ${humanizeEnum(a.status)}`;
    case 'priority':
      return `${$t('ticket.page.rule.actionPriority')}: ${humanizeEnum(a.priority)}`;
    case 'drop':
      return $t('ticket.page.rule.actionDrop');
    default:
      return a.type ?? '';
  }
}

const columns = [
  { title: $t('ticket.page.rule.name'), key: 'name', width: 160 },
  { title: $t('ticket.page.rule.conditions'), key: 'summary', ellipsis: true },
  { title: $t('ticket.page.rule.actions'), key: 'actions', width: 240 },
  { title: $t('ticket.page.rule.enabled'), key: 'enabled', width: 80 },
  { title: '', key: 'ops', width: 140 },
];

onMounted(() => {
  load();
  loadUsers();
});
</script>

<template>
  <div style="padding: 16px">
    <div style="display: flex; margin-bottom: 12px; align-items: center">
      <div style="flex: 1; color: #888">{{ $t('ticket.page.rule.hint') }}</div>
      <Button type="primary" @click="openCreate">
        {{ $t('ticket.page.rule.create') }}
      </Button>
    </div>

    <Table
      :columns="columns"
      :data-source="rows"
      :loading="loading"
      :pagination="false"
      row-key="id"
      size="small"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'summary'">
          <span style="font-size: 12px; color: #555">{{ summarizeConditions(record) }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <div
            v-for="(a, i) in actionsFromRule(record)"
            :key="i"
            style="font-size: 12px; color: #555"
          >
            {{ actionText(a) }}
          </div>
        </template>
        <template v-else-if="column.key === 'enabled'">
          <Switch
            :checked="record.enabled"
            size="small"
            @change="(v: any) => toggleEnabled(record, v)"
          />
        </template>
        <template v-else-if="column.key === 'ops'">
          <Button size="small" type="link" @click="openEdit(record)">
            {{ $t('ticket.action.edit') }}
          </Button>
          <Popconfirm :title="$t('ticket.page.rule.confirmDelete')" @confirm="remove(record)">
            <Button size="small" type="link" danger>{{ $t('ticket.action.delete') }}</Button>
          </Popconfirm>
        </template>
      </template>
    </Table>

    <Modal
      v-model:open="modalOpen"
      :title="editingId ? $t('ticket.page.rule.edit') : $t('ticket.page.rule.create')"
      :confirm-loading="saving"
      width="820px"
      @ok="save"
    >
      <div style="display: flex; flex-direction: column; gap: 14px; padding: 8px 0">
        <div style="display: flex; gap: 12px">
          <div style="flex: 2">
            <div style="margin-bottom: 4px; font-weight: 500">{{ $t('ticket.page.rule.name') }}</div>
            <Input v-model:value="form.name" :placeholder="$t('ticket.page.rule.name')" />
          </div>
          <div style="width: 120px">
            <div style="margin-bottom: 4px; font-weight: 500">{{ $t('ticket.page.rule.sortOrder') }}</div>
            <InputNumber v-model:value="form.sortOrder" :min="0" style="width: 100%" />
          </div>
          <div style="width: 100px">
            <div style="margin-bottom: 4px; font-weight: 500">{{ $t('ticket.page.rule.enabled') }}</div>
            <Switch v-model:checked="form.enabled" />
          </div>
        </div>

        <!-- WHEN: condition builder (Cloudflare-style) -->
        <div style="border: 1px solid var(--border, #e5e7eb); border-radius: 8px; padding: 12px">
          <div style="display: flex; align-items: center; gap: 8px; margin-bottom: 10px">
            <b>{{ $t('ticket.page.rule.when') }}</b>
            <Select v-model:value="form.match" :options="matchOptions" style="width: 220px" size="small" />
          </div>

          <div
            v-for="(cond, idx) in form.conditions"
            :key="idx"
            style="display: flex; gap: 8px; align-items: center; margin-bottom: 8px"
          >
            <span v-if="idx > 0" style="width: 44px; color: #888; font-size: 12px">
              {{ form.match === 'ANY' ? $t('ticket.page.rule.or') : $t('ticket.page.rule.and') }}
            </span>
            <span v-else style="width: 44px"></span>
            <Select
              v-model:value="cond.field"
              :options="fieldOptions"
              style="width: 160px"
              @change="() => onFieldChange(cond)"
            />
            <template v-if="isBoolean(cond.field)">
              <Select
                v-model:value="cond.value"
                :options="booleanValueOptions"
                style="width: 360px"
              />
            </template>
            <template v-else-if="isNumeric(cond.field)">
              <Select
                v-model:value="cond.operator"
                :options="numericOperatorOptions"
                style="width: 200px"
              />
              <InputNumber
                v-model:value="cond.value"
                :step="0.1"
                string-mode
                :placeholder="$t('ticket.page.rule.value')"
                style="flex: 1"
              />
            </template>
            <template v-else>
              <Select
                v-model:value="cond.operator"
                :options="operatorOptions"
                style="width: 170px"
              />
              <Input
                v-model:value="cond.value"
                :placeholder="$t('ticket.page.rule.value')"
                style="flex: 1"
              />
            </template>
            <Button
              type="text"
              danger
              size="small"
              :disabled="form.conditions.length === 1"
              @click="removeCondition(idx)"
            >
              ✕
            </Button>
          </div>

          <Button type="dashed" size="small" block @click="addCondition">
            + {{ $t('ticket.page.rule.addCondition') }}
          </Button>
        </div>

        <!-- THEN: multiple actions -->
        <div style="border: 1px solid var(--border, #e5e7eb); border-radius: 8px; padding: 12px">
          <div style="margin-bottom: 10px"><b>{{ $t('ticket.page.rule.then') }}</b></div>

          <div
            v-for="(act, idx) in form.actions"
            :key="idx"
            style="display: flex; gap: 8px; align-items: center; margin-bottom: 8px"
          >
            <Select
              v-model:value="act.type"
              :options="actionTypeOptions"
              style="width: 160px"
            />

            <!-- tag params -->
            <template v-if="act.type === 'tag'">
              <Select
                v-model:value="act.tagKind"
                :options="tagKindOptions"
                style="width: 130px"
              />
              <Select
                v-model:value="act.tagNames"
                mode="tags"
                :placeholder="$t('ticket.page.rule.tagsPlaceholder')"
                style="flex: 1"
                :token-separators="[',']"
              />
            </template>

            <!-- assign params -->
            <template v-else-if="act.type === 'assign'">
              <Select
                v-model:value="act.assigneeId"
                :options="assigneeOptions()"
                style="flex: 1"
                show-search
                option-filter-prop="label"
              />
            </template>

            <!-- status params -->
            <template v-else-if="act.type === 'status'">
              <Select
                v-model:value="act.status"
                :options="statusOptions"
                style="flex: 1"
              />
            </template>

            <!-- priority params -->
            <template v-else-if="act.type === 'priority'">
              <Select
                v-model:value="act.priority"
                :options="priorityOptions"
                style="flex: 1"
              />
            </template>

            <!-- drop: no params -->
            <template v-else-if="act.type === 'drop'">
              <span style="flex: 1; color: #c0392b; font-size: 12px">
                {{ $t('ticket.page.rule.dropHint') }}
              </span>
            </template>

            <Button
              type="text"
              danger
              size="small"
              :disabled="form.actions.length === 1"
              @click="removeAction(idx)"
            >
              ✕
            </Button>
          </div>

          <Button type="dashed" size="small" block @click="addAction">
            + {{ $t('ticket.page.rule.addAction') }}
          </Button>
          <div style="margin-top: 6px; color: #888; font-size: 12px">
            {{ $t('ticket.page.rule.tagsHint') }}
          </div>
        </div>
      </div>
    </Modal>
  </div>
</template>
