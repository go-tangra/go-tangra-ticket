<script setup lang="ts">
// Ticket queue: a filter bar (text, status, priority, assignee incl.
// unassigned, tag), a paged table (newest first) and a right-hand drawer that
// carries everything done to one ticket. "New ticket" opens a record drawer
// that closes on save and then shows the created ticket.
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useAbility } from '@casl/vue'
import { UiPage, UiAlert, UiCard, UiForm, UiInput, UiSelect, UiButton, UiBadge, UiDataTable, UiPagination, UiStatusChip, UiRecordDrawer, UiLiveIndicator, type Column, type SelectOption } from '@freya/ui'
import { useZodForm, zodToFields } from '@freya/ui/forms'
import { useTickets } from '@/stores/tickets'
import { coalesce, useLive } from '@/stores/live'
import { PRIORITIES, PRIORITY_COLORS, PRIORITY_LABELS, STATUSES, STATUS_COLORS, STATUS_LABELS, ticketFilterSchema, ticketSchema } from '@/schemas'
import type { Ticket, TicketCreate } from '@/api/types'
import TicketDrawer from './drawer.vue'
import { tagColor } from '@/views/tags/colors'

const store = useTickets()
const ability = useAbility()
const canCreate = computed(() => ability.can('create', 'Ticket'))

const statusOptions: SelectOption[] = STATUSES.map((s) => ({ title: STATUS_LABELS[s], value: s }))
const priorityOptions: SelectOption[] = PRIORITIES.map((p) => ({ title: PRIORITY_LABELS[p], value: p }))
const userOptions = computed<SelectOption[]>(() => store.users.map((u) => ({ title: u.name || u.id, value: u.id })))
const assigneeOptions = computed<SelectOption[]>(() => [{ title: 'Unassigned', value: 'none' }, ...userOptions.value])
const tagOptions = computed<SelectOption[]>(() => store.tags.map((t) => ({ title: t.kind === 'category' ? `${t.name} (category)` : t.name, value: t.id })))

const filter = useZodForm(ticketFilterSchema, {
  initial: { query: '' },
  onSubmit: (f) => store.list({ query: f.query || undefined, status: f.status, priority: f.priority, assignee_id: f.assignee_id, tag_id: f.tag_id }),
})
const reload = () => void filter.submit()

// Live refresh: any ticket event of the tenant (created, assigned, status,
// comment, requester reply) reloads the current page, coalesced per burst.
const live = useLive()
const refresh = coalesce(() => void store.reload())
let release: (() => void) | null = null
let off: (() => void) | null = null

onMounted(() => {
  void filter.submit()
  void store.assignableUsers()
  void store.loadTags()
  release = live.connect()
  off = live.on(() => refresh.trigger())
})
onUnmounted(() => {
  off?.()
  release?.()
  refresh.cancel()
})

// --- paging ---
const pages = computed(() => Math.max(1, Math.ceil(store.total / store.pageSize)))
const pageLabel = computed(() => `Page ${store.page} of ${pages.value} · ${store.total} ticket${store.total === 1 ? '' : 's'}`)
const goTo = (p: number) => void store.list(store.filter, p)

// --- drawer ---
const selected = ref<string | null>(null)
const openTicket = (id: string) => (selected.value = id)

// --- new ticket ---
const creating = ref(false)
const createFields = computed(() =>
  zodToFields(ticketSchema, {
    subject: { required: true, cols: 12 },
    priority: { type: 'select', cols: 6, options: priorityOptions },
    assignee_id: { label: 'Assignee', type: 'select', cols: 6, options: userOptions.value },
    requester_name: { label: 'Requester name', cols: 6 },
    requester_email: { label: 'Requester email', cols: 6, placeholder: 'name@example.org' },
    description: { cols: 12 },
  }),
)
const createTicket = (v: Record<string, unknown>) => store.create(v as unknown as TicketCreate)
function onCreated(t: unknown): void {
  selected.value = (t as Ticket).id
}

