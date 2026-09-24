<script setup lang="ts">
// Right-hand ticket drawer: header chips, status/priority/assignee selects
// that save immediately, the conversation (sanitised message, attachments,
// timeline, reply / internal-note composer), the request details and the
// change history. Its
// toolbar switches the same drawer into an edit form (subject/description) and
// back; delete asks first.
import { computed, ref, shallowRef, watch } from 'vue'
import type { z } from 'zod'
import { useAbility } from '@casl/vue'
import { UiDrawer, UiButton, UiAlert, UiBadge, UiKeyValueTable, UiRecordForm, UiSelect, UiStatusChip, UiTabs, UiTextarea, UiToolbar, useConfirm, useToast, type KeyValue, type SelectOption, type TabItem } from '@go-tangra/ui'
import { zodToFields, type ZodForm } from '@go-tangra/ui/forms'
import { useTickets } from '@/stores/tickets'
import { PRIORITIES, PRIORITY_COLORS, PRIORITY_LABELS, STATUSES, STATUS_COLORS, STATUS_LABELS, ticketUpdateSchema } from '@/schemas'
import type { Comment, HistoryEntry, Ticket } from '@/api/types'
import MessageView from './MessageView.vue'
import { describe } from '@/api/client'
import { tagColor } from '@/views/tags/colors'

const props = defineProps<{ ticketId: string | null }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'changed'): void }>()

const store = useTickets()
const ability = useAbility()
const confirm = useConfirm()
const toast = useToast()
const canUpdate = computed(() => ability.can('update', 'Ticket'))
const canAssign = computed(() => ability.can('assign', 'Ticket'))
const canDelete = computed(() => ability.can('delete', 'Ticket'))

const open = computed(() => props.ticketId !== null)
const ticket = ref<Ticket | null>(null)
const events = ref<HistoryEntry[]>([])
const error = ref('')
const saving = ref(false)
const mode = ref<'view' | 'edit'>('view')
const tab = ref('conversation')
const comments = ref<Comment[]>([])
const composeMode = ref<'reply' | 'note'>('reply')
const draft = ref('')
const posting = ref(false)

async function load(): Promise<void> {
  error.value = ''
  events.value = []
  if (!props.ticketId) {
    ticket.value = null
    return
  }
  if (!store.users.length) void store.assignableUsers()
  if (!store.tags.length) void store.loadTags()
  try {
    ticket.value = await store.get(props.ticketId)
    if (tab.value === 'history') await loadHistory()
    if (tab.value === 'conversation') await loadComments()
  } catch (e) {
    ticket.value = null
    error.value = describe(e)
  }
}
async function loadHistory(): Promise<void> {
  if (!ticket.value) return
  try {
    events.value = await store.history(ticket.value.id)
  } catch (e) {
    error.value = describe(e)
  }
}
watch(() => props.ticketId, () => {
  mode.value = 'view'
  tab.value = 'conversation'
  comments.value = []
  draft.value = ''
  void load()
}, { immediate: true })
watch(tab, (t) => {
  if (t === 'history') void loadHistory()
  if (t === 'conversation') void loadComments()
})

// --- conversation ---
const canComment = computed(() => ability.can('comment', 'Ticket') || ability.can('update', 'Ticket'))
const canReply = computed(() => (ability.can('reply', 'Ticket') || ability.can('update', 'Ticket')) && !!ticket.value?.requester_email)
async function loadComments(): Promise<void> {
  if (!ticket.value) return
  try {
    comments.value = await store.comments(ticket.value.id)
  } catch (e) {
    error.value = describe(e)
  }
}
watch(canReply, (ok) => { if (!ok) composeMode.value = 'note' }, { immediate: true })
async function post(): Promise<void> {
  const t = ticket.value
  const text = draft.value.trim()
  if (!t || !text) return
  error.value = ''
  posting.value = true
  try {
    if (composeMode.value === 'reply') await store.reply(t.id, text)
    else await store.addNote(t.id, text)
    draft.value = ''
    toast.show({ kind: 'success', title: composeMode.value === 'reply' ? 'Reply sent' : 'Note added' })
  } catch (e) {
    error.value = describe(e)
  } finally {
    posting.value = false
  }
  // a reply re-opens resolved tickets and a failed delivery is still recorded
  try {
    ticket.value = await store.get(t.id)
  } catch {
    // keep the current view; the error above (if any) is already shown
  }
  await loadComments()
  emit('changed')
}
async function removeComment(c: Comment): Promise<void> {
  if (!(await confirm.ask({ title: 'Delete this comment?', text: 'It is removed from the conversation permanently.', danger: true, confirmLabel: 'Delete' }))) return
  error.value = ''
  try {
    await store.removeComment(c.id)
    await loadComments()
    emit('changed')
  } catch (e) {
    error.value = describe(e)
  }
}
type Badge = { label: string; color: 'warning' | 'success' | 'info' | 'neutral' | 'primary' }
function badgeOf(c: Comment): Badge {
  if (c.internal) return { label: 'Internal', color: 'warning' }
  if (c.author_kind === 'requester') return { label: 'Incoming', color: 'info' }
  if (c.author_kind === 'system') return { label: 'System', color: 'neutral' }
  return { label: 'Sent', color: 'success' }
}
const authorOf = (c: Comment) => c.author_name || c.author_email || (c.author_kind === 'system' ? 'System' : c.author_kind === 'requester' ? 'Requester' : 'Agent')

