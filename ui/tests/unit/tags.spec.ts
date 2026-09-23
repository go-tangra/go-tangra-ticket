import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { abilitiesPlugin } from '@casl/vue'
import { createMongoAbility } from '@casl/ability'
import { useConfirm } from '@freya/ui'
import Tags from '@/views/tags/index.vue'
import TicketDrawer from '@/views/tickets/drawer.vue'
import { TAG_COLORS, autoColor, isTagColor, tagColor } from '@/views/tags/colors'
import { tagSchema } from '@/schemas'
import { useTags } from '@/stores/tags'
import type { Tag, Ticket } from '@/api/types'

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
const withAbility = (rules: { action: string; subject: string }[]) => ({ plugins: [[abilitiesPlugin, createMongoAbility(rules), { useGlobalProperties: true }]] as never })
const TAGGER = [{ action: 'read', subject: 'TicketTag' }, { action: 'manage', subject: 'TicketTag' }]
const AGENT = [{ action: 'read', subject: 'Ticket' }, { action: 'update', subject: 'Ticket' }]

const billing: Tag = { id: 'g1', name: 'Billing', kind: 'tag', color: 'warning', description: 'Money' }
const hardware: Tag = { id: 'g2', name: 'Hardware', kind: 'category' }

describe('tag colours', () => {
  it('auto colour is deterministic, case-insensitive and a palette name', () => {
    expect(autoColor('Billing')).toBe(autoColor('billing'))
    expect(autoColor(' billing ')).toBe(autoColor('BILLING'))
    const seen = new Set(['a', 'b', 'c', 'network', 'printer', 'invoice', 'vip', 'spam', 'hr', 'legal'].map(autoColor))
    expect(seen.size).toBeGreaterThan(2)
    for (const c of seen) expect(TAG_COLORS).toContain(c)
    expect(seen.has('neutral')).toBe(false)
  })
  it('an explicit palette colour wins; hex or empty falls back to automatic', () => {
    expect(tagColor({ name: 'x', color: 'error' })).toBe('error')
    expect(tagColor({ name: 'x', color: '#ff0000' })).toBe(autoColor('x'))
    expect(tagColor({ name: 'x' })).toBe(autoColor('x'))
    expect(isTagColor('info')).toBe(true)
    expect(isTagColor('red')).toBe(false)
  })
})

describe('tag schema', () => {
  it('name required and bounded; kind default tag; colour a palette name or automatic', () => {
    expect(tagSchema.safeParse({ name: ' Billing ' }).data).toEqual({ name: 'Billing', kind: 'tag' })
    expect(tagSchema.safeParse({ name: 'x', color: '' }).data?.color).toBeUndefined()
    expect(tagSchema.safeParse({ name: 'x', color: 'info', kind: 'category' }).success).toBe(true)
    expect(tagSchema.safeParse({ name: '' }).success).toBe(false)
    expect(tagSchema.safeParse({ name: 'x'.repeat(101) }).success).toBe(false)
    expect(tagSchema.safeParse({ name: 'x', color: 'red; x:y' }).success).toBe(false)
    expect(tagSchema.safeParse({ name: 'x', kind: 'label' }).success).toBe(false)
  })
})

