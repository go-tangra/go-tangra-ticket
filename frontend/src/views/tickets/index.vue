<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue';

import {
  Button,
  Input,
  message as antMessage,
  Pagination,
  Select,
  Table,
  Tag,
} from 'ant-design-vue';

import { $t } from 'shell/locales';

import type {
  AssignableUser,
  Ticket,
  TicketPriority,
  TicketStatus,
} from '../../api/client';
import { formatDateTime } from '../../datetime';
import { useTicketStore } from '../../stores/ticket.state';
import { tagColor } from '../rules/helpers';
import {
  humanizeEnum,
  priorityColor,
  priorityOptions,
  statusColor,
  statusOptions,
} from './helpers';
import TicketDrawer from './ticket-drawer.vue';

const store = useTicketStore();

const rows = ref<Ticket[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = ref(20);
const loading = ref(false);

const users = ref<AssignableUser[]>([]);
const userMap = ref<Record<number, string>>({});

const drawerOpen = ref(false);
const selectedId = ref<string>('');

const filters = reactive<{
  status: TicketStatus | undefined;
  priority: TicketPriority | undefined;
  assigneeId: number | undefined;
  query: string;
}>({
  status: undefined,
  priority: undefined,
  assigneeId: undefined,
  query: '',
});

const assigneeOptions = ref<{ value: number; label: string }[]>([]);

async function loadUsers() {
  try {
    users.value = await store.listAssignableUsers();
    const map: Record<number, string> = {};
    for (const u of users.value) map[u.id] = u.name || u.username;
    userMap.value = map;
    assigneeOptions.value = users.value.map((u) => ({
      value: u.id,
      label: u.name || u.username,
    }));
  } catch {
    // assignable users are best-effort (admin-service may be unavailable)
  }
}

async function load() {
  loading.value = true;
  try {
    const res = await store.listTickets({
      page: page.value,
      pageSize: pageSize.value,
      status: filters.status,
      priority: filters.priority,
      assigneeId: filters.assigneeId,
      query: filters.query.trim() || undefined,
    });
    rows.value = res.tickets ?? [];
    total.value = res.total ?? 0;
  } catch (e: any) {
    antMessage.error(e?.message ?? 'Load failed');
  } finally {
    loading.value = false;
  }
}

function search() {
  page.value = 1;
  load();
}

function reset() {
  filters.status = undefined;
  filters.priority = undefined;
  filters.assigneeId = undefined;
  filters.query = '';
  search();
}

function openDrawer(row: Ticket) {
  selectedId.value = row.id;
  drawerOpen.value = true;
}

function assigneeLabel(row: Ticket): string {
  if (!row.assigneeId) return $t('ticket.page.ticket.unassigned');
  return row.assigneeName || userMap.value[row.assigneeId] || `#${row.assigneeId}`;
}

const columns = [
  { title: $t('ticket.page.ticket.subject'), dataIndex: 'subject', key: 'subject', ellipsis: true },
  { title: $t('ticket.page.ticket.status'), key: 'status', width: 130 },
  { title: $t('ticket.page.ticket.priority'), key: 'priority', width: 110 },
  { title: $t('ticket.page.ticket.tags'), key: 'tags', width: 180 },
  { title: $t('ticket.page.ticket.requester'), key: 'requester', width: 200 },
  { title: $t('ticket.page.ticket.assignee'), key: 'assignee', width: 150 },
  { title: $t('ticket.page.ticket.created'), key: 'createTime', width: 170 },
  { title: '', key: 'actions', width: 90 },
];

onMounted(() => {
  loadUsers();
  load();
});
</script>

<template>
  <div style="padding: 16px">
    <div style="display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 12px; align-items: center">
      <Input
        v-model:value="filters.query"
        placeholder="Search subject / requester"
        style="width: 240px"
        allow-clear
        @press-enter="search"
      />
      <Select
        v-model:value="filters.status"
        :options="statusOptions"
        placeholder="Status"
        style="width: 170px"
        allow-clear
        @change="search"
      />
      <Select
        v-model:value="filters.priority"
        :options="priorityOptions"
        placeholder="Priority"
        style="width: 150px"
        allow-clear
        @change="search"
      />
      <Select
        v-model:value="filters.assigneeId"
        :options="assigneeOptions"
        placeholder="Assignee"
        style="width: 200px"
        allow-clear
        show-search
        option-filter-prop="label"
        @change="search"
      />
      <Button type="primary" @click="search">Search</Button>
      <Button @click="reset">Reset</Button>
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
        <template v-if="column.key === 'status'">
          <Tag :color="statusColor(record.status)">{{ humanizeEnum(record.status) }}</Tag>
        </template>
        <template v-else-if="column.key === 'priority'">
          <Tag :color="priorityColor(record.priority)">{{ humanizeEnum(record.priority) }}</Tag>
        </template>
        <template v-else-if="column.key === 'tags'">
          <Tag
            v-for="t in record.tags || []"
            :key="t.id"
            :color="tagColor(t.color, t.name)"
            style="margin-bottom: 2px"
          >
            {{ t.name }}
          </Tag>
          <span v-if="!record.tags || !record.tags.length" style="color: #ccc">—</span>
        </template>
        <template v-else-if="column.key === 'requester'">
          {{ record.requesterName || record.requesterEmail || '—' }}
        </template>
        <template v-else-if="column.key === 'assignee'">
          {{ assigneeLabel(record) }}
        </template>
        <template v-else-if="column.key === 'createTime'">
          {{ record.createTime ? formatDateTime(record.createTime) : '—' }}
        </template>
        <template v-else-if="column.key === 'actions'">
          <Button size="small" @click="openDrawer(record)">View</Button>
        </template>
      </template>
    </Table>

    <div style="display: flex; justify-content: flex-end; margin-top: 16px">
      <Pagination
        v-model:current="page"
        v-model:page-size="pageSize"
        :total="total"
        :show-size-changer="true"
        @change="load"
        @show-size-change="load"
      />
    </div>

    <TicketDrawer
      v-model:open="drawerOpen"
      :ticket-id="selectedId"
      :users="users"
      @changed="load"
    />
  </div>
</template>