// --- immediate saves ---
async function apply(op: () => Promise<Ticket>): Promise<void> {
  error.value = ''
  saving.value = true
  try {
    ticket.value = await op()
    emit('changed')
    if (tab.value === 'history') await loadHistory()
  } catch (e) {
    error.value = describe(e)
  } finally {
    saving.value = false
  }
}
function setStatus(v: unknown): void {
  const t = ticket.value
  if (t && typeof v === 'string' && v && v !== t.status) void apply(() => store.setStatus(t.id, v))
}
function setPriority(v: unknown): void {
  const t = ticket.value
  if (t && typeof v === 'string' && v && v !== t.priority) void apply(() => store.update(t.id, { priority: v as Ticket['priority'] }))
}
function setAssignee(v: unknown): void {
  const t = ticket.value
  const next = typeof v === 'string' ? v : ''
  if (t && next !== (t.assignee_id ?? '')) void apply(() => store.assign(t.id, next || null))
}

// --- tags (the whole set is replaced on every toggle) ---
const tagIds = computed(() => new Set((ticket.value?.tags ?? []).map((t) => t.id)))
function toggleTag(id: string): void {
  const t = ticket.value
  if (!t) return
  const next = new Set(tagIds.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  void apply(() => store.setTags(t.id, [...next]))
}

const statusOptions: SelectOption[] = STATUSES.map((s) => ({ title: STATUS_LABELS[s], value: s }))
const priorityOptions: SelectOption[] = PRIORITIES.map((p) => ({ title: PRIORITY_LABELS[p], value: p }))
const assigneeOptions = computed<SelectOption[]>(() => {
  const opts = store.users.map((u) => ({ title: u.name || u.id, value: u.id }))
  const t = ticket.value
  // keep a current assignee who is no longer assignable visible
  if (t?.assignee_id && !opts.some((o) => o.value === t.assignee_id)) opts.push({ title: t.assignee_name || t.assignee_id, value: t.assignee_id })
  return opts
})

const when = (iso?: string) => (iso ? new Date(iso).toLocaleString() : '')
const details = computed<KeyValue[]>(() => {
  const t = ticket.value
  if (!t) return []
  return [
    { label: 'Requester', value: t.requester_name }, { label: 'Requester email', value: t.requester_email, copyable: true },
    { label: 'Source', value: t.source === 'email' ? 'Email' : 'Manual' }, { label: 'Mailbox', value: t.recipient },
    { label: 'Assignee', value: t.assignee_name || 'Unassigned' }, { label: 'Comments', value: t.comment_count },
    { label: 'Created', value: when(t.created_at) }, { label: 'Updated', value: when(t.updated_at) },
    { label: 'Resolved', value: when(t.resolved_at) }, { label: 'Ticket id', value: t.id, copyable: true },
  ]
})
const tabs = computed<TabItem[]>(() => [
  { key: 'conversation', label: 'Conversation', icon: 'mdi-message-text-outline', ...(comments.value.length ? { count: comments.value.length } : {}) },
  { key: 'details', label: 'Details', icon: 'mdi-information-outline' },
  { key: 'history', label: 'History', icon: 'mdi-history', ...(events.value.length ? { count: events.value.length } : {}) },
])

// --- history rendering ---
const FIELD_LABELS: Record<string, string> = { status: 'Status', priority: 'Priority', assignee: 'Assignee' }
const ACTOR_LABELS: Record<string, string> = { agent: 'an agent', rule: 'a rule', inbound: 'inbound mail', system: 'the system' }
function valueLabel(field: string, v: string): string {
  if (!v) return field === 'assignee' ? 'nobody' : '—'
  if (field === 'status') return STATUS_LABELS[v as keyof typeof STATUS_LABELS] ?? v
  if (field === 'priority') return PRIORITY_LABELS[v as keyof typeof PRIORITY_LABELS] ?? v
  return store.users.find((u) => u.id === v)?.name || v
}

// --- edit subject / description ---
const fields = zodToFields(ticketUpdateSchema, { subject: { required: true, cols: 12 }, description: { cols: 12 } })
const form = shallowRef<ZodForm<z.ZodType> | null>(null)
const initial = computed(() => ({ subject: ticket.value?.subject ?? '', description: ticket.value?.description ?? '' }))
async function submit(v: Record<string, unknown>): Promise<Ticket> {
  const t = ticket.value as Ticket
  return store.update(t.id, { subject: v.subject as string, description: (v.description as string | undefined) ?? '' })
}
function onSaved(t: unknown): void {
  ticket.value = t as Ticket
  mode.value = 'view'
  emit('changed')
}

async function remove(): Promise<void> {
  const t = ticket.value
  if (!t) return
  if (!(await confirm.ask({ title: 'Delete this ticket?', text: 'Its comments, history and attachments are removed permanently.', danger: true, confirmLabel: 'Delete' }))) return
  try {
    await store.remove(t.id)
    toast.show({ kind: 'success', title: 'Ticket deleted' })
    emit('changed')
    emit('close')
  } catch (e) {
    error.value = describe(e)
  }
}

const title = computed(() => (ticket.value ? (mode.value === 'edit' ? 'Edit ticket' : ticket.value.subject) : 'Ticket'))
</script>

<template>
  <UiDrawer :model-value="open" :title="title" size="lg" data-test="ticket-drawer" @update:model-value="emit('close')">
    <UiAlert v-if="error" kind="error" class="mb-4" data-test="ticket-error">{{ error }}</UiAlert>

    <template v-if="ticket && mode === 'view'">
      <div class="mb-3 flex flex-wrap items-center gap-2" data-test="ticket-chips">
        <UiStatusChip :status="ticket.status" :label="STATUS_LABELS[ticket.status]" :colors="STATUS_COLORS" />
        <UiStatusChip :status="ticket.priority" :label="PRIORITY_LABELS[ticket.priority]" :colors="PRIORITY_COLORS" />
        <UiBadge :color="ticket.source === 'email' ? 'info' : 'neutral'">{{ ticket.source }}</UiBadge>
        <UiBadge v-for="tag in ticket.tags ?? []" :key="tag.id" :color="tagColor(tag)" :outline="tag.kind === 'category'" :data-test="'ticket-tag-' + tag.id">{{ tag.name }}</UiBadge>
      </div>
      <div v-if="canUpdate && store.tags.length" class="mb-3 flex flex-wrap items-center gap-1" role="group" aria-label="Tags" data-test="ticket-tags-editor">
        <span class="me-1 text-xs text-base-content/70">Tags</span>
        <UiButton v-for="tag in store.tags" :key="tag.id" size="xs" :color="tagColor(tag)" :variant="tagIds.has(tag.id) ? 'solid' : 'outline'" :icon="tagIds.has(tag.id) ? 'mdi-check' : undefined" :aria-pressed="tagIds.has(tag.id)" :disabled="saving" :data-test="'ticket-tag-toggle-' + tag.id" @click="toggleTag(tag.id)">{{ tag.name }}<template v-if="tag.kind === 'category'"> (category)</template></UiButton>
      </div>

      <UiToolbar class="mb-4 flex-wrap">
        <UiButton v-if="canUpdate" size="sm" variant="soft" icon="mdi-pencil-outline" data-test="ticket-edit" @click="mode = 'edit'">Edit</UiButton>
        <span class="grow" />
        <UiButton v-if="canDelete" size="sm" variant="text" color="error" icon="mdi-delete-outline" data-test="ticket-delete" @click="remove">Delete</UiButton>
      </UiToolbar>

      <div class="mb-4 grid grid-cols-1 gap-2 md:grid-cols-3">
        <UiSelect id="ticket-status" label="Status" :model-value="ticket.status" :options="statusOptions" :clearable="false" :disabled="!canUpdate || saving" size="sm" data-test="ticket-status" @update:model-value="setStatus" />
        <UiSelect id="ticket-priority" label="Priority" :model-value="ticket.priority" :options="priorityOptions" :clearable="false" :disabled="!canUpdate || saving" size="sm" data-test="ticket-priority" @update:model-value="setPriority" />
        <UiSelect id="ticket-assignee" label="Assignee" :model-value="ticket.assignee_id ?? ''" :options="assigneeOptions" placeholder="Unassigned" :disabled="!canAssign || saving" size="sm" data-test="ticket-assignee" @update:model-value="setAssignee" />
      </div>

      <UiTabs v-model="tab" :tabs="tabs" class="mb-4" />
      <section v-if="tab === 'conversation'" data-test="ticket-conversation">
        <MessageView :ticket="ticket" />
        <p v-if="!comments.length" class="mb-4 text-sm text-base-content/70">No comments yet.</p>
        <ol v-else class="mb-4 flex flex-col gap-2" data-test="ticket-timeline">
          <li v-for="c in comments" :key="c.id" class="rounded-box border border-base-300 p-3" :class="c.internal ? 'bg-warning/10' : ''" :data-test="'comment-' + c.id">
            <div class="mb-1 flex flex-wrap items-center gap-2 text-xs">
              <UiBadge :color="badgeOf(c).color" size="xs">{{ badgeOf(c).label }}</UiBadge>
              <UiBadge v-if="c.delivery === 'failed'" color="error" size="xs" data-test="comment-delivery-failed">Delivery failed</UiBadge>
              <span class="font-medium">{{ authorOf(c) }}</span>
              <span class="grow text-end text-base-content/70">{{ when(c.created_at) }}</span>
              <UiButton v-if="canUpdate" size="xs" variant="text" color="error" icon="mdi-delete-outline" icon-only label="Delete comment" :data-test="'comment-delete-' + c.id" @click="removeComment(c)" />
            </div>
            <p class="text-sm whitespace-pre-wrap break-words">{{ c.body }}</p>
          </li>
        </ol>
        <div v-if="canComment" class="rounded-box border border-base-300 p-3" data-test="ticket-composer">
          <div class="mb-2 flex flex-wrap items-center gap-2" role="group" aria-label="Message type">
            <UiButton size="xs" :variant="composeMode === 'reply' ? 'soft' : 'text'" icon="mdi-email-outline" :disabled="!canReply" :aria-pressed="composeMode === 'reply'" data-test="compose-reply" @click="composeMode = 'reply'">Reply</UiButton>
            <UiButton size="xs" :variant="composeMode === 'note' ? 'soft' : 'text'" icon="mdi-lock-outline" :aria-pressed="composeMode === 'note'" data-test="compose-note" @click="composeMode = 'note'">Internal note</UiButton>
            <span v-if="!ticket.requester_email" class="text-xs text-base-content/70" data-test="compose-no-requester">Replies need a requester email address.</span>
          </div>
          <UiTextarea id="ticket-compose" v-model="draft" :label="composeMode === 'reply' ? 'Reply to the requester (emailed)' : 'Internal note (agents only, never emailed)'" :rows="4" :disabled="posting" />
          <div class="mt-2 flex justify-end">
            <UiButton size="sm" :icon="composeMode === 'reply' ? 'mdi-email-outline' : 'mdi-lock-outline'" :loading="posting" :disabled="!draft.trim()" data-test="compose-submit" @click="post">{{ composeMode === 'reply' ? 'Send reply' : 'Add note' }}</UiButton>
          </div>
        </div>
      </section>
      <section v-else-if="tab === 'details'" data-test="ticket-details">
        <p v-if="ticket.description" class="mb-4 text-sm whitespace-pre-wrap break-words">{{ ticket.description }}</p>
        <p v-else class="mb-4 text-sm text-base-content/70">No description.</p>
        <UiKeyValueTable :items="details" :columns="2" />
      </section>
      <section v-else data-test="ticket-history">
        <p v-if="!events.length" class="text-sm text-base-content/70">No changes recorded yet.</p>
        <ol v-else class="divide-y divide-base-300 rounded-box border border-base-300 text-sm">
          <li v-for="h in [...events].reverse()" :key="h.id" class="flex flex-wrap items-baseline gap-x-2 px-3 py-2" :data-test="'history-' + h.id">
            <span class="font-medium">{{ FIELD_LABELS[h.field] ?? h.field }}</span>
            <span>{{ valueLabel(h.field, h.old_value) }} → {{ valueLabel(h.field, h.new_value) }}</span>
            <span class="grow text-end text-xs text-base-content/70">by {{ ACTOR_LABELS[h.actor_kind] ?? h.actor_kind }} · {{ when(h.created_at) }}</span>
          </li>
        </ol>
      </section>
    </template>

    <UiRecordForm v-else-if="ticket && mode === 'edit'" :key="'edit-' + ticket.id" :schema="ticketUpdateSchema" :fields="fields" :initial="initial" :submit="submit" @ready="form = $event" @saved="onSaved" />

    <template v-if="ticket && mode === 'edit'" #actions>
      <UiButton variant="text" color="neutral" icon="mdi-arrow-left" @click="mode = 'view'">Back</UiButton>
      <UiButton :loading="form?.submitting.value ?? false" data-test="ticket-save" @click="form?.submit()">Save</UiButton>
    </template>
  </UiDrawer>
</template>
