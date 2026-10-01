<script setup lang="ts">
// Ticket queue: a filter bar (text, status, priority, assignee incl.
// unassigned, tag), a server-paged and server-sorted table (newest first by
// default; page/size/sort live in the URL) and a right-hand drawer that carries
// everything done to one ticket. "New ticket" opens a record drawer that closes
// on save and then shows the created ticket.
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useAbility } from '@casl/vue'
import { UiPage, UiAlert, UiCard, UiForm, UiInput, UiSelect, UiButton, UiBadge, UiDataTable, UiStatusChip, UiRecordDrawer, UiLiveIndicator, useListQuery, type Column, type SelectOption } from '@go-tangra/ui'
import { useZodForm, zodToFields } from '@go-tangra/ui/forms'
import { TICKET_SORTS, useTickets } from '@/stores/tickets'
import { coalesce, useLive } from '@/stores/live'
import { PRIORITIES, PRIORITY_COLORS, PRIORITY_LABELS, STATUSES, STATUS_COLORS, STATUS_LABELS, ticketFilterSchema, ticketSchema } from '@/schemas'
import type { Ticket, TicketCreate, TicketFilter } from '@/api/types'
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

// Page, size and sort (URL-backed); only the server's sortable fields sort.
const lq = useListQuery('tickets', { sortable: [...TICKET_SORTS], defaultSort: { key: 'created_at', dir: 'desc' } })
const filters = ref<TicketFilter>({})
async function load(): Promise<void> {
  const page = await store.list(filters.value, lq.query.value)
  if (page !== null) lq.clampTo(page)
}
watch(lq.query, () => void load())

// A changed filter starts again at page 1.
const filter = useZodForm(ticketFilterSchema, {
  initial: { query: '' },
  onSubmit: (f) => {
    filters.value = { query: f.query || undefined, status: f.status, priority: f.priority, assignee_id: f.assignee_id, tag_id: f.tag_id }
    if (lq.page.value !== 1) lq.resetPage()
    else void load()
  },
})
const reload = () => void filter.submit()

// Live refresh: any ticket event of the tenant (created, assigned, status,
// comment, requester reply) reloads the current page with its sort, coalesced
// per burst.
const live = useLive()
const refresh = coalesce(() => void load())
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
// Sortable columns are exactly the server's sort fields (status and priority
// sort by workflow/urgency rank; unassigned tickets sort last).
const columns: Column<Ticket>[] = [
  { key: 'subject', label: 'Subject', sortable: true },
  { key: 'status', label: 'Status', width: 'sm', sortable: true },
  { key: 'priority', label: 'Priority', width: 'sm', sortable: true, defaultDir: 'desc' },
  { key: 'assignee', label: 'Assignee', sortable: true, format: (t) => t.assignee_name || 'Unassigned' },
  { key: 'requester_name', label: 'Requester', hideOnStack: true, format: (t) => t.requester_name || t.requester_email || '' },
  { key: 'created_at', label: 'Created', hideOnStack: true, sortable: true, defaultDir: 'desc', format: (t) => when(t.created_at) },
  { key: 'updated_at', label: 'Updated', hideOnStack: true, sortable: true, defaultDir: 'desc', format: (t) => when(t.updated_at) },
]
</script>

<template>
  <UiPage title="Tickets">
    <template #badges><UiLiveIndicator :connected="live.connected" /></template>
    <template #actions>
      <UiButton v-if="canCreate" icon="mdi-plus" data-test="ticket-new" @click="creating = true">New ticket</UiButton>
      <UiButton variant="text" icon="mdi-refresh" icon-only label="Refresh" @click="load()" />
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
        <UiDataTable :items="store.items" :columns="columns" :loading="store.loading" :total="store.total" :page="lq.page.value" :page-size="lq.pageSize.value" :sort="lq.sort.value" row-key="id" caption="Tickets — select one to work on it" empty-title="No tickets match" clickable :row-attrs="(t) => ({ 'data-test': 'ticket-row-' + t.id })" data-test="tickets-table" @row-click="openTicket($event.id)" @update:page="lq.setPage" @update:page-size="lq.setPageSize" @update:sort="lq.setSort">
          <template #cell-subject="{ row }">
            <span class="font-medium">{{ row.subject }}</span>
            <UiBadge v-for="tag in row.tags ?? []" :key="tag.id" size="xs" class="ms-1" :color="tagColor(tag)" :outline="tag.kind === 'category'">{{ tag.name }}</UiBadge>
            <UiBadge v-if="row.comment_count" size="xs" color="neutral" class="ms-1">{{ row.comment_count }} comments</UiBadge>
          </template>
          <template #cell-status="{ row }"><UiStatusChip :status="row.status" :label="STATUS_LABELS[row.status]" :colors="STATUS_COLORS" /></template>
          <template #cell-priority="{ row }"><UiStatusChip :status="row.priority" :label="PRIORITY_LABELS[row.priority]" :colors="PRIORITY_COLORS" /></template>
        </UiDataTable>
      </UiCard>
    </div>

    <UiRecordDrawer v-model="creating" title="New ticket" :schema="ticketSchema" :fields="createFields" :initial="{ priority: 'normal' }" :submit="createTicket" size="lg" save-label="Create" close-on-save data-test="ticket-create" @saved="onCreated" />
    <TicketDrawer :ticket-id="selected" @close="selected = null" @changed="load()" />
  </UiPage>
</template>
