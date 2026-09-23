import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { abilitiesPlugin } from '@casl/vue'
import { createMongoAbility } from '@casl/ability'
import { useConfirm } from '@freya/ui'
import TicketDrawer from '@/views/tickets/drawer.vue'
import MessageView from '@/views/tickets/MessageView.vue'
import { useTickets } from '@/stores/tickets'
import type { Comment, Ticket } from '@/api/types'

type Call = { url: string; method: string; body: unknown }
type Reply = { status?: number; body?: unknown; bytes?: Uint8Array; type?: string }

const PNG = new Uint8Array([0x89, 0x50, 0x4e, 0x47])
function fetchMock(handler: (url: string, method: string, body: unknown) => Reply) {
  const calls: Call[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    const method = init.method ?? 'GET'
    const body = init.body ? JSON.parse(String(init.body)) : undefined
    calls.push({ url, method, body })
    const res = handler(url, method, body)
    const status = res.status ?? 200
    if (res.bytes) return new Response(res.bytes.slice().buffer as ArrayBuffer, { status, headers: { 'Content-Type': res.type ?? 'application/octet-stream' } })
    return new Response(status === 204 ? null : JSON.stringify(res.body ?? {}), { status, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}

const AGENT = ['read', 'create', 'update', 'assign', 'comment', 'reply', 'delete'].map((action) => ({ action, subject: 'Ticket' }))
const VIEWER = [{ action: 'read', subject: 'Ticket' }]
const withAbility = (rules: { action: string; subject: string }[]) => ({ plugins: [[abilitiesPlugin, createMongoAbility(rules), { useGlobalProperties: true }]] as never })

const ticket: Ticket = {
  id: 't1', subject: 'Screenshot of the error', description: 'See the screenshot below.', status: 'resolved', priority: 'normal', source: 'email',
  requester_name: 'Jane', requester_email: 'jane@customer.example', comment_count: 3, has_html: true, tags: [],
  attachments: [{ id: 'a1', filename: 'shot.png', content_type: 'image/png', size: 2048, content_id: 'shot1@customer.example', inline: true }, { id: 'a2', filename: 'invoice.pdf', content_type: 'application/pdf', size: 300 }],
  created_at: '2026-09-01T12:00:00Z', updated_at: '2026-09-01T12:00:00Z',
}
const comments: Comment[] = [
  { id: 'c1', ticket_id: 't1', body: 'Looking into it', internal: true, author_kind: 'agent', author_name: 'Ada', delivery: 'none', created_at: '2026-09-01T12:01:00Z' },
  { id: 'c2', ticket_id: 't1', body: 'We replaced the roller', internal: false, author_kind: 'agent', author_name: 'Ada', delivery: 'failed', message_id: 'm2', created_at: '2026-09-01T12:02:00Z' },
  { id: 'c3', ticket_id: 't1', body: 'Still broken', internal: false, author_kind: 'requester', author_email: 'jane@customer.example', delivery: 'none', created_at: '2026-09-01T12:03:00Z' },
  { id: 'c4', ticket_id: 't1', body: 'We received your request', internal: false, author_kind: 'system', delivery: 'sent', created_at: '2026-09-01T12:04:00Z' },
]
const HTML = '<p>See the screenshot below.</p><p><img src="/api/ticket/v1/tickets/t1/attachments/a1" alt="error"></p>'

function api(state: { ticket: Ticket; replyStatus?: number }) {
  return (url: string, method: string): Reply => {
    const path = url.replace(/^\/api\/ticket\/v1\//, '').split('?')[0]!
    if (path === 'assignable-users') return { body: { items: [] } }
    if (path === 'tickets/t1/attachments/a1') return { bytes: PNG, type: 'image/png' }
    if (path === 'tickets/t1/body') return { body: { html_sanitized: HTML, text: 'See the screenshot below.' } }
    if (path === 'tickets/t1/comments' && method === 'GET') return { body: { items: comments } }
    if (path === 'tickets/t1/comments' && method === 'POST') return { status: 201, body: { ...comments[0], id: 'c9' } }
    if (path === 'tickets/t1/reply') {
      if (state.replyStatus) return { status: state.replyStatus, body: { reason: state.replyStatus === 409 ? 'reply_unavailable' : 'delivery_failed' } }
      state.ticket = { ...state.ticket, status: 'open' }
      return { status: 201, body: { ...comments[1], id: 'c10', delivery: 'sent' } }
    }
    if (path.startsWith('comments/') && method === 'DELETE') return { status: 204 }
    if (path === 'tickets/t1') return { body: state.ticket }
    return { status: 404, body: { reason: 'not_found' } }
  }
}
const body = () => document.body
const panel = () => body().querySelector('[data-test="ticket-drawer"]')!

describe('conversation', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.cookie = '__Host-csrf=tok; Secure; Path=/'
    ;(globalThis as unknown as { __vw: number }).__vw = 1280
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it('message view: sanitised HTML in a permissionless sandboxed srcdoc frame, inline images embedded, plain-text toggle, downloads', async () => {
    const calls = fetchMock(api({ ticket: { ...ticket } }))
    const w = mount(MessageView, { props: { ticket }, attachTo: document.body })
    await flushPromises()
    const frame = w.find('iframe[data-test="message-frame"]')
    expect(frame.exists()).toBe(true)
    expect(frame.attributes('sandbox')).toBe('')
    expect(frame.attributes('sandbox')).not.toMatch(/allow-scripts|allow-same-origin/)
    expect(frame.attributes('referrerpolicy')).toBe('no-referrer')
    const srcdoc = (frame.element as HTMLIFrameElement).srcdoc
    expect(srcdoc).toContain('See the screenshot below.')
    expect(srcdoc).toContain('src="data:image/png;base64,')
    expect(srcdoc).not.toContain('/attachments/a1')
    expect(calls.some((c) => c.url === '/api/ticket/v1/tickets/t1/body')).toBe(true)

    await w.find('[data-test="message-plain-toggle"]').trigger('click')
    expect(w.find('iframe').exists()).toBe(false)
    expect(w.find('[data-test="message-text"]').text()).toBe('See the screenshot below.')

    const links = w.findAll('[data-test="message-attachments"] a')
    expect(links.map((a) => a.attributes('href'))).toEqual(['/api/ticket/v1/tickets/t1/attachments/a1', '/api/ticket/v1/tickets/t1/attachments/a2'])
    expect(links[1]!.attributes('download')).toBe('invoice.pdf')
    expect(w.find('[style]').exists()).toBe(false)
    w.unmount()
  })

  it('message view: a text-only ticket shows its description and fetches no body', async () => {
    const calls = fetchMock(api({ ticket: { ...ticket } }))
    const w = mount(MessageView, { props: { ticket: { ...ticket, has_html: false, attachments: [] } } })
    await flushPromises()
    expect(w.find('iframe').exists()).toBe(false)
    expect(w.find('[data-test="message-text"]').text()).toBe('See the screenshot below.')
    expect(calls.some((c) => c.url.endsWith('/body'))).toBe(false)
  })

  it('drawer: timeline badges incl. delivery failed, note and reply post, deletion confirms', async () => {
    const state: { ticket: Ticket; replyStatus?: number } = { ticket: { ...ticket } }
    const calls = fetchMock(api(state))
    const w = mount(TicketDrawer, { props: { ticketId: 't1' }, global: withAbility(AGENT), attachTo: document.body })
    await flushPromises()
    const timeline = panel().querySelector('[data-test="ticket-timeline"]')!
    const text = (id: string) => timeline.querySelector(`[data-test="comment-${id}"]`)!.textContent!
    expect(text('c1')).toContain('Internal')
    expect(text('c2')).toContain('Sent')
    expect(text('c2')).toContain('Delivery failed')
    expect(text('c3')).toContain('Incoming')
    expect(text('c4')).toContain('System')
    expect(timeline.querySelectorAll('[data-test="comment-delivery-failed"]').length).toBe(1)
    expect(Array.from(timeline.children).map((li) => li.getAttribute('data-test'))).toEqual(['comment-c1', 'comment-c2', 'comment-c3', 'comment-c4'])

    const setDraft = (v: string) => {
      const ta = panel().querySelector<HTMLTextAreaElement>('textarea[data-field="ticket-compose"]')!
      ta.value = v
      ta.dispatchEvent(new Event('input'))
    }
    const click = (sel: string) => (panel().querySelector(sel) as HTMLButtonElement).click()

    // internal note
    click('[data-test="compose-note"]')
    await flushPromises()
    setDraft('Checked the logs')
    await flushPromises()
    click('[data-test="compose-submit"]')
    await flushPromises()
    expect(calls.find((c) => c.method === 'POST')).toMatchObject({ url: '/api/ticket/v1/tickets/t1/comments', body: { body: 'Checked the logs', internal: true } })

    // public reply (re-opens the resolved ticket; drawer reloads it)
    click('[data-test="compose-reply"]')
    await flushPromises()
    setDraft('Please try again now')
    await flushPromises()
    click('[data-test="compose-submit"]')
    await flushPromises()
    expect(calls.filter((c) => c.method === 'POST').at(-1)).toMatchObject({ url: '/api/ticket/v1/tickets/t1/reply', body: { body: 'Please try again now' } })
    expect(panel().querySelector('[data-test="ticket-chips"]')!.textContent).toContain('Open')
    expect(w.emitted('changed')).toBeTruthy()

    // a failed delivery is surfaced
    state.replyStatus = 502
    setDraft('Again')
    await flushPromises()
    click('[data-test="compose-submit"]')
    await flushPromises()
    expect(panel().querySelector('[data-test="ticket-error"]')!.textContent).toContain('could not be delivered')

    // delete a comment after confirming
    const confirm = useConfirm()
    click('[data-test="comment-delete-c1"]')
    await flushPromises()
    confirm.answer(true)
    await flushPromises()
    expect(calls.at(-2)).toMatchObject({ url: '/api/ticket/v1/comments/c1', method: 'DELETE' })
    w.unmount()
  })

  it('drawer: reply is disabled without a requester; viewers get no composer', async () => {
    fetchMock(api({ ticket: { ...ticket, requester_email: '' } }))
    const w = mount(TicketDrawer, { props: { ticketId: 't1' }, global: withAbility(AGENT), attachTo: document.body })
    await flushPromises()
    expect(panel().querySelector<HTMLButtonElement>('[data-test="compose-reply"]')!.disabled).toBe(true)
    expect(panel().querySelector('[data-test="compose-no-requester"]')).not.toBeNull()
    expect(panel().querySelector('[data-test="compose-note"]')!.getAttribute('aria-pressed')).toBe('true')
    w.unmount()

    fetchMock(api({ ticket: { ...ticket } }))
    const ro = mount(TicketDrawer, { props: { ticketId: 't1' }, global: withAbility(VIEWER), attachTo: document.body })
    await flushPromises()
    expect(panel().querySelector('[data-test="ticket-composer"]')).toBeNull()
    expect(panel().querySelector('[data-test="comment-delete-c1"]')).toBeNull()
    expect(panel().querySelector('[data-test="ticket-timeline"]')).not.toBeNull()
    ro.unmount()
  })

  it('store: comment calls hit the contract paths', async () => {
    const calls = fetchMock(api({ ticket: { ...ticket } }))
    const s = useTickets()
    expect((await s.comments('t1')).length).toBe(4)
    await s.addNote('t1', 'n')
    await s.reply('t1', 'r')
    await s.removeComment('c1')
    expect((await s.body('t1')).html_sanitized).toContain('<img')
    expect(calls.map((c) => c.method + ' ' + c.url)).toEqual([
      'GET /api/ticket/v1/tickets/t1/comments', 'POST /api/ticket/v1/tickets/t1/comments', 'POST /api/ticket/v1/tickets/t1/reply',
      'DELETE /api/ticket/v1/comments/c1', 'GET /api/ticket/v1/tickets/t1/body',
    ])
  })
})
