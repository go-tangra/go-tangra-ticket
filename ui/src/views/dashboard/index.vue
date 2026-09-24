<script setup lang="ts">
// Ticket dashboard (FR-017): open work by status, priority and assignee,
// unassigned open tickets and created vs resolved per day over a selectable
// window; refreshed live from the tenant's ticket stream. Tenant backup
// (export / import) for holders of backup:manage.
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useAbility } from '@casl/vue'
import { UiPage, UiAlert, UiCard, UiButton, UiSelect, UiStatGrid, UiStatTile, UiBarList, UiLiveIndicator, UiDataTable, UiForm, UiFilePicker, UiEmptyState, type BarItem, type Column, type SelectOption } from '@go-tangra/ui'
import { useZodForm } from '@go-tangra/ui/forms'
import { useStats } from '@/stores/stats'
import { coalesce, useLive } from '@/stores/live'
import { describe } from '@/api/client'
import { BACKUP_MODES, BACKUP_MODE_LABELS, PRIORITIES, PRIORITY_COLORS, PRIORITY_LABELS, STATS_WINDOWS, STATUSES, STATUS_COLORS, STATUS_LABELS, backupImportSchema } from '@/schemas'

const stats = useStats()
const live = useLive()
const ability = useAbility()
const canBackup = computed(() => ability.can('manage', 'TicketBackup'))

const refresh = coalesce(() => void stats.load())
let release: (() => void) | null = null
let off: (() => void) | null = null
onMounted(() => {
  void stats.load()
  release = live.connect()
  off = live.on(() => refresh.trigger())
})
onUnmounted(() => {
  off?.()
  release?.()
  refresh.cancel()
})

const s = computed(() => stats.snapshot)
const windowOptions: SelectOption[] = STATS_WINDOWS.map((d) => ({ title: d === 365 ? 'Last year' : `Last ${d} days`, value: String(d) }))
const setWindow = (v: unknown) => void stats.load(Number(v) || 30)

const openWork = computed(() => (s.value ? (s.value.by_status.open ?? 0) + (s.value.by_status.in_progress ?? 0) + (s.value.by_status.pending ?? 0) : 0))
const sum = (xs?: { count: number }[]) => (xs ?? []).reduce((n, x) => n + x.count, 0)
const createdTotal = computed(() => sum(s.value?.created_per_day))
const resolvedTotal = computed(() => sum(s.value?.resolved_per_day))

const byStatus = computed<BarItem[]>(() => (s.value ? STATUSES.map((k) => ({ label: STATUS_LABELS[k], value: s.value!.by_status[k] ?? 0, color: STATUS_COLORS[k] })).filter((b) => b.value > 0) : []))
const byPriority = computed<BarItem[]>(() => (s.value ? PRIORITIES.map((k) => ({ label: PRIORITY_LABELS[k], value: s.value!.by_priority[k] ?? 0, color: PRIORITY_COLORS[k] })).filter((b) => b.value > 0) : []))
const byAssignee = computed<BarItem[]>(() => {
  if (!s.value) return []
  const rows: BarItem[] = s.value.by_assignee.map((a) => ({ label: a.assignee_name || a.assignee_id, value: a.count, color: 'primary' }))
  if (s.value.unassigned_open > 0) rows.push({ label: 'Unassigned', value: s.value.unassigned_open, color: 'warning' })
  return rows
})

// Created vs resolved: the active days of the window, newest first.
type DayRow = { day: string; created: number; resolved: number }
const perDay = computed<DayRow[]>(() => {
  if (!s.value) return []
  const resolved = new Map(s.value.resolved_per_day.map((d) => [d.day, d.count]))
  return s.value.created_per_day
    .map((d) => ({ day: d.day, created: d.count, resolved: resolved.get(d.day) ?? 0 }))
    .filter((r) => r.created > 0 || r.resolved > 0)
    .reverse()
})
const dayColumns: Column<DayRow>[] = [
  { key: 'day', label: 'Day' },
  { key: 'created', label: 'Created', width: 'sm' },
  { key: 'resolved', label: 'Resolved', width: 'sm' },
  { key: 'net', label: 'Net', width: 'sm', format: (r) => (r.created - r.resolved > 0 ? '+' : '') + String(r.created - r.resolved) },
]

