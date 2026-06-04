<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue';

import {
  Button,
  Input,
  message as antMessage,
  Modal,
  Popconfirm,
  Select,
  Table,
  Tag,
} from 'ant-design-vue';

import { $t } from 'shell/locales';

import type { TicketTag } from '../../api/client';
import { useTagStore } from '../../stores/tag.state';
import { tagColor, tagKindOptions } from '../rules/helpers';

const store = useTagStore();

const rows = ref<TicketTag[]>([]);
const loading = ref(false);
const kindFilter = ref<string | undefined>(undefined);

const modalOpen = ref(false);
const saving = ref(false);
const editingId = ref<string>('');

const form = reactive<{
  name: string;
  kind: string;
  color: string;
  description: string;
}>({
  name: '',
  kind: 'TAG',
  color: '',
  description: '',
});

const colorOptions = [
  { value: '', label: 'Auto' },
  { value: 'blue', label: 'Blue' },
  { value: 'green', label: 'Green' },
  { value: 'orange', label: 'Orange' },
  { value: 'red', label: 'Red' },
  { value: 'purple', label: 'Purple' },
  { value: 'cyan', label: 'Cyan' },
  { value: 'magenta', label: 'Magenta' },
  { value: 'gold', label: 'Gold' },
];

async function load() {
  loading.value = true;
  try {
    rows.value = await store.listTags(kindFilter.value);
  } catch (e: any) {
    antMessage.error(e?.message ?? 'Load failed');
  } finally {
    loading.value = false;
  }
}

function openCreate() {
  editingId.value = '';
  form.name = '';
  form.kind = 'TAG';
  form.color = '';
  form.description = '';
  modalOpen.value = true;
}

function openEdit(row: TicketTag) {
  editingId.value = row.id ?? '';
  form.name = row.name ?? '';
  form.kind = row.kind ?? 'TAG';
  form.color = row.color ?? '';
  form.description = row.description ?? '';
  modalOpen.value = true;
}

async function save() {
  if (!form.name.trim()) {
    antMessage.warning($t('ticket.page.tag.nameRequired'));
    return;
  }
  saving.value = true;
  try {
    if (editingId.value) {
      await store.updateTag(editingId.value, {
        name: form.name.trim(),
        color: form.color,
        description: form.description,
      });
    } else {
      await store.createTag({
        name: form.name.trim(),
        kind: form.kind,
        color: form.color,
        description: form.description,
      });
    }
    antMessage.success($t('ticket.page.tag.saveSuccess'));
    modalOpen.value = false;
    load();
  } catch (e: any) {
    antMessage.error(e?.message ?? 'Save failed');
  } finally {
    saving.value = false;
  }
}

async function remove(row: TicketTag) {
  try {
    await store.deleteTag(row.id ?? '');
    antMessage.success($t('ticket.page.tag.deleteSuccess'));
    load();
  } catch (e: any) {
    antMessage.error(e?.message ?? 'Delete failed');
  }
}

const columns = [
  { title: $t('ticket.page.tag.name'), key: 'name' },
  { title: $t('ticket.page.tag.kind'), key: 'kind', width: 140 },
  { title: $t('ticket.page.tag.description'), dataIndex: 'description', key: 'description', ellipsis: true },
  { title: '', key: 'actions', width: 150 },
];

onMounted(load);
</script>

<template>
  <div style="padding: 16px">
    <div style="display: flex; gap: 8px; margin-bottom: 12px; align-items: center">
      <Select
        v-model:value="kindFilter"
        :options="tagKindOptions"
        :placeholder="$t('ticket.page.tag.kind')"
        style="width: 160px"
        allow-clear
        @change="load"
      />
      <div style="flex: 1"></div>
      <Button type="primary" @click="openCreate">
        {{ $t('ticket.page.tag.create') }}
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
        <template v-if="column.key === 'name'">
          <Tag :color="tagColor(record.color, record.name)">{{ record.name }}</Tag>
        </template>
        <template v-else-if="column.key === 'kind'">
          {{ record.kind === 'CATEGORY' ? $t('ticket.page.tag.category') : $t('ticket.page.tag.tag') }}
        </template>
        <template v-else-if="column.key === 'actions'">
          <Button size="small" type="link" @click="openEdit(record)">
            {{ $t('ticket.action.edit') }}
          </Button>
          <Popconfirm :title="$t('ticket.page.tag.confirmDelete')" @confirm="remove(record)">
            <Button size="small" type="link" danger>{{ $t('ticket.action.delete') }}</Button>
          </Popconfirm>
        </template>
      </template>
    </Table>

    <Modal
      v-model:open="modalOpen"
      :title="editingId ? $t('ticket.page.tag.edit') : $t('ticket.page.tag.create')"
      :confirm-loading="saving"
      @ok="save"
    >
      <div style="display: flex; flex-direction: column; gap: 12px; padding: 8px 0">
        <div>
          <div style="margin-bottom: 4px; font-weight: 500">{{ $t('ticket.page.tag.name') }}</div>
          <Input v-model:value="form.name" :placeholder="$t('ticket.page.tag.name')" />
        </div>
        <div>
          <div style="margin-bottom: 4px; font-weight: 500">{{ $t('ticket.page.tag.kind') }}</div>
          <Select
            v-model:value="form.kind"
            :options="tagKindOptions"
            :disabled="!!editingId"
            style="width: 100%"
          />
        </div>
        <div>
          <div style="margin-bottom: 4px; font-weight: 500">{{ $t('ticket.page.tag.color') }}</div>
          <Select v-model:value="form.color" :options="colorOptions" style="width: 100%" />
          <div style="margin-top: 6px">
            <Tag :color="tagColor(form.color, form.name)">{{ form.name || 'preview' }}</Tag>
          </div>
        </div>
        <div>
          <div style="margin-bottom: 4px; font-weight: 500">{{ $t('ticket.page.tag.description') }}</div>
          <Input v-model:value="form.description" :placeholder="$t('ticket.page.tag.description')" />
        </div>
      </div>
    </Modal>
  </div>
</template>
