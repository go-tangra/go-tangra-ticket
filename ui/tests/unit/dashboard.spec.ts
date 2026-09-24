import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { abilitiesPlugin } from '@casl/vue'
import { createMongoAbility } from '@casl/ability'
import { axe } from 'vitest-axe'
import Dashboard from '@/views/dashboard/index.vue'
import Tickets from '@/views/tickets/index.vue'
import { coalesce, EVENTS, STREAM_URL, useLive } from '@/stores/live'
import { useStats } from '@/stores/stats'
import { backupImportSchema, STATS_WINDOWS } from '@/schemas'
import type { TicketStats } from '@/api/types'

type Call = { url: string; method: string; body: unknown }

function fetchMock(handler: (url: string, method: string, body: unknown) => { status?: number; body?: unknown }) {
  const calls: Call[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    const method = init.method ?? 'GET'
    const body = init.body ? JSON.parse(String(init.body)) : undefined
    calls.push({ url, method, body })
    const res = handler(url, method, body)
    return new Response(JSON.stringify(res.body ?? {}), { status: res.status ?? 200, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}

class FakeSource {
  static instances: FakeSource[] = []
  url: string
  closed = false
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  listeners = new Map<string, (e: MessageEvent) => void>()
  constructor(url: string) {
    this.url = url
    FakeSource.instances.push(this)
  }
  addEventListener(t: string, fn: (e: MessageEvent) => void) { this.listeners.set(t, fn) }
  close() { this.closed = true }
  emit(t: string, data: unknown) { this.listeners.get(t)?.(new MessageEvent(t, { data: typeof data === 'string' ? data : JSON.stringify(data) })) }
}

const day = (n: number) => new Date(Date.UTC(2026, 8, 23 - n)).toISOString().slice(0, 10)
const series = (counts: Record<number, number>, len = 30) => Array.from({ length: len }, (_, i) => ({ day: day(len - 1 - i), count: counts[len - 1 - i] ?? 0 }))
const snapshot: TicketStats = {
  total: 9,
  by_status: { open: 3, in_progress: 2, pending: 1, resolved: 2, closed: 1 },
  by_priority: { low: 1, normal: 5, high: 2, urgent: 1 },
  by_assignee: [{ assignee_id: 'u1', assignee_name: 'Ada Agent', count: 3 }, { assignee_id: 'u2', count: 1 }],
  unassigned_open: 2,
  created_per_day: series({ 0: 2, 3: 4 }),
  resolved_per_day: series({ 0: 1, 5: 1 }),
}

const withAbility = (rules: { action: string; subject: string }[]) => ({ plugins: [[abilitiesPlugin, createMongoAbility(rules), { useGlobalProperties: true }]] as never })
const ADMIN = [{ action: 'read', subject: 'TicketStats' }, { action: 'manage', subject: 'TicketBackup' }, { action: 'read', subject: 'Ticket' }]
const READER = [{ action: 'read', subject: 'TicketStats' }]

describe('stats store and schemas', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.cookie = '__Host-csrf=tok; Secure; Path=/'
  })
  afterEach(() => vi.unstubAllGlobals())

  it('loads a window, reports failures, exports and imports', async () => {
    const calls = fetchMock((url) => (url.includes('/stats') ? { body: snapshot } : url.endsWith('/import') ? { body: { imported: { tickets: 1 }, skipped: {} } } : { body: { schema_version: 1 } }))
    const s = useStats()
    await s.load(7)
    expect(calls[0]!.url).toBe('/api/ticket/v1/stats?days=7')
    expect(s.days).toBe(7)
    expect(s.snapshot?.total).toBe(9)
    expect(await s.exportBackup()).toEqual({ schema_version: 1 })
    expect(calls.at(-1)).toMatchObject({ method: 'POST', url: '/api/ticket/v1/backup/export' })
    await s.importBackup({ schema_version: 1 }, 'overwrite')
    expect(calls.at(-1)).toMatchObject({ url: '/api/ticket/v1/backup/import', body: { mode: 'overwrite', backup: { schema_version: 1 } } })

    fetchMock(() => ({ status: 403, body: { reason: 'forbidden' } }))
    await s.load()
    expect(s.error).not.toBe('')
    expect(s.snapshot).toBeNull()
  })

  it('backup import form needs a file and a known mode; windows fit the API', () => {
    expect(backupImportSchema.safeParse({ mode: 'skip' }).success).toBe(false)
    const f = new File(['{}'], 'b.json', { type: 'application/json' })
    expect(backupImportSchema.safeParse({ file: f, mode: 'merge' }).success).toBe(false)
    expect(backupImportSchema.safeParse({ file: f, mode: 'overwrite' }).success).toBe(true)
    expect(STATS_WINDOWS.every((d) => d >= 1 && d <= 365)).toBe(true)
  })
})

