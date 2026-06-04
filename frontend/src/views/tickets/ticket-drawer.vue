<script setup lang="ts">
import { ref, watch } from 'vue';

import {
  Button,
  Checkbox,
  Divider,
  Drawer,
  message as antMessage,
  Select,
  Spin,
  Tag,
  Textarea,
} from 'ant-design-vue';

import { $t } from 'shell/locales';

import type {
  AssignableUser,
  Ticket,
  TicketComment,
  TicketStatus,
} from '../../api/services';
import { useTicketStore } from '../../stores/ticket.state';
import { humanizeEnum, priorityColor, statusColor, statusOptions } from './helpers';

const props = defineProps<{
  open: boolean;
  ticketId: string;
  users: AssignableUser[];
}>();

const emit = defineEmits<{
  (e: 'update:open', v: boolean): void;
  (e: 'changed'): void;
}>();

const store = useTicketStore();

const loading = ref(false);
const ticket = ref<Ticket | null>(null);
const comments = ref<TicketComment[]>([]);

const assignee = ref<number | undefined>(undefined);
const status = ref<TicketStatus | undefined>(undefined);

const newComment = ref('');
const internal = ref(false);
const posting = ref(false);

function close() {
  emit('update:open', false);
}

async function loadAll() {
  if (!props.ticketId) return;
  loading.value = true;
  try {
    ticket.value = await store.getTicket(props.ticketId);
    assignee.value = ticket.value.assigneeId || undefined;
    status.value = ticket.value.status;
    comments.value = await store.listComments(props.ticketId);
  } catch (e: any) {
    antMessage.error(e?.message ?? 'Load failed');
  } finally {
    loading.value = false;
  }
}

watch(
  () => [props.open, props.ticketId],
  ([isOpen]) => {
    if (isOpen) loadAll();
  },
);

async function onAssign(value: number | undefined) {
  try {
    ticket.value = await store.assignTicket(props.ticketId, value ?? 0);
    antMessage.success($t('ticket.page.ticket.assignSuccess'));
    emit('changed');
  } catch (e: any) {
    antMessage.error(e?.message ?? 'Assign failed');
  }
}

async function onStatus(value: TicketStatus) {
  try {
    ticket.value = await store.updateStatus(props.ticketId, value);
    antMessage.success($t('ticket.page.ticket.statusSuccess'));
    emit('changed');
  } catch (e: any) {
    antMessage.error(e?.message ?? 'Status update failed');
  }
}

async function postComment() {
  if (!newComment.value.trim()) return;
  posting.value = true;
  try {
    await store.addComment(props.ticketId, newComment.value.trim(), internal.value);
    newComment.value = '';
    internal.value = false;
    comments.value = await store.listComments(props.ticketId);
    antMessage.success($t('ticket.page.ticket.commentSuccess'));
  } catch (e: any) {
    antMessage.error(e?.message ?? 'Comment failed');
  } finally {
    posting.value = false;
  }
}

const assigneeOptions = () => [
  { value: 0, label: $t('ticket.page.ticket.unassigned') },
  ...props.users.map((u) => ({ value: u.id, label: u.name || u.username })),
];
</script>

<template>
  <Drawer
    :open="open"
    :title="$t('ticket.page.ticket.detail')"
    width="640"
    @close="close"
  >
    <Spin :spinning="loading">
      <div v-if="ticket">
        <h2 style="margin: 0 0 8px; font-size: 16px; font-weight: 600">
          {{ ticket.subject }}
        </h2>
        <div style="margin-bottom: 12px">
          <Tag :color="statusColor(ticket.status)">{{ humanizeEnum(ticket.status) }}</Tag>
          <Tag :color="priorityColor(ticket.priority)">{{ humanizeEnum(ticket.priority) }}</Tag>
          <Tag v-if="ticket.source">{{ ticket.source }}</Tag>
        </div>

        <p>
          <b>{{ $t('ticket.page.ticket.requester') }}:</b>
          {{ ticket.requesterName || '—' }}
          <span v-if="ticket.requesterEmail">&lt;{{ ticket.requesterEmail }}&gt;</span>
        </p>
        <p v-if="ticket.recipient"><b>To:</b> {{ ticket.recipient }}</p>
        <p>
          <b>{{ $t('ticket.page.ticket.created') }}:</b>
          {{ ticket.createTime ? new Date(ticket.createTime).toLocaleString() : '—' }}
        </p>

        <div style="display: flex; gap: 16px; margin: 12px 0">
          <div style="flex: 1">
            <div style="margin-bottom: 4px; font-weight: 500">
              {{ $t('ticket.page.ticket.changeStatus') }}
            </div>
            <Select
              v-model:value="status"
              :options="statusOptions"
              style="width: 100%"
              @change="(v: any) => onStatus(v)"
            />
          </div>
          <div style="flex: 1">
            <div style="margin-bottom: 4px; font-weight: 500">
              {{ $t('ticket.page.ticket.assignee') }}
            </div>
            <Select
              v-model:value="assignee"
              :options="assigneeOptions()"
              style="width: 100%"
              show-search
              option-filter-prop="label"
              @change="(v: any) => onAssign(v)"
            />
          </div>
        </div>

        <Divider style="margin: 12px 0" />

        <div style="white-space: pre-wrap; word-break: break-word; margin-bottom: 16px">
          {{ ticket.description || '(no body)' }}
        </div>

        <Divider style="margin: 12px 0" />

        <h3 style="font-weight: 600; margin-bottom: 8px">
          {{ $t('ticket.page.ticket.comments') }} ({{ comments.length }})
        </h3>

        <div v-if="comments.length" style="margin-bottom: 12px">
          <div
            v-for="c in comments"
            :key="c.id"
            style="border-left: 3px solid var(--border, #e5e7eb); padding: 4px 0 4px 10px; margin-bottom: 10px"
          >
            <div style="font-size: 12px; color: #888">
              <b>{{ c.authorName || c.authorEmail || ('#' + c.authorId) }}</b>
              · {{ c.createTime ? new Date(c.createTime).toLocaleString() : '' }}
              <Tag v-if="c.internal" color="orange" style="margin-left: 6px">internal</Tag>
            </div>
            <div style="white-space: pre-wrap; word-break: break-word">{{ c.body }}</div>
          </div>
        </div>
        <p v-else style="color: #888">{{ $t('ticket.page.ticket.noComments') }}</p>

        <Textarea
          v-model:value="newComment"
          :rows="3"
          :placeholder="$t('ticket.page.ticket.addComment')"
        />
        <div style="display: flex; align-items: center; gap: 12px; margin-top: 8px">
          <Checkbox v-model:checked="internal">{{ $t('ticket.page.ticket.internalNote') }}</Checkbox>
          <Button type="primary" :loading="posting" @click="postComment">
            {{ $t('ticket.page.ticket.addComment') }}
          </Button>
        </div>
      </div>
    </Spin>
  </Drawer>
</template>
