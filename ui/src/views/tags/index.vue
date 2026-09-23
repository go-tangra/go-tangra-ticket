<script setup lang="ts">
// Tag vocabulary: tags and categories with a palette colour (automatic when
// none is chosen) and a description. Filter by kind; create/edit in a record
// drawer (the kind is fixed after creation); delete asks first and removes the
// tag from every ticket.
import { computed, onMounted, ref } from 'vue'
import { useAbility } from '@casl/vue'
import { UiPage, UiAlert, UiCard, UiButton, UiBadge, UiDataTable, UiRecordDrawer, UiSelect, useConfirm, useToast, type Column, type SelectOption } from '@freya/ui'
import { zodToFields } from '@freya/ui/forms'
import { useTags } from '@/stores/tags'
import { tagSchema, TAG_KIND_OPTIONS } from '@/schemas'
import { TAG_COLORS, TAG_COLOR_LABELS, tagColor } from './colors'
import type { Tag, TagInput, TagKind } from '@/api/types'
import { describe } from '@/api/client'

const store = useTags()
const ability = useAbility()
const confirm = useConfirm()
const toast = useToast()
const canManage = computed(() => ability.can('manage', 'TicketTag'))
const error = ref('')
const kind = ref<TagKind | ''>('')

onMounted(() => void store.list())
function filterKind(v: unknown): void {
  kind.value = v === 'tag' || v === 'category' ? v : ''
  void store.list(kind.value)
}

const colorOptions: SelectOption[] = TAG_COLORS.map((c) => ({ title: TAG_COLOR_LABELS[c], value: c }))
const allFields = zodToFields(tagSchema, {
  name: { label: 'Name', cols: 12, required: true },
  kind: { label: 'Kind', cols: 6, type: 'select', options: TAG_KIND_OPTIONS, hint: 'Fixed after creation.' },
  color: { label: 'Colour', cols: 6, type: 'select', options: colorOptions, placeholder: 'Automatic' },
  description: { label: 'Description', type: 'textarea', cols: 12 },
})

const drawer = ref(false)
const editing = ref<Tag | null>(null)
const fields = computed(() => (editing.value ? allFields.filter((f) => f.key !== 'kind') : allFields))
function newTag(): void {
  editing.value = null
  drawer.value = true
}
function edit(t: Tag): void {
  editing.value = t
  drawer.value = true
}
const initial = computed(() => (editing.value ? { ...editing.value, color: editing.value.color && TAG_COLORS.includes(editing.value.color as never) ? editing.value.color : '' } : { kind: kind.value || 'tag' }))
function submit(v: Record<string, unknown>): Promise<Tag> {
  const body: TagInput = {
    name: v.name as string,
    color: (v.color as string | undefined) ?? '',
    description: (v.description as string | undefined) ?? '',
  }
  if (editing.value) return store.update(editing.value.id, body)
  return store.create({ ...body, kind: (v.kind as TagKind | undefined) ?? 'tag' })
}

async function remove(t: Tag): Promise<void> {
  if (!(await confirm.ask({ title: `Delete ${t.name}?`, text: 'It is removed from every ticket that carries it.', danger: true, confirmLabel: 'Delete' }))) return
  error.value = ''
  try {
    await store.remove(t.id)
    toast.show({ kind: 'success', title: 'Tag deleted' })
  } catch (e) {
    error.value = describe(e)
  }
}

type Row = Tag & Record<string, unknown>
const columns: Column<Row>[] = [
  { key: 'name', label: 'Name' },
  { key: 'kind', label: 'Kind', width: 'sm' },
  { key: 'description', label: 'Description', hideOnStack: true },
]
const rows = computed(() => store.items as Row[])
</script>

<template>
  <UiPage title="Tags">
    <template #actions>
      <UiButton v-if="canManage" icon="mdi-plus" data-test="tag-new" @click="newTag">New tag</UiButton>
      <UiButton variant="text" icon="mdi-refresh" icon-only label="Refresh" @click="store.list(kind)" />
    </template>
    <UiAlert v-if="error" kind="error" class="mb-3" data-test="tag-error">{{ error }}</UiAlert>
    <UiAlert v-if="store.error" kind="error" class="mb-3">{{ store.error }}</UiAlert>
    <div class="mb-3 max-w-xs">
      <UiSelect id="tag-kind-filter" label="Kind" :model-value="kind" :options="TAG_KIND_OPTIONS" placeholder="All kinds" size="sm" data-test="tag-kind-filter" @update:model-value="filterKind" />
    </div>
    <UiCard :padded="false">
      <UiDataTable :items="rows" :columns="columns" :loading="store.loading" caption="Tags and categories" empty-title="No tags" empty-text="Create tags and categories to organise tickets; rules can also create them." :row-attrs="(t) => ({ 'data-test': 'tag-row-' + t.id })" data-test="tags-table">
        <template #cell-name="{ row }"><UiBadge :color="tagColor(row)" :data-test="'tag-chip-' + row.id">{{ row.name }}</UiBadge></template>
        <template #cell-kind="{ row }">{{ row.kind === 'category' ? 'Category' : 'Tag' }}</template>
        <template v-if="canManage" #actions="{ row }">
          <UiButton size="xs" variant="text" icon="mdi-pencil-outline" icon-only label="Edit tag" :data-test="'tag-edit-' + row.id" @click="edit(row)" />
          <UiButton size="xs" variant="text" color="error" icon="mdi-delete-outline" icon-only label="Delete tag" :data-test="'tag-delete-' + row.id" @click="remove(row)" />
        </template>
      </UiDataTable>
    </UiCard>

    <UiRecordDrawer v-model="drawer" close-on-save :title="editing ? 'Edit tag' : 'New tag'" :schema="tagSchema" :fields="fields" :initial="initial" :submit="submit" data-test="tag-drawer" />
  </UiPage>
</template>
