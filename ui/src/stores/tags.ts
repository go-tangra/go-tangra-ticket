import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, describe } from '@/api/client'
import type { Tag, TagInput, TagKind } from '@/api/types'

const byKindName = (a: Tag, b: Tag) => a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name, undefined, { sensitivity: 'base' })

export const useTags = defineStore('ticket-tags', () => {
  const items = ref<Tag[]>([])
  const loading = ref(false)
  const error = ref('')

  /** Loads the vocabulary (optionally one kind). */
  async function list(kind?: TagKind | ''): Promise<void> {
    loading.value = true
    error.value = ''
    try {
      const res = await api<{ items: Tag[] }>('GET', 'tags', undefined, kind ? { query: { kind } } : {})
      items.value = res.items ?? []
    } catch (e) {
      error.value = describe(e)
    } finally {
      loading.value = false
    }
  }

  async function create(body: TagInput): Promise<Tag> {
    const t = await api<Tag>('POST', 'tags', body)
    items.value = [...items.value, t].sort(byKindName)
    return t
  }

  async function update(id: string, body: TagInput): Promise<Tag> {
    const t = await api<Tag>('PUT', 'tags/' + id, body)
    items.value = items.value.map((x) => (x.id === id ? t : x)).sort(byKindName)
    return t
  }

  /** Deletes the tag; it disappears from every ticket carrying it. */
  async function remove(id: string): Promise<void> {
    await api('DELETE', 'tags/' + id)
    items.value = items.value.filter((x) => x.id !== id)
  }

  return { items, loading, error, list, create, update, remove }
})
