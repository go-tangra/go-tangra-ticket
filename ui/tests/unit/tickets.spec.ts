import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { abilitiesPlugin } from '@casl/vue'
import { createMongoAbility } from '@casl/ability'
import { useConfirm } from '@freya/ui'
import Tickets from '@/views/tickets/index.vue'
import TicketDrawer from '@/views/tickets/drawer.vue'
import { ticketFilterSchema, ticketSchema, ticketUpdateSchema, STATUSES, PRIORITIES } from '@/schemas'
import { useTickets } from '@/stores/tickets'
import type { Ticket } from '@/api/types'

type Call = { url: string; method: string; body: unknown }

function fetchMock(handler: (url: string, method: string, body: unknown) => { status?: number; body?: unknown }) {
  const calls: Call[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    const method = init.method ?? 'GET'
    const body = init.body ? JSON.parse(String(init.body)) : undefined
    calls.push({ url, method, body })
    const res = handler(url, method, body)
    const status = res.status ?? 200
    return new Response(status === 204 ? null : JSON.stringify(res.body ?? {}), { status, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}

const AGENT = [{ action: 'read', subject: 'Ticket' }, { action: 'create', subject: 'Ticket' }, { action: 'update', subject: 'Ticket' }, { action: 'assign', subject: 'Ticket' }, { action: 'delete', subject: 'Ticket' }]
const VIEWER = [{ action: 'read', subject: 'Ticket' }]
const withAbility = (rules: { action: string; subject: string }[]) => ({ plugins: [[abilitiesPlugin, createMongoAbility(rules), { useGlobalProperties: true }]] as never })

const base: Ticket = {
  id: 't1', subject: 'Printer on fire', description: 'Smoke everywhere', status: 'open', priority: 'normal', source: 'manual',
  requester_name: 'Carol', requester_email: 'carol@example.org', comment_count: 0, tags: [{ id: 'g1', name: 'hardware', kind: 'tag' }],
  created_at: '2026-09-01T12:00:00Z', updated_at: '2026-09-01T12:00:00Z',
}
const users = { items: [{ id: 'u1', name: 'Ada Agent' }, { id: 'u2', name: 'Bob Agent' }] }
const tagList = [{ id: 'g1', name: 'hardware', kind: 'tag' }, { id: 'g2', name: 'Billing', kind: 'category' }]
const body = () => document.body

function standardApi(state: { ticket: Ticket; total?: number; created?: Ticket }) {
  return (url: string, method: string, reqBody: unknown): { status?: number; body?: unknown } => {
    const path = url.replace(/^\/api\/ticket\/v1\//, '').split('?')[0]!
    if (state.created && path === 'tickets/' + state.created.id) return { body: state.created }
    if (path === 'assignable-users') return { body: users }
    if (path === 'tags') return { body: { items: tagList } }
    if (path === 'tickets' && method === 'GET') return { body: { items: [state.ticket], total: state.total ?? 1 } }
    if (path === 'tickets' && method === 'POST') {
      state.created = { ...base, id: 't9', tags: [], ...(reqBody as object) }
      return { status: 201, body: state.created }
    }
    if (path.endsWith('/history')) return { body: { items: [{ id: 'h1', field: 'status', old_value: 'open', new_value: 'resolved', actor_kind: 'agent', created_at: '2026-09-01T13:00:00Z' }, { id: 'h2', field: 'assignee', old_value: '', new_value: 'u1', actor_kind: 'rule', created_at: '2026-09-01T14:00:00Z' }] } }
    if (path.endsWith('/status')) {
      state.ticket = { ...state.ticket, status: (reqBody as { status: Ticket['status'] }).status }
      return { body: state.ticket }
    }
    if (path.endsWith('/assign')) {
      const id = (reqBody as { assignee_id: string | null }).assignee_id
      state.ticket = { ...state.ticket, assignee_id: id ?? undefined, assignee_name: users.items.find((u) => u.id === id)?.name } as Ticket
      return { body: state.ticket }
    }
    if (method === 'PUT') {
      state.ticket = { ...state.ticket, ...(reqBody as object) }
      return { body: state.ticket }
    }
    if (method === 'DELETE') return { status: 204 }
    return { body: state.ticket }
  }
}

describe('ticket schemas', () => {
  it('create: subject required and single-line, optional fields blank → undefined, email checked', () => {
    expect(ticketSchema.safeParse({ subject: 'Printer', priority: '', requester_email: '', assignee_id: '' }).data).toEqual({ subject: 'Printer' })
    expect(ticketSchema.safeParse({ subject: '  ' }).success).toBe(false)
    expect(ticketSchema.safeParse({ subject: 'a\nb' }).success).toBe(false)
    expect(ticketSchema.safeParse({ subject: 'x'.repeat(999) }).success).toBe(false)
    expect(ticketSchema.safeParse({ subject: 'x', requester_email: 'not-an-address' }).success).toBe(false)
    expect(ticketSchema.safeParse({ subject: 'x', priority: 'whenever' }).success).toBe(false)
    expect(ticketSchema.safeParse({ subject: 'x', priority: 'urgent', requester_email: 'a@b.org' }).data).toEqual({ subject: 'x', priority: 'urgent', requester_email: 'a@b.org' })
    expect(ticketUpdateSchema.safeParse({ subject: 'x', description: '' }).data).toEqual({ subject: 'x' })
  })
  it('filter: cleared selects mean "any"; values are closed vocabularies', () => {
    expect(ticketFilterSchema.safeParse({ query: ' vpn ', status: '', priority: '', assignee_id: '', tag_id: '' }).data).toEqual({ query: 'vpn' })
    expect(ticketFilterSchema.safeParse({ status: 'unspecified' }).success).toBe(false)
    expect(ticketFilterSchema.safeParse({ assignee_id: 'none', status: 'in_progress' }).success).toBe(true)
    expect(STATUSES).toEqual(['open', 'in_progress', 'pending', 'resolved', 'closed'])
    expect(PRIORITIES).toEqual(['low', 'normal', 'high', 'urgent'])
  })
})

describe('tickets store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.cookie = '__Host-csrf=tok; Secure; Path=/'
  })
  afterEach(() => vi.unstubAllGlobals())

  it('lists with filters and paging, assigns (null unassigns), sets status, reads history and users', async () => {
    const state = { ticket: { ...base }, total: 60 }
    const calls = fetchMock(standardApi(state))
    const s = useTickets()
    await s.list({ status: 'open', assignee_id: 'none', query: undefined }, 2)
    const url = new URL(calls[0]!.url, 'https://x')
    expect(url.pathname).toBe('/api/ticket/v1/tickets')
    expect(Object.fromEntries(url.searchParams)).toEqual({ status: 'open', assignee_id: 'none', page: '2', page_size: '25' })
    expect(s.total).toBe(60)
    expect(s.page).toBe(2)
    await s.assign('t1', '')
    expect(calls.at(-1)).toMatchObject({ method: 'POST', body: { assignee_id: null } })
    await s.assign('t1', 'u1')
    expect(s.items[0]!.assignee_name).toBe('Ada Agent')
    await s.setStatus('t1', 'resolved')
    expect(calls.at(-1)).toMatchObject({ url: '/api/ticket/v1/tickets/t1/status', body: { status: 'resolved' } })
    expect(s.items[0]!.status).toBe('resolved')
    expect((await s.history('t1')).length).toBe(2)
    expect((await s.assignableUsers()).map((u) => u.id)).toEqual(['u1', 'u2'])
    expect((await s.loadTags()).map((t) => t.id)).toEqual(['g1', 'g2'])
    await s.create({ subject: 'New' })
    expect(s.total).toBe(61)
    await s.remove('t9')
    expect(s.total).toBe(60)
  })

  it('reports a failed list', async () => {
    fetchMock(() => ({ status: 503, body: { reason: 'temporarily_unavailable' } }))
    const s = useTickets()
    await s.list({})
    expect(s.error).not.toBe('')
    expect(s.items).toEqual([])
  })
})

describe('ticket views', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.cookie = '__Host-csrf=tok; Secure; Path=/'
    ;(globalThis as unknown as { __vw: number }).__vw = 1280
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it('list: rows with chips and tags, filter selects refetch (incl. unassigned), pager moves pages', async () => {
    const calls = fetchMock(standardApi({ ticket: { ...base }, total: 60 }))
    const w = mount(Tickets, { global: withAbility(AGENT), attachTo: document.body })
    await flushPromises()
    const row = w.find('[data-test="ticket-row-t1"]')
    expect(row.exists()).toBe(true)
    expect(row.text()).toContain('Printer on fire')
    expect(row.text()).toContain('hardware')
    expect(row.text()).toContain('Open')
    expect(row.text()).toContain('Unassigned')
    expect(w.find('[style]').exists()).toBe(false)

    const assignee = w.find<HTMLSelectElement>('select[data-field="assignee_id"]')
    expect(assignee.findAll('option').map((o) => o.text())).toEqual(['Anyone', 'Unassigned', 'Ada Agent', 'Bob Agent'])
    expect(w.find<HTMLSelectElement>('select[data-field="tag_id"]').findAll('option').map((o) => o.text())).toEqual(['Any', 'hardware', 'Billing (category)'])
    await assignee.setValue('none')
    await flushPromises()
    expect(calls.at(-1)!.url).toContain('assignee_id=none')
    await w.find<HTMLSelectElement>('select[data-field="status"]').setValue('pending')
    await flushPromises()
    expect(calls.at(-1)!.url).toMatch(/status=pending.*assignee_id=none|assignee_id=none.*status=pending/)

    expect(w.find('[data-test="ticket-pager"]').text()).toContain('Page 1 of 3 · 60 tickets')
    await w.find('button[aria-label="Next page"]').trigger('click')
    await flushPromises()
    expect(calls.at(-1)!.url).toContain('page=2')
    expect(calls.at(-1)!.url).toContain('status=pending')
    w.unmount()
  })

  it('list: viewers get no "New ticket"; agents create through a drawer that closes on save and opens the ticket', async () => {
    fetchMock(standardApi({ ticket: { ...base } }))
    const viewer = mount(Tickets, { global: withAbility(VIEWER), attachTo: document.body })
    await flushPromises()
    expect(viewer.find('[data-test="ticket-new"]').exists()).toBe(false)
    viewer.unmount()

    const calls = fetchMock(standardApi({ ticket: { ...base } }))
    const w = mount(Tickets, { global: withAbility(AGENT), attachTo: document.body })
    await flushPromises()
    await w.find('[data-test="ticket-new"]').trigger('click')
    await flushPromises()
    const drawer = body().querySelector('[data-test="ticket-create"]')!
    expect(drawer).not.toBeNull()
    const save = () => (Array.from(drawer.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Create') as HTMLButtonElement).click()
    save()
    await flushPromises()
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
    const subject = drawer.querySelector<HTMLInputElement>('input[data-field=subject]')!
    subject.value = 'VPN is down'
    subject.dispatchEvent(new Event('input'))
    const assignee = drawer.querySelector<HTMLSelectElement>('select[data-field=assignee_id]')!
    assignee.value = 'u2'
    assignee.dispatchEvent(new Event('change'))
    save()
    await flushPromises()
    const post = calls.find((c) => c.method === 'POST')!
    expect(post.url).toBe('/api/ticket/v1/tickets')
    expect(post.body).toEqual({ subject: 'VPN is down', priority: 'normal', assignee_id: 'u2' })
    expect(body().querySelector('[data-test="ticket-create"]')).toBeNull()
    expect(body().querySelector('[data-test="ticket-drawer"]')?.textContent).toContain('VPN is down')
    w.unmount()
  })

  it('drawer: status/priority/assignee save immediately, history tab renders, viewers cannot change', async () => {
    const state = { ticket: { ...base } }
    const calls = fetchMock(standardApi(state))
    await useTickets().assignableUsers()
    const w = mount(TicketDrawer, { props: { ticketId: 't1' }, global: withAbility(AGENT), attachTo: document.body })
    await flushPromises()
    const panel = body().querySelector('[data-test="ticket-drawer"]')!
    expect(panel.textContent).toContain('Printer on fire')
    expect(panel.textContent).toContain('Smoke everywhere')
    expect(panel.querySelector('[data-test="ticket-chips"]')?.textContent).toContain('hardware')

    const change = (field: string, value: string) => {
      const el = panel.querySelector<HTMLSelectElement>(`select[data-field="${field}"]`)!
      el.value = value
      el.dispatchEvent(new Event('change'))
    }
    change('ticket-status', 'resolved')
    await flushPromises()
    expect(calls.at(-1)).toMatchObject({ url: '/api/ticket/v1/tickets/t1/status', method: 'POST', body: { status: 'resolved' } })
    expect(panel.querySelector('[data-test="ticket-chips"]')?.textContent).toContain('Resolved')
    expect(w.emitted('changed')).toBeTruthy()

    change('ticket-priority', 'urgent')
    await flushPromises()
    expect(calls.at(-1)).toMatchObject({ url: '/api/ticket/v1/tickets/t1', method: 'PUT', body: { priority: 'urgent' } })

    change('ticket-assignee', 'u1')
    await flushPromises()
    expect(calls.at(-1)).toMatchObject({ url: '/api/ticket/v1/tickets/t1/assign', body: { assignee_id: 'u1' } })
    change('ticket-assignee', '')
    await flushPromises()
    expect(calls.at(-1)).toMatchObject({ body: { assignee_id: null } })

    const historyTab = Array.from(panel.querySelectorAll<HTMLButtonElement>('[role=tab]')).find((t) => t.textContent?.includes('History'))!
    historyTab.click()
    await flushPromises()
    const history = panel.querySelector('[data-test="ticket-history"]')!.textContent!
    expect(history).toContain('Open → Resolved')
    expect(history).toContain('nobody → Ada Agent')
    expect(history).toContain('by a rule')
    w.unmount()

    fetchMock(standardApi({ ticket: { ...base } }))
    const ro = mount(TicketDrawer, { props: { ticketId: 't1' }, global: withAbility(VIEWER), attachTo: document.body })
    await flushPromises()
    const roPanel = body().querySelector('[data-test="ticket-drawer"]')!
    expect(roPanel.querySelector<HTMLSelectElement>('select[data-field="ticket-status"]')!.disabled).toBe(true)
    expect(roPanel.querySelector('[data-test="ticket-delete"]')).toBeNull()
    expect(roPanel.querySelector('[data-test="ticket-edit"]')).toBeNull()
    ro.unmount()
  })

  it('drawer: edit subject, surfaced refusal, delete asks first', async () => {
    const state = { ticket: { ...base } }
    let calls = fetchMock(standardApi(state))
    const w = mount(TicketDrawer, { props: { ticketId: 't1' }, global: withAbility(AGENT), attachTo: document.body })
    await flushPromises()
    const panel = () => body().querySelector('[data-test="ticket-drawer"]')!
    ;(panel().querySelector('[data-test="ticket-edit"]') as HTMLButtonElement).click()
    await flushPromises()
    const subject = panel().querySelector<HTMLInputElement>('input[data-field=subject]')!
    subject.value = 'Printer fixed?'
    subject.dispatchEvent(new Event('input'))
    ;(panel().querySelector('[data-test="ticket-save"]') as HTMLButtonElement).click()
    await flushPromises()
    expect(calls.at(-1)).toMatchObject({ method: 'PUT', body: { subject: 'Printer fixed?', description: 'Smoke everywhere' } })
    expect(panel().textContent).toContain('Printer fixed?')

    // an invalid assignee refusal is shown in the drawer
    calls = fetchMock((url, method, b) => (url.endsWith('/assign') ? { status: 422, body: { reason: 'invalid_assignee' } } : standardApi(state)(url, method, b)))
    const el = panel().querySelector<HTMLSelectElement>('select[data-field="ticket-assignee"]')!
    el.value = 'u2'
    el.dispatchEvent(new Event('change'))
    await flushPromises()
    expect(panel().querySelector('[data-test="ticket-error"]')?.textContent).toContain('cannot work tickets')

    // delete: declined first, then confirmed
    const confirm = useConfirm()
    ;(panel().querySelector('[data-test="ticket-delete"]') as HTMLButtonElement).click()
    await flushPromises()
    confirm.answer(false)
    await flushPromises()
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)
    ;(panel().querySelector('[data-test="ticket-delete"]') as HTMLButtonElement).click()
    await flushPromises()
    confirm.answer(true)
    await flushPromises()
    expect(calls.at(-1)).toMatchObject({ url: '/api/ticket/v1/tickets/t1', method: 'DELETE' })
    expect(w.emitted('close')).toBeTruthy()
    w.unmount()
  })

  it('drawer: a missing ticket shows the refusal', async () => {
    fetchMock(() => ({ status: 404, body: { reason: 'ticket_not_found' } }))
    const w = mount(TicketDrawer, { props: { ticketId: 'gone' }, global: withAbility(AGENT), attachTo: document.body })
    await flushPromises()
    expect(body().querySelector('[data-test="ticket-error"]')?.textContent).toContain('no longer exists')
    w.unmount()
  })
})