function api(state: { items: Tag[] }) {
  return (url: string, method: string, body: unknown): { status?: number; body?: unknown } => {
    const path = url.replace(/^\/api\/ticket\/v1\//, '')
    if (path.startsWith('tags') && method === 'GET') {
      const kind = new URL(url, 'https://x').searchParams.get('kind')
      return { body: { items: state.items.filter((t) => !kind || t.kind === kind) } }
    }
    if (path === 'tags' && method === 'POST') {
      if (state.items.some((t) => t.name.toLowerCase() === String((body as Tag).name).toLowerCase())) return { status: 409, body: { reason: 'conflict' } }
      const t = { id: 'g9', kind: 'tag', ...(body as object) } as Tag
      state.items = [...state.items, t]
      return { status: 201, body: t }
    }
    if (path.startsWith('tags/') && method === 'PUT') return { body: { ...state.items.find((t) => path.endsWith(t.id)), ...(body as object) } }
    if (path.startsWith('tags/') && method === 'DELETE') return { status: 204 }
    return { status: 404, body: { reason: 'not_found' } }
  }
}

describe('tags', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.cookie = '__Host-csrf=tok; Secure; Path=/'
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it('store: list by kind, create keeps order, update, delete', async () => {
    const calls = fetchMock(api({ items: [billing, hardware] }))
    const s = useTags()
    await s.list('category')
    expect(calls.at(-1)!.url).toBe('/api/ticket/v1/tags?kind=category')
    expect(s.items.map((t) => t.id)).toEqual(['g2'])
    await s.list()
    await s.create({ name: 'Alpha' })
    expect(s.items.map((t) => t.name)).toEqual(['Hardware', 'Alpha', 'Billing'])
    await s.update('g1', { name: 'Invoices' })
    expect(calls.at(-1)).toMatchObject({ method: 'PUT', url: '/api/ticket/v1/tags/g1', body: { name: 'Invoices' } })
    await s.remove('g1')
    expect(s.items.some((t) => t.id === 'g1')).toBe(false)
    await expect(s.create({ name: 'hardware' })).rejects.toThrow()
  })

  it('view: coloured chips, kind filter, create in a drawer (no kind on edit), delete asks first', async () => {
    const calls = fetchMock(api({ items: [billing, hardware] }))
    const w = mount(Tags, { global: withAbility(TAGGER), attachTo: document.body })
    await flushPromises()
    expect(w.find('[data-test="tag-chip-g1"]').classes()).toContain('badge-warning')
    expect(w.find('[data-test="tag-chip-g2"]').classes()).toContain('badge-' + autoColor('Hardware'))
    expect(w.find('[data-test="tag-row-g2"]').text()).toContain('Category')
    expect(w.find('[style]').exists()).toBe(false)

    const filter = w.find<HTMLSelectElement>('select[data-field="tag-kind-filter"]')
    filter.element.value = 'category'
    await filter.trigger('change')
    await flushPromises()
    expect(calls.at(-1)!.url).toBe('/api/ticket/v1/tags?kind=category')

    await w.find('[data-test="tag-new"]').trigger('click')
    await flushPromises()
    const drawer = document.body.querySelector('[data-test="tag-drawer"]')!
    expect(drawer.querySelector('select[data-field=kind]')).not.toBeNull()
    const name = drawer.querySelector<HTMLInputElement>('input[data-field=name]')!
    name.value = 'Network'
    name.dispatchEvent(new Event('input'))
    ;(Array.from(drawer.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Save') as HTMLButtonElement).click()
    await flushPromises()
    expect(calls.find((c) => c.method === 'POST')).toMatchObject({ url: '/api/ticket/v1/tags', body: { name: 'Network', kind: 'category', color: '', description: '' } })

    await w.find('[data-test="tag-edit-g2"]').trigger('click')
    await flushPromises()
    const edit = document.body.querySelector('[data-test="tag-drawer"]')!
    expect(edit.querySelector('select[data-field=kind]')).toBeNull()

    const confirm = useConfirm()
    await w.find('[data-test="tag-delete-g2"]').trigger('click')
    await flushPromises()
    confirm.answer(true)
    await flushPromises()
    expect(calls.at(-1)).toMatchObject({ url: '/api/ticket/v1/tags/g2', method: 'DELETE' })
    w.unmount()
  })

  it('view: readers get no write actions', async () => {
    fetchMock(api({ items: [billing] }))
    const w = mount(Tags, { global: withAbility([{ action: 'read', subject: 'TicketTag' }]), attachTo: document.body })
    await flushPromises()
    expect(w.find('[data-test="tag-new"]').exists()).toBe(false)
    expect(w.find('[data-test="tag-edit-g1"]').exists()).toBe(false)
    w.unmount()
  })

  it('ticket drawer: toggling a tag replaces the ticket tag set', async () => {
    let ticket: Ticket = { id: 't1', subject: 'S', status: 'open', priority: 'normal', source: 'manual', comment_count: 0, tags: [billing], created_at: '2026-09-01T12:00:00Z', updated_at: '2026-09-01T12:00:00Z' }
    const calls = fetchMock((url, method, body) => {
      const path = url.replace(/^\/api\/ticket\/v1\//, '').split('?')[0]!
      if (path === 'tags') return { body: { items: [billing, hardware] } }
      if (path === 'assignable-users') return { body: { items: [] } }
      if (path === 'tickets/t1/tags') {
        const ids = (body as { tag_ids: string[] }).tag_ids
        ticket = { ...ticket, tags: [billing, hardware].filter((t) => ids.includes(t.id)) }
        return { body: ticket }
      }
      if (path === 'tickets/t1/comments') return { body: { items: [] } }
      if (path === 'tickets/t1/body') return { body: { html_sanitized: '', text: '' } }
      return { body: ticket }
    })
    const w = mount(TicketDrawer, { props: { ticketId: 't1' }, global: withAbility(AGENT), attachTo: document.body })
    await flushPromises()
    const panel = document.body.querySelector('[data-test="ticket-drawer"]')!
    const toggle = (id: string) => panel.querySelector<HTMLButtonElement>(`[data-test="ticket-tag-toggle-${id}"]`)!
    expect(toggle('g1').getAttribute('aria-pressed')).toBe('true')
    expect(toggle('g2').getAttribute('aria-pressed')).toBe('false')
    toggle('g2').click()
    await flushPromises()
    expect(calls.at(-1)).toMatchObject({ url: '/api/ticket/v1/tickets/t1/tags', method: 'POST', body: { tag_ids: ['g1', 'g2'] } })
    expect(panel.querySelector('[data-test="ticket-tag-g2"]')).not.toBeNull()
    toggle('g1').click()
    await flushPromises()
    expect(calls.at(-1)).toMatchObject({ body: { tag_ids: ['g2'] } })
    expect(w.emitted('changed')).toBeTruthy()
    w.unmount()

    fetchMock(() => ({ body: { items: [billing] } }))
    const ro = mount(TicketDrawer, { props: { ticketId: 't1' }, global: withAbility([{ action: 'read', subject: 'Ticket' }]), attachTo: document.body })
    await flushPromises()
    expect(document.body.querySelector('[data-test="ticket-tags-editor"]')).toBeNull()
    ro.unmount()
  })
})