// --- backup ---
const backupError = ref('')
const importResult = ref('')
async function exportBackup(): Promise<void> {
  backupError.value = ''
  try {
    const b = await stats.exportBackup()
    const blob = new Blob([JSON.stringify(b, null, 2)], { type: 'application/json' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = 'ticket-backup-' + new Date().toISOString().slice(0, 10) + '.json'
    a.click()
    URL.revokeObjectURL(a.href)
  } catch (e) {
    backupError.value = describe(e)
  }
}
// FileReader works in every browser (and jsdom), unlike Blob.text() in older engines.
function readText(f: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => resolve(String(r.result ?? ''))
    r.onerror = () => reject(r.error ?? new Error('read failed'))
    r.readAsText(f)
  })
}
const counts = (m: Record<string, number>) => Object.entries(m).map(([k, v]) => `${k.replace('_', ' ')} ${v}`).join(', ') || 'nothing'
const importForm = useZodForm(backupImportSchema, {
  initial: { mode: 'skip' },
  onSubmit: async ({ file, mode }) => {
    let parsed: unknown
    try {
      parsed = JSON.parse(await readText(file))
    } catch {
      importForm.setFieldError('file', 'The file is not valid JSON.')
      throw new Error('invalid json')
    }
    const res = await stats.importBackup(parsed, mode)
    importResult.value = `Imported ${counts(res.imported)}; skipped ${counts(res.skipped)}.`
  },
  onSuccess: async () => {
    importForm.reset({ mode: 'skip' })
    await stats.load()
  },
})
const modeOptions: SelectOption[] = BACKUP_MODES.map((m) => ({ title: BACKUP_MODE_LABELS[m], value: m }))
</script>

<template>
  <UiPage title="Ticket dashboard">
    <template #badges><UiLiveIndicator :connected="live.connected" /></template>
    <template #actions>
      <div class="w-40"><UiSelect id="stats-window" label="Window" :model-value="String(stats.days)" :options="windowOptions" :clearable="false" size="sm" sr-only-label data-test="stats-window" @update:model-value="setWindow" /></div>
      <UiButton variant="text" icon="mdi-refresh" icon-only label="Refresh" @click="stats.load()" />
    </template>
    <UiAlert v-if="stats.error" kind="error" class="mb-3">{{ stats.error }}</UiAlert>
    <UiStatGrid class="mb-4" :cols="4" data-test="stats-tiles">
      <UiStatTile title="Open work" :value="openWork" icon="mdi-inbox-outline" color="primary" :subtitle="`${s?.total ?? 0} tickets in total`" />
      <UiStatTile title="Unassigned open" :value="s?.unassigned_open ?? 0" icon="mdi-alert-circle-outline" color="warning" subtitle="waiting for an owner" />
      <UiStatTile title="Created" :value="createdTotal" icon="mdi-ticket-outline" color="info" :subtitle="`last ${stats.days} days`" />
      <UiStatTile title="Resolved" :value="resolvedTotal" icon="mdi-check-circle-outline" color="success" :subtitle="`last ${stats.days} days`" />
    </UiStatGrid>
    <div class="grid grid-cols-1 gap-4 lg:grid-cols-3">
      <UiCard title="By status" data-test="stats-status"><UiBarList :items="byStatus" empty-title="No tickets yet" /></UiCard>
      <UiCard title="By priority" data-test="stats-priority"><UiBarList :items="byPriority" empty-title="No tickets yet" /></UiCard>
      <UiCard title="Open work per assignee" data-test="stats-assignee"><UiBarList :items="byAssignee" empty-title="No open tickets" /></UiCard>
    </div>
    <div class="mt-4 grid grid-cols-1 gap-4 lg:grid-cols-12">
      <UiCard title="Created vs resolved per day" :padded="false" class="lg:col-span-7" data-test="stats-per-day">
        <UiEmptyState v-if="perDay.length === 0" title="No activity in this window" />
        <UiDataTable v-else :items="perDay" :columns="dayColumns" caption="Tickets created and resolved per day (UTC), newest first" />
      </UiCard>
      <UiCard v-if="canBackup" title="Backup" class="lg:col-span-5" data-test="backup">
        <UiAlert v-if="backupError" kind="error" class="mb-3">{{ backupError }}</UiAlert>
        <UiButton variant="soft" icon="mdi-download" class="mb-3" data-test="backup-export" @click="exportBackup">Export tenant data</UiButton>
        <UiForm :form="importForm">
          <div class="grid grid-cols-1 gap-2 md:grid-cols-12 md:items-end">
            <div class="md:col-span-12"><UiFilePicker v-bind="importForm.field('file')" label="Backup file (.json)" accept="application/json" /></div>
            <div class="md:col-span-8"><UiSelect v-bind="importForm.field('mode')" label="On conflict" :options="modeOptions" :clearable="false" /></div>
            <div class="md:col-span-4"><UiButton type="submit" icon="mdi-upload" block :loading="importForm.submitting.value">Import</UiButton></div>
          </div>
        </UiForm>
        <p v-if="importResult" class="mt-2 text-sm" data-test="backup-result">{{ importResult }}</p>
      </UiCard>
    </div>
  </UiPage>
</template>