const when = (iso?: string) => (iso ? new Date(iso).toLocaleString() : '')
const columns: Column<Ticket>[] = [
  { key: 'subject', label: 'Subject' },
  { key: 'status', label: 'Status', width: 'sm' },
  { key: 'priority', label: 'Priority', width: 'sm' },
  { key: 'assignee_name', label: 'Assignee', format: (t) => t.assignee_name || 'Unassigned' },
  { key: 'requester_name', label: 'Requester', hideOnStack: true, format: (t) => t.requester_name || t.requester_email || '' },
  { key: 'created_at', label: 'Created', hideOnStack: true, format: (t) => when(t.created_at) },
]
</script>

<template>
  <UiPage title="Tickets">
    <template #badges><UiLiveIndicator :connected="live.connected" /></template>
    <template #actions>
      <UiButton v-if="canCreate" icon="mdi-plus" data-test="ticket-new" @click="creating = true">New ticket</UiButton>
      <UiButton variant="text" icon="mdi-refresh" icon-only label="Refresh" @click="store.reload()" />
    </template>
    <div class="flex min-w-0 flex-col gap-3">
      <UiCard>
        <UiForm :form="filter">
          <div class="grid grid-cols-2 gap-2 md:grid-cols-12 md:items-end" data-test="ticket-filters">
            <div class="col-span-2 md:col-span-4"><UiInput v-bind="filter.field('query')" label="Search (subject / requester)" type="search" size="sm" @enter="reload" /></div>
            <div class="md:col-span-2"><UiSelect v-bind="filter.field('status')" label="Status" :options="statusOptions" placeholder="Any" size="sm" @update:model-value="reload" /></div>
            <div class="md:col-span-2"><UiSelect v-bind="filter.field('priority')" label="Priority" :options="priorityOptions" placeholder="Any" size="sm" @update:model-value="reload" /></div>
            <div class="md:col-span-2"><UiSelect v-bind="filter.field('assignee_id')" label="Assignee" :options="assigneeOptions" placeholder="Anyone" size="sm" @update:model-value="reload" /></div>
            <div class="md:col-span-2"><UiSelect v-bind="filter.field('tag_id')" label="Tag" :options="tagOptions" placeholder="Any" size="sm" @update:model-value="reload" /></div>
          </div>
        </UiForm>
      </UiCard>
      <UiAlert v-if="store.error" kind="error">{{ store.error }}</UiAlert>
      <UiCard :padded="false">
        <UiDataTable :items="store.items" :columns="columns" :loading="store.loading" caption="Tickets — select one to work on it" empty-title="No tickets match" clickable :row-attrs="(t) => ({ 'data-test': 'ticket-row-' + t.id })" data-test="tickets-table" @row-click="openTicket($event.id)">
          <template #cell-subject="{ row }">
            <span class="font-medium">{{ row.subject }}</span>
            <UiBadge v-for="tag in row.tags ?? []" :key="tag.id" size="xs" class="ms-1" :color="tagColor(tag)" :outline="tag.kind === 'category'">{{ tag.name }}</UiBadge>
            <UiBadge v-if="row.comment_count" size="xs" color="neutral" class="ms-1">{{ row.comment_count }} comments</UiBadge>
          </template>
          <template #cell-status="{ row }"><UiStatusChip :status="row.status" :label="STATUS_LABELS[row.status]" :colors="STATUS_COLORS" /></template>
          <template #cell-priority="{ row }"><UiStatusChip :status="row.priority" :label="PRIORITY_LABELS[row.priority]" :colors="PRIORITY_COLORS" /></template>
        </UiDataTable>
      </UiCard>
      <div class="flex justify-end">
        <UiPagination :has-prev="store.page > 1" :has-next="store.page < pages" :label="pageLabel" data-test="ticket-pager" @prev="goTo(store.page - 1)" @next="goTo(store.page + 1)" />
      </div>
    </div>

    <UiRecordDrawer v-model="creating" title="New ticket" :schema="ticketSchema" :fields="createFields" :initial="{ priority: 'normal' }" :submit="createTicket" size="lg" save-label="Create" close-on-save data-test="ticket-create" @saved="onCreated" />
    <TicketDrawer :ticket-id="selected" @close="selected = null" @changed="store.reload()" />
  </UiPage>
</template>
