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
  TicketTag,
} from '../../api/client';
import { useTagStore } from '../../stores/tag.state';
import { useTicketStore } from '../../stores/ticket.state';
import { tagColor } from '../rules/helpers';
import EmailViewer from './EmailViewer.vue';
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
const tagStore = useTagStore();

const loading = ref(false);
const ticket = ref<Ticket | null>(null);
const comments = ref<TicketComment[]>([]);

const allTags = ref<TicketTag[]>([]);
const editingTags = ref(false);
const selectedTagIds = ref<string[]>([]);
const savingTags = ref(false);

const assignee = ref<number | undefined>(undefined);
const status = ref<TicketStatus | undefined>(undefined);

const newComment = ref('');
const internal = ref(false);
const posting = ref(false);

function close() {
  emit('update:open', false);
}

function formatSize(bytes?: number): string {
  const n = bytes ?? 0;
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

async function loadAll() {
  if (!props.ticketId) return;
  loading.value = true;
  try {
    ticket.value = await store.getTicket(props.ticketId);
    assignee.value = ticket.value.assigneeId || undefined;
    status.value = ticket.value.status;
    editingTags.value = false;
    comments.value = await store.listComments(props.ticketId);
    allTags.value = await tagStore.listTags();
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

const tagOptions = () =>
  allTags.value.map((t) => ({
    value: t.id,
    label: `${t.kind === 'CATEGORY' ? '▣ ' : ''}${t.name}`,
  }));

function startEditTags() {
  selectedTagIds.value = (ticket.value?.tags ?? [])
    .map((t) => t.id)
    .filter((id): id is string => !!id);
  editingTags.value = true;
}

async function saveTags() {
  if (!ticket.value) return;
  savingTags.value = true;
  try {
    await tagStore.setTicketTags(props.ticketId, selectedTagIds.value);
    ticket.value = await store.getTicket(props.ticketId);
    editingTags.value = false;
    antMessage.success($t('ticket.page.ticket.tagsSuccess'));
    emit('changed');
  } catch (e: any) {
    antMessage.error(e?.message ?? 'Failed to update tags');
  } finally {
    savingTags.value = false;
  }
}
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

        <div style="margin-bottom: 12px">
          <div style="display: flex; align-items: center; gap: 8px; margin-bottom: 6px">
            <span style="font-weight: 500">{{ $t('ticket.page.ticket.tags') }}:</span>
            <template v-if="!editingTags">
              <Tag
                v-for="t in ticket.tags || []"
                :key="t.id"
                :color="tagColor(t.color, t.name)"
              >
                {{ t.name }}
              </Tag>
              <span v-if="!ticket.tags || !ticket.tags.length" style="color: #888">—</span>
              <Button type="link" size="small" @click="startEditTags">
                {{ $t('ticket.action.edit') }}
              </Button>
            </template>
          </div>
          <div v-if="editingTags" style="display: flex; gap: 8px; align-items: center">
            <Select
              v-model:value="selectedTagIds"
              :options="tagOptions()"
              mode="multiple"
              style="flex: 1"
              option-filter-prop="label"
              :placeholder="$t('ticket.page.ticket.tagsPlaceholder')"
            />
            <Button type="primary" size="small" :loading="savingTags" @click="saveTags">
              {{ $t('ticket.action.save') }}
            </Button>
            <Button size="small" @click="editingTags = false">
              {{ $t('ticket.action.cancel') }}
            </Button>
          </div>
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

        <div style="margin-bottom: 16px">
          <EmailViewer
            :html-content="ticket.bodyHtml"
            :text-content="ticket.description"
          />
        </div>

        <div
          v-if="ticket.attachments && ticket.attachments.length"
          style="margin-bottom: 16px"
        >
          <h3 style="font-weight: 600; margin-bottom: 8px">
            {{ $t('ticket.page.ticket.attachments') }} ({{ ticket.attachments.length }})
          </h3>
          <div
            v-for="a in ticket.attachments"
            :key="a.id"
            style="display: flex; align-items: center; gap: 8px; margin-bottom: 4px"
          >
            <span>📎</span>
            <a :href="a.downloadUrl" target="_blank" rel="noopener noreferrer">
              {{ a.filename || 'attachment' }}
            </a>
            <span style="color: #888; font-size: 12px">{{ formatSize(a.size) }}</span>
            <Tag v-if="a.inline" color="blue" style="margin-left: 4px">inline</Tag>
          </div>
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
