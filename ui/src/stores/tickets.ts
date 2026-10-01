import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, describe } from '@/api/client'
import type { ListParams } from '@go-tangra/ui'
import type { AssignableUser, Comment, HistoryEntry, MessageBody, Tag, Ticket, TicketCreate, TicketFilter, TicketPage, TicketUpdate } from '@/api/types'

/** Columns the ticket list sorts on server-side (api/openapi/ticket.yaml). */
export const TICKET_SORTS = ['subject', 'status', 'priority', 'assignee', 'created_at', 'updated_at'] as const

export const useTickets = defineStore('ticket-tickets', () => {
  const items = ref<Ticket[]>([])
  const total = ref(0)
  const filter = ref<TicketFilter>({})
  const params = ref<Partial<ListParams>>({})
  const loading = ref(false)
  const error = ref('')
  const users = ref<AssignableUser[]>([])
  const tags = ref<Tag[]>([])
  let seq = 0

  /**
   * Loads one page for the filter and the page/size/sort parameters (server
   * defaults when omitted). Resolves with the page the server answered (it
   * clamps pages beyond the end), or null when a newer request superseded it.
   */
  async function list(f: TicketFilter = filter.value, p: Partial<ListParams> = params.value): Promise<number | null> {
    const mine = ++seq
    loading.value = true
    error.value = ''
    filter.value = { ...f }
    params.value = { ...p }
    try {
      const res = await api<TicketPage>('GET', 'tickets', undefined, { query: { ...f, ...p } })
      if (mine !== seq) return null
      items.value = res.items ?? []
      total.value = res.total ?? 0
      return res.page ?? 1
    } catch (e) {
      if (mine === seq) error.value = describe(e)
      return null
    } finally {
      if (mine === seq) loading.value = false
    }
  }

  /** Reloads the current page (same filter, page, size and sort). */
  const reload = () => list(filter.value, params.value)

  async function get(id: string): Promise<Ticket> {
    return api<Ticket>('GET', 'tickets/' + id)
  }

  function replace(t: Ticket): Ticket {
    items.value = items.value.map((x) => (x.id === t.id ? t : x))
    return t
  }

  async function create(body: TicketCreate): Promise<Ticket> {
    const t = await api<Ticket>('POST', 'tickets', body)
    items.value = [t, ...items.value]
    total.value += 1
    return t
  }

  async function update(id: string, body: TicketUpdate): Promise<Ticket> {
    return replace(await api<Ticket>('PUT', 'tickets/' + id, body))
  }

  async function remove(id: string): Promise<void> {
    await api('DELETE', 'tickets/' + id)
    const before = items.value.length
    items.value = items.value.filter((x) => x.id !== id)
    if (items.value.length < before) total.value = Math.max(0, total.value - 1)
  }

  /** Assigns the ticket; null (or '') unassigns it. */
  async function assign(id: string, assigneeId: string | null): Promise<Ticket> {
    return replace(await api<Ticket>('POST', 'tickets/' + id + '/assign', { assignee_id: assigneeId || null }))
  }

  async function setStatus(id: string, status: string): Promise<Ticket> {
    return replace(await api<Ticket>('POST', 'tickets/' + id + '/status', { status }))
  }

  async function history(id: string): Promise<HistoryEntry[]> {
    const res = await api<{ items: HistoryEntry[] }>('GET', 'tickets/' + id + '/history')
    return res.items ?? []
  }

  /** Agents a ticket may be assigned to (users holding tickets:manage). */
  async function assignableUsers(): Promise<AssignableUser[]> {
    try {
      const res = await api<{ items: AssignableUser[] }>('GET', 'assignable-users')
      users.value = res.items ?? []
    } catch (e) {
      error.value = describe(e)
    }
    return users.value
  }

  /** Tags for the filter bar (the first 200 by name); an unavailable tag service leaves the list empty. */
  async function loadTags(): Promise<Tag[]> {
    try {
      const res = await api<{ items: Tag[] }>('GET', 'tags', undefined, { query: { page_size: 200 } })
      tags.value = res.items ?? []
    } catch {
      tags.value = []
    }
    return tags.value
  }

  /** Replaces the ticket's tag set. */
  async function setTags(id: string, tagIds: string[]): Promise<Ticket> {
    return replace(await api<Ticket>('POST', 'tickets/' + id + '/tags', { tag_ids: tagIds }))
  }

  /** The ticket's conversation, oldest first. */
  async function comments(id: string): Promise<Comment[]> {
    const res = await api<{ items: Comment[] }>('GET', 'tickets/' + id + '/comments')
    return res.items ?? []
  }

  /** Adds an internal note (never emailed). */
  async function addNote(id: string, text: string): Promise<Comment> {
    return api<Comment>('POST', 'tickets/' + id + '/comments', { body: text, internal: true })
  }

  /** Emails a public reply to the requester and records it. */
  async function reply(id: string, text: string): Promise<Comment> {
    return api<Comment>('POST', 'tickets/' + id + '/reply', { body: text })
  }

  async function removeComment(id: string): Promise<void> {
    await api('DELETE', 'comments/' + id)
  }

  /** Sanitised HTML + text of the ticket's first message (raw HTML is never served). */
  async function body(id: string): Promise<MessageBody> {
    return api<MessageBody>('GET', 'tickets/' + id + '/body')
  }

  return { comments, addNote, reply, removeComment, body, items, total, params, filter, loading, error, users, tags, list, reload, get, create, update, remove, assign, setStatus, history, assignableUsers, loadTags, setTags }
})
