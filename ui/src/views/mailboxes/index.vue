<script setup lang="ts">
// Support mailboxes: the inbound addresses routed to this tenant, with the
// reply identity and acknowledgement settings. Create/edit in a record drawer;
// delete asks first and, while tickets still reference the mailbox, offers to
// detach them.
import { computed, onMounted, ref } from 'vue'
import { useAbility } from '@casl/vue'
import { UiPage, UiAlert, UiCard, UiButton, UiBadge, UiDataTable, UiRecordDrawer, useConfirm, useToast, type Column } from '@go-tangra/ui'
import { zodToFields } from '@go-tangra/ui/forms'
import { useMailboxes } from '@/stores/mailboxes'
import { AUTO_ACK_HINT, mailboxSchema } from '@/schemas'
import type { Mailbox, MailboxInput } from '@/api/types'
import { ApiError, describe } from '@/api/client'

const store = useMailboxes()
const ability = useAbility()
const confirm = useConfirm()
const toast = useToast()
const canManage = computed(() => ability.can('manage', 'TicketMailbox'))
const error = ref('')

onMounted(() => void store.list())

const fields = zodToFields(mailboxSchema, {
  address: { label: 'Address', cols: 12, required: true, placeholder: 'support@example.org' },
  display_name: { label: 'Display name', cols: 12, hint: 'The From name of replies (default "Support").' },
  active: { label: 'Active (accept inbound mail)', cols: 6 },
  auto_ack: { label: 'Send acknowledgements', cols: 6 },
  auto_ack_template: { label: 'Acknowledgement template', type: 'textarea', cols: 12, hint: AUTO_ACK_HINT },
})

const drawer = ref(false)
const editing = ref<Mailbox | null>(null)
function newMailbox(): void {
  editing.value = null
  drawer.value = true
}
function edit(m: Mailbox): void {
  editing.value = m
  drawer.value = true
}
const initial = computed(() => (editing.value ? { ...editing.value } : { active: true, auto_ack: false }))
function submit(v: Record<string, unknown>): Promise<Mailbox> {
  const body: MailboxInput = {
    address: v.address as string,
    display_name: (v.display_name as string | undefined) ?? '',
    active: Boolean(v.active),
    auto_ack: Boolean(v.auto_ack),
    auto_ack_template: (v.auto_ack_template as string | undefined) ?? '',
  }
  return editing.value ? store.update(editing.value.id, body) : store.create(body)
}

async function remove(m: Mailbox): Promise<void> {
  if (!(await confirm.ask({ title: `Delete ${m.address}?`, text: 'Mail to this address will no longer open tickets.', danger: true, confirmLabel: 'Delete' }))) return
  error.value = ''
  try {
    await store.remove(m.id)
    toast.show({ kind: 'success', title: 'Mailbox deleted' })
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) {
      if (!(await confirm.ask({ title: 'Tickets use this mailbox', text: 'Detach those tickets from the mailbox and delete it?', danger: true, confirmLabel: 'Detach tickets and delete' }))) return
      try {
        await store.remove(m.id, true)
        toast.show({ kind: 'success', title: 'Mailbox deleted' })
      } catch (e2) {
        error.value = describe(e2)
      }
      return
    }
    error.value = describe(e)
  }
}

type Row = Mailbox & Record<string, unknown>
const columns: Column<Row>[] = [
  { key: 'address', label: 'Address' },
  { key: 'display_name', label: 'Display name', hideOnStack: true },
  { key: 'active', label: 'Active', width: 'sm' },
  { key: 'auto_ack', label: 'Auto-ack', width: 'sm' },
]
const rows = computed(() => store.items as Row[])
</script>

<template>
  <UiPage title="Mailboxes">
    <template #actions>
      <UiButton v-if="canManage" icon="mdi-plus" data-test="mailbox-new" @click="newMailbox">New mailbox</UiButton>
      <UiButton variant="text" icon="mdi-refresh" icon-only label="Refresh" @click="store.list()" />
    </template>
    <UiAlert v-if="error" kind="error" class="mb-3" data-test="mailbox-error">{{ error }}</UiAlert>
    <UiAlert v-if="store.error" kind="error" class="mb-3">{{ store.error }}</UiAlert>
    <UiCard :padded="false">
      <UiDataTable :items="rows" :columns="columns" :loading="store.loading" caption="Support mailboxes" empty-title="No mailboxes" empty-text="Add the support address your mail relay forwards to this service." :row-attrs="(m) => ({ 'data-test': 'mailbox-row-' + m.id })" data-test="mailboxes-table">
        <template #cell-active="{ row }"><UiBadge :color="row.active ? 'success' : 'neutral'">{{ row.active ? 'Active' : 'Inactive' }}</UiBadge></template>
        <template #cell-auto_ack="{ row }"><UiBadge :color="row.auto_ack ? 'info' : 'neutral'">{{ row.auto_ack ? 'On' : 'Off' }}</UiBadge></template>
        <template v-if="canManage" #actions="{ row }">
          <UiButton size="xs" variant="text" icon="mdi-pencil-outline" icon-only label="Edit mailbox" :data-test="'mailbox-edit-' + row.id" @click="edit(row)" />
          <UiButton size="xs" variant="text" color="error" icon="mdi-delete-outline" icon-only label="Delete mailbox" :data-test="'mailbox-delete-' + row.id" @click="remove(row)" />
        </template>
      </UiDataTable>
    </UiCard>

    <UiRecordDrawer v-model="drawer" close-on-save :title="editing ? 'Edit mailbox' : 'New mailbox'" :schema="mailboxSchema" :fields="fields" :initial="initial" :submit="submit" data-test="mailbox-drawer" />
  </UiPage>
</template>
