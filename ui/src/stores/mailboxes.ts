import { defineStore } from 'pinia'
import { api } from '@/api/client'
import type { Mailbox, MailboxInput } from '@/api/types'
import { pagedList } from './paged'

/** Server-side sort fields of the mailbox list (name = display name). */
export const MAILBOX_SORTS = ['address', 'name'] as const

export const useMailboxes = defineStore('ticket-mailboxes', () => {
  const page = pagedList<Mailbox>('mailboxes')
  const { items, total, reload } = page

  async function create(body: MailboxInput): Promise<Mailbox> {
    const m = await api<Mailbox>('POST', 'mailboxes', body)
    void reload()
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
    total.value = Math.max(0, total.value - 1)
    void reload()
  }

  return { ...page, create, update, remove }
})
