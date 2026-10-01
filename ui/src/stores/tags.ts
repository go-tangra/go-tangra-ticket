import { defineStore } from 'pinia'
import { api } from '@/api/client'
import type { Tag, TagInput } from '@/api/types'
import { pagedList } from './paged'

/** Server-side sort fields of the tag list. */
export const TAG_SORTS = ['name'] as const

export const useTags = defineStore('ticket-tags', () => {
  const page = pagedList<Tag>('tags')
  const { items, total, reload } = page

  async function create(body: TagInput): Promise<Tag> {
    const t = await api<Tag>('POST', 'tags', body)
    void reload()
    return t
  }

  async function update(id: string, body: TagInput): Promise<Tag> {
    const t = await api<Tag>('PUT', 'tags/' + id, body)
    items.value = items.value.map((x) => (x.id === id ? t : x))
    return t
  }

  /** Deletes the tag; it disappears from every ticket carrying it. */
  async function remove(id: string): Promise<void> {
    await api('DELETE', 'tags/' + id)
    items.value = items.value.filter((x) => x.id !== id)
    total.value = Math.max(0, total.value - 1)
    void reload()
  }

  return { ...page, create, update, remove }
})