describe('live stream store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.stubGlobal('EventSource', FakeSource)
    FakeSource.instances = []
  })
  afterEach(() => vi.unstubAllGlobals())

  it('shares one reference-counted EventSource, relays ticket events, ignores junk', () => {
    const live = useLive()
    const r1 = live.connect()
    const r2 = live.connect()
    expect(FakeSource.instances.length).toBe(1)
    const src = FakeSource.instances[0]!
    expect(src.url).toBe(STREAM_URL)
    expect([...src.listeners.keys()]).toEqual([...EVENTS])
    src.onopen?.()
    expect(live.connected).toBe(true)
    const seen: string[] = []
    const off = live.on((type, data) => seen.push(type + ':' + data.ticket_id))
    src.emit('ticket.created', { ticket_id: 't1', status: 'open' })
    src.emit('ticket.assigned', 'not json')
    src.emit('ticket.assigned', { no_ticket: true })
    expect(seen).toEqual(['ticket.created:t1'])
    expect(live.recent[0]!.type).toBe('ticket.created')
    off()
    src.emit('ticket.status_changed', { ticket_id: 't2' })
    expect(seen.length).toBe(1)
    src.onerror?.()
    expect(live.connected).toBe(false)
    r1()
    r1() // idempotent
    expect(src.closed).toBe(false)
    r2()
    expect(src.closed).toBe(true)
  })

  it('without EventSource (old browsers, jsdom) connecting is a no-op', () => {
    vi.stubGlobal('EventSource', undefined)
    const live = useLive()
    const release = live.connect()
    expect(live.connected).toBe(false)
    release()
  })

  it('coalesce runs once per burst and can be cancelled', () => {
    vi.useFakeTimers()
    const fn = vi.fn()
    const c = coalesce(fn, 100)
    c.trigger()
    c.trigger()
    c.trigger()
    vi.advanceTimersByTime(100)
    expect(fn).toHaveBeenCalledTimes(1)
    c.trigger()
    c.cancel()
    vi.advanceTimersByTime(200)
    expect(fn).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
  })
})

