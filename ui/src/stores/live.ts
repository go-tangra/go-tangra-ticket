import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { TicketEvent } from '@/api/types'

// One shared EventSource relays this tenant's ticket events (GET /stream,
// tickets:read) through the gateway. It is reference-counted so the list and
// the dashboard share a connection. Events carry ids and metadata only.
export type Listener = (type: string, data: TicketEvent) => void

export const EVENTS = ['ticket.created', 'ticket.assigned', 'ticket.status_changed', 'ticket.commented', 'ticket.requester_replied'] as const
export const STREAM_URL = '/api/ticket/v1/stream'

export const useLive = defineStore('ticket-live', () => {
  const connected = ref(false)
  const recent = ref<{ type: string; at: string; data: TicketEvent }[]>([])
  let source: EventSource | null = null
  let refs = 0
  const listeners = new Set<Listener>()

  function handle(type: string, raw: string): void {
    let data: TicketEvent
    try {
      data = JSON.parse(raw) as TicketEvent
    } catch {
      return // non-JSON frames are ignored
    }
    if (!data || typeof data.ticket_id !== 'string') return
    recent.value = [{ type, at: new Date().toISOString(), data }, ...recent.value].slice(0, 20)
    for (const l of listeners) l(type, data)
  }

  function open(): void {
    if (source || typeof EventSource === 'undefined') return
    source = new EventSource(STREAM_URL, { withCredentials: true })
    source.onopen = () => (connected.value = true)
    source.onerror = () => (connected.value = false)
    for (const t of EVENTS) source.addEventListener(t, (e) => handle(t, (e as MessageEvent).data))
  }

  function close(): void {
    refs = 0
    source?.close()
    source = null
    connected.value = false
  }

  /** Opens the stream (first caller) and returns a release function. */
  function connect(): () => void {
    refs += 1
    open()
    let released = false
    return () => {
      if (released) return
      released = true
      refs -= 1
      if (refs <= 0) close()
    }
  }

  /** Subscribes to every event; returns the unsubscribe function. */
  function on(l: Listener): () => void {
    listeners.add(l)
    return () => listeners.delete(l)
  }

  return { connected, recent, connect, close, on, _emit: handle }
})

/**
 * Calls fn at most once per `wait` ms for a burst of events (a rule run or a
 * bulk import emits many); the returned cancel stops a pending call.
 */
export function coalesce(fn: () => void, wait = 400): { trigger: () => void; cancel: () => void } {
  let timer: ReturnType<typeof setTimeout> | null = null
  return {
    trigger: () => {
      if (timer) return
      timer = setTimeout(() => {
        timer = null
        fn()
      }, wait)
    },
    cancel: () => {
      if (timer) clearTimeout(timer)
      timer = null
    },
  }
}
