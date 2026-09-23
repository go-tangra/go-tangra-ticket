import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, describe } from '@/api/client'
import type { Mailbox, MailboxInput } from '@/api/types'

export const useMailboxes = defineStore('ticket-mailboxes', () => {
  const items = ref<Mailbox[]>([])
  const loading = ref(false)
  const error = ref('')

  async function list(): Promise<void> {
    loading.value = true
    error.value = ''
    try {
      const res = await api<{ items: Mailbox[] }>('GET', 'mailboxes')
      items.value = res.items ?? []
    } catch (e) {
      error.value = describe(e)
    } finally {
      loading.value = false
    }
  }

  async function create(body: MailboxInput): Promise<Mailbox> {
    const m = await api<Mailbox>('POST', 'mailboxes', body)
    items.value = [...items.value, m].sort((a, b) => a.address.localeCompare(b.address))
    return m
  }

  async function update(id: string, body: MailboxInput): Promise<Mailbox> {
    const m = await api<Mailbox>('PUT', 'mailboxes/' + id, body)
    items.value = items.value.map((x) => (x.id === id ? m : x))
    return m
  }

  /** Deletes the mailbox; force detaches tickets that still reference it. */
  async function remove(id: string, force = false): Promise<void> {
    await api('DELETE', 'mailboxes/' + id, undefined, force ? { query: { force: 'true' } } : {})
    items.value = items.value.filter((x) => x.id !== id)
  }

  return { items, loading, error, list, create, update, remove }
})