describe('dashboard view', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.cookie = '__Host-csrf=tok; Secure; Path=/'
    vi.stubGlobal('EventSource', FakeSource)
    FakeSource.instances = []
    ;(globalThis as unknown as { __vw: number }).__vw = 1280
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.useRealTimers()
    document.body.innerHTML = ''
  })

  it('tiles, status/priority/assignee bars, active days newest first, no inline styles, axe clean', async () => {
    fetchMock(() => ({ body: snapshot }))
    const w = mount(Dashboard, { global: withAbility(READER), attachTo: document.body })
    await flushPromises()
    const tiles = w.findAll('.stat-tile')
    expect(tiles.length).toBe(4)
    expect(tiles[0]!.text()).toContain('6') // open + in progress + pending
    expect(tiles[0]!.text()).toContain('9 tickets in total')
    expect(tiles[1]!.text()).toContain('2')
    expect(tiles[2]!.text()).toContain('6') // created in the window
    expect(tiles[3]!.text()).toContain('2') // resolved in the window
    expect(w.find('[data-test="stats-status"]').findAll('progress').length).toBe(5)
    expect(w.find('[data-test="stats-priority"]').text()).toContain('Urgent')
    const assignees = w.find('[data-test="stats-assignee"]').text()
    expect(assignees).toContain('Ada Agent')
    expect(assignees).toContain('u2') // no name: the id
    expect(assignees).toContain('Unassigned')
    const rows = w.find('[data-test="stats-per-day"]').findAll('tbody tr')
    expect(rows.map((r) => r.findAll('td').map((c) => c.text().trim()).join(' '))).toEqual([`${day(0)} 2 1 +1`, `${day(3)} 4 0 +4`, `${day(5)} 0 1 -1`])
    expect(w.find('[data-test="backup"]').exists()).toBe(false) // no backup:manage
    expect(w.find('[style]').exists()).toBe(false)
    // heading-order: the kit's UiStatTile renders its value as <h4> under the page <h1>
    // (a kit-wide markup choice shared by every module dashboard), not this view's markup.
    expect((await axe(w.element as HTMLElement, { rules: { 'heading-order': { enabled: false }, 'color-contrast': { enabled: false }, region: { enabled: false } } })).violations).toEqual([])
    w.unmount()
  })

  it('changes the window and refreshes on live events (coalesced)', async () => {
    const calls = fetchMock(() => ({ body: snapshot }))
    const w = mount(Dashboard, { global: withAbility(READER), attachTo: document.body })
    await flushPromises()
    await w.find<HTMLSelectElement>('[data-test="stats-window"] select, select#stats-window').setValue('7')
    await flushPromises()
    expect(calls.at(-1)!.url).toBe('/api/ticket/v1/stats?days=7')
    vi.useFakeTimers()
    const before = calls.length
    const src = FakeSource.instances[0]!
    src.emit('ticket.created', { ticket_id: 'a' })
    src.emit('ticket.assigned', { ticket_id: 'a' })
    vi.advanceTimersByTime(500)
    vi.useRealTimers()
    await flushPromises()
    expect(calls.length).toBe(before + 1)
    expect(calls.at(-1)!.url).toBe('/api/ticket/v1/stats?days=7')
    w.unmount()
    expect(src.closed).toBe(true)
  })

  it('empty tenant and failures', async () => {
    fetchMock(() => ({ body: { total: 0, by_status: {}, by_priority: {}, by_assignee: [], unassigned_open: 0, created_per_day: series({}), resolved_per_day: series({}) } }))
    const w = mount(Dashboard, { global: withAbility(READER), attachTo: document.body })
    await flushPromises()
    expect(w.text()).toContain('No tickets yet')
    expect(w.text()).toContain('No open tickets')
    expect(w.text()).toContain('No activity in this window')
    w.unmount()
    fetchMock(() => ({ status: 503, body: { reason: 'temporarily_unavailable' } }))
    const f = mount(Dashboard, { global: withAbility(READER), attachTo: document.body })
    await flushPromises()
    expect(f.find('[role="alert"], .alert').exists()).toBe(true)
    f.unmount()
  })

  it('backup: export downloads the document, import posts the parsed file and reports counts', async () => {
    const calls = fetchMock((url, method) => {
      if (url.includes('/stats')) return { body: snapshot }
      if (url.endsWith('/backup/export')) return { body: { schema_version: 1, tickets: [] } }
      if (url.endsWith('/backup/import') && method === 'POST') return { body: { tenant_id: 't', mode: 'overwrite', imported: { tickets: 2, tag_links: 1 }, skipped: { mailboxes: 1 }, deleted: 0 } }
      return { body: {} }
    })
    const created: string[] = []
    const click = vi.fn()
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: () => { created.push('blob'); return 'blob:x' }, revokeObjectURL: () => {} }))
    const origCreate = document.createElement.bind(document)
    vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      const el = origCreate(tag)
      if (tag === 'a') (el as HTMLAnchorElement).click = click
      return el
    })
    const w = mount(Dashboard, { global: withAbility(ADMIN), attachTo: document.body })
    await flushPromises()
    await w.find('[data-test="backup-export"]').trigger('click')
    await flushPromises()
    expect(created.length).toBe(1)
    expect(click).toHaveBeenCalled()
    vi.mocked(document.createElement).mockRestore()

    const input = w.find<HTMLInputElement>('[data-test="backup"] input[type="file"]')
    const file = new File([JSON.stringify({ schema_version: 1, tickets: [] })], 'b.json', { type: 'application/json' })
    Object.defineProperty(input.element, 'files', { value: [file], configurable: true })
    await input.trigger('change')
    await w.find<HTMLSelectElement>('[data-test="backup"] select').setValue('overwrite')
    await w.find('[data-test="backup"] form').trigger('submit')
    await flushPromises()
    await flushPromises()
    const post = calls.find((c) => c.url.endsWith('/backup/import'))!
    expect(post.body).toEqual({ mode: 'overwrite', backup: { schema_version: 1, tickets: [] } })
    expect(w.find('[data-test="backup-result"]').text()).toBe('Imported tickets 2, tag links 1; skipped mailboxes 1.')

    // a file that is not JSON is refused before any request
    const n = calls.length
    Object.defineProperty(input.element, 'files', { value: [new File(['nope'], 'x.json')], configurable: true })
    await input.trigger('change')
    await w.find('[data-test="backup"] form').trigger('submit')
    await flushPromises()
    expect(calls.slice(n).some((c) => c.url.endsWith('/backup/import'))).toBe(false)
    w.unmount()
  })

  it('export failure is reported in the backup card', async () => {
    fetchMock((url) => (url.includes('/stats') ? { body: snapshot } : { status: 403, body: { reason: 'forbidden' } }))
    const w = mount(Dashboard, { global: withAbility(ADMIN), attachTo: document.body })
    await flushPromises()
    await w.find('[data-test="backup-export"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-test="backup"]').text()).toMatch(/permission|forbidden|not allowed/i)
    w.unmount()
  })
})

describe('ticket list live refresh', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.cookie = '__Host-csrf=tok; Secure; Path=/'
    vi.stubGlobal('EventSource', FakeSource)
    FakeSource.instances = []
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.useRealTimers()
    document.body.innerHTML = ''
  })

  it('reloads the current page once per burst of tenant ticket events and closes on unmount', async () => {
    const calls = fetchMock((url) => {
      if (url.includes('/assignable-users') || url.includes('/tags')) return { body: { items: [] } }
      return { body: { items: [], total: 0 } }
    })
    const w = mount(Tickets, { global: withAbility([{ action: 'read', subject: 'Ticket' }]), attachTo: document.body })
    await flushPromises()
    const lists = () => calls.filter((c) => c.url.startsWith('/api/ticket/v1/tickets')).length
    const before = lists()
    const src = FakeSource.instances[0]!
    src.onopen?.()
    await flushPromises()
    expect(w.text()).toMatch(/live/i)
    vi.useFakeTimers()
    src.emit('ticket.created', { ticket_id: 'n1' })
    src.emit('ticket.requester_replied', { ticket_id: 'n1' })
    src.emit('ticket.commented', { ticket_id: 'n1' })
    vi.advanceTimersByTime(500)
    vi.useRealTimers()
    await flushPromises()
    expect(lists()).toBe(before + 1)
    w.unmount()
    expect(src.closed).toBe(true)
  })
})
