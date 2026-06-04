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
  Tag,
} from 'ant-design-vue';

import { $t } from 'shell/locales';

import type { RuleCondition, RuleInput, TicketRule } from '../../api/client';
import { useTagStore } from '../../stores/tag.state';
import {
  fieldLabel,
  fieldOptions,
  matchOptions,
  operatorLabel,
  operatorOptions,
  tagColor,
  tagKindOptions,
} from './helpers';

const store = useTagStore();

const rows = ref<TicketRule[]>([]);
const loading = ref(false);

const modalOpen = ref(false);
const saving = ref(false);
const editingId = ref<string>('');

type FormState = {
  name: string;
  enabled: boolean;
  sortOrder: number;
  match: string;
  conditions: RuleCondition[];
  tagKind: string;
  tagNames: string[];
};

const form = reactive<FormState>({
  name: '',
  enabled: true,
  sortOrder: 0,
  match: 'ALL',
  conditions: [{ field: 'subject', operator: 'contains', value: '' }],
  tagKind: 'TAG',
  tagNames: [],
});

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

function newCondition(): RuleCondition {
  return { field: 'subject', operator: 'contains', value: '' };
}

function openCreate() {
  editingId.value = '';
  form.name = '';
  form.enabled = true;
  form.sortOrder = (rows.value.length + 1) * 10;
  form.match = 'ALL';
  form.conditions = [newCondition()];
  form.tagKind = 'TAG';
  form.tagNames = [];
  modalOpen.value = true;
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
  form.tagKind = row.tagKind || 'TAG';
  form.tagNames = [...(row.tagNames ?? [])];
  modalOpen.value = true;
}

function addCondition() {
  form.conditions.push(newCondition());
}

function removeCondition(idx: number) {
  form.conditions.splice(idx, 1);
  if (form.conditions.length === 0) form.conditions.push(newCondition());
}

async function save() {
  if (!form.name.trim()) {
    antMessage.warning($t('ticket.page.rule.nameRequired'));
    return;
  }
  if (!form.tagNames.length) {
    antMessage.warning($t('ticket.page.rule.tagsRequired'));
    return;
  }
  const conditions = form.conditions.filter((c) => (c.value ?? '').trim() !== '');
  if (!conditions.length) {
    antMessage.warning($t('ticket.page.rule.conditionRequired'));
    return;
  }
  const rule: RuleInput = {
    name: form.name.trim(),
    enabled: form.enabled,
    sortOrder: form.sortOrder,
    match: form.match,
    conditions,
    expression: '',
    tagKind: form.tagKind,
    tagNames: form.tagNames,
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
      tagKind: row.tagKind,
      tagNames: row.tagNames ?? [],
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

function summarize(row: TicketRule): string {
  const conds = (row.conditions ?? [])
    .map((c) => `${fieldLabel(c.field)} ${operatorLabel(c.operator)} "${c.value}"`)
    .join(row.match === 'ANY' ? '  OR  ' : '  AND  ');
  return conds || '—';
}

const columns = [
  { title: $t('ticket.page.rule.name'), key: 'name', width: 180 },
  { title: $t('ticket.page.rule.conditions'), key: 'summary', ellipsis: true },
  { title: $t('ticket.page.rule.tags'), key: 'tags', width: 200 },
  { title: $t('ticket.page.rule.enabled'), key: 'enabled', width: 90 },
  { title: '', key: 'actions', width: 150 },
];

onMounted(load);
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
          <span style="font-size: 12px; color: #555">{{ summarize(record) }}</span>
        </template>
        <template v-else-if="column.key === 'tags'">
          <Tag
            v-for="n in record.tagNames || []"
            :key="n"
            :color="tagColor(undefined, n)"
          >
            {{ n }}
          </Tag>
        </template>
        <template v-else-if="column.key === 'enabled'">
          <Switch
            :checked="record.enabled"
            size="small"
            @change="(v: any) => toggleEnabled(record, v)"
          />
        </template>
        <template v-else-if="column.key === 'actions'">
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
      width="760px"
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
              style="width: 150px"
            />
            <Select
              v-model:value="cond.operator"
              :options="operatorOptions"
              style="width: 180px"
            />
            <Input
              v-model:value="cond.value"
              :placeholder="$t('ticket.page.rule.value')"
              style="flex: 1"
            />
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

        <!-- THEN: tags to apply -->
        <div style="border: 1px solid var(--border, #e5e7eb); border-radius: 8px; padding: 12px">
          <div style="margin-bottom: 10px"><b>{{ $t('ticket.page.rule.then') }}</b></div>
          <div style="display: flex; gap: 12px">
            <div style="width: 160px">
              <div style="margin-bottom: 4px; font-weight: 500">{{ $t('ticket.page.rule.tagKind') }}</div>
              <Select v-model:value="form.tagKind" :options="tagKindOptions" style="width: 100%" />
            </div>
            <div style="flex: 1">
              <div style="margin-bottom: 4px; font-weight: 500">{{ $t('ticket.page.rule.tags') }}</div>
              <Select
                v-model:value="form.tagNames"
                mode="tags"
                :placeholder="$t('ticket.page.rule.tagsPlaceholder')"
                style="width: 100%"
                :token-separators="[',']"
              />
            </div>
          </div>
          <div style="margin-top: 6px; color: #888; font-size: 12px">
            {{ $t('ticket.page.rule.tagsHint') }}
          </div>
        </div>
      </div>
    </Modal>
  </div>
</template>
