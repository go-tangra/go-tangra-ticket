import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { abilitiesPlugin } from '@casl/vue'
import { createMongoAbility } from '@casl/ability'
import { useConfirm } from '@go-tangra/ui'
import Mailboxes from '@/views/mailboxes/index.vue'
import { mailboxSchema } from '@/schemas'
import { useMailboxes } from '@/stores/mailboxes'
import type { Mailbox } from '@/api/types'

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
const MANAGER = [{ action: 'manage', subject: 'TicketMailbox' }]
const withAbility = (rules: { action: string; subject: string }[]) => ({ plugins: [[abilitiesPlugin, createMongoAbility(rules), { useGlobalProperties: true }]] as never })
const mb: Mailbox = { id: 'm1', address: 'support@acme.test', display_name: 'Acme Support', active: true, auto_ack: true, auto_ack_template: 'Hi {{name}}', created_at: '2026-09-01T12:00:00Z', updated_at: '2026-09-01T12:00:00Z' }

function api(state: { items: Mailbox[]; refs: boolean }) {
  return (url: string, method: string, body: unknown): { status?: number; body?: unknown } => {
    const path = url.split('?')[0]
    if (path === '/api/ticket/v1/mailboxes' && method === 'GET') {
      const q = new URL(url, 'https://x').searchParams
      return { body: { items: state.items, total: state.items.length, page: 1, page_size: 25, sort: q.get('sort') ?? 'address', order: q.get('order') ?? 'asc' } }
    }
    if (url === '/api/ticket/v1/mailboxes' && method === 'POST') {
      const m = { ...mb, id: 'm2', ...(body as object) } as Mailbox
      state.items = [...state.items, m]
      return { status: 201, body: m }
    }
    if (url.startsWith('/api/ticket/v1/mailboxes/m1') && method === 'PUT') return { body: { ...mb, ...(body as object) } }
    if (url.startsWith('/api/ticket/v1/mailboxes/m1') && method === 'DELETE') {
      if (state.refs && !url.includes('force=true')) return { status: 409, body: { reason: 'conflict' } }
      state.items = state.items.filter((m) => m.id !== 'm1')
      return { status: 204 }
    }
    return { status: 404, body: { reason: 'not_found' } }
  }
}

describe('mailbox schema', () => {
  it('address required and normalised; defaults; template bounded', () => {
    expect(mailboxSchema.safeParse({ address: ' Support@Acme.Test ' }).data).toEqual({ address: 'support@acme.test', active: true, auto_ack: false })
    expect(mailboxSchema.safeParse({ address: 'nope' }).success).toBe(false)
    expect(mailboxSchema.safeParse({ address: '' }).success).toBe(false)
    expect(mailboxSchema.safeParse({ address: 'a@b.org', auto_ack_template: 'x'.repeat(8193) }).success).toBe(false)
    expect(mailboxSchema.safeParse({ address: 'a@b.org', display_name: 'x'.repeat(201) }).success).toBe(false)
  })
})

describe('mailboxes', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.cookie = '__Host-csrf=tok; Secure; Path=/'
    ;(globalThis as unknown as { __vw: number }).__vw = 1280
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it('store: list, create, update, delete with force', async () => {
    const calls = fetchMock(api({ items: [mb], refs: true }))
    const s = useMailboxes()
    await s.list()
    expect(s.items.map((m) => m.address)).toEqual(['support@acme.test'])
    expect(s.total).toBe(1)
    await s.create({ address: 'help@acme.test' })
    await flushPromises()
    expect(s.items.length).toBe(2)
    expect(s.total).toBe(2)
    await s.update('m1', { active: false })
    expect(calls.at(-1)).toMatchObject({ method: 'PUT', url: '/api/ticket/v1/mailboxes/m1', body: { active: false } })
    await expect(s.remove('m1')).rejects.toThrow()
    await s.remove('m1', true)
    expect(calls.filter((c) => c.method !== 'GET').at(-1)!.url).toBe('/api/ticket/v1/mailboxes/m1?force=true')
    expect(s.items.map((m) => m.id)).toEqual(['m2'])
    await flushPromises()
    expect(s.total).toBe(1)
  })

  it('view: rows, create through a drawer, delete with conflict offers detaching', async () => {
    const calls = fetchMock(api({ items: [mb], refs: true }))
    const w = mount(Mailboxes, { global: withAbility(MANAGER), attachTo: document.body })
    await flushPromises()
    expect(Object.fromEntries(new URL(calls[0]!.url, 'https://x').searchParams)).toEqual({ page: '1', page_size: '25', sort: 'address', order: 'asc' })
    // Only the server's sort fields are sortable headers; a header click sorts server-side.
    const sortButtons = w.findAll('th button').map((b) => b.text())
    expect(sortButtons).toEqual(['Address', 'Display name'])
    await w.findAll('th button')[1]!.trigger('click')
    await flushPromises()
    expect(calls.at(-1)!.url).toContain('sort=name&order=asc')
    const row = w.find('[data-test="mailbox-row-m1"]')
    expect(row.text()).toContain('support@acme.test')
    expect(row.text()).toContain('Active')
    expect(row.text()).toContain('On')
    expect(w.find('[style]').exists()).toBe(false)

    await w.find('[data-test="mailbox-new"]').trigger('click')
    await flushPromises()
    const drawer = document.body.querySelector('[data-test="mailbox-drawer"]') ?? document.body
    const addr = drawer.querySelector<HTMLInputElement>('input[data-field=address]')!
    addr.value = 'Help@Acme.Test'
    addr.dispatchEvent(new Event('input'))
    ;(Array.from(drawer.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Save') as HTMLButtonElement).click()
    await flushPromises()
    expect(calls.find((c) => c.method === 'POST')).toMatchObject({ url: '/api/ticket/v1/mailboxes', body: { address: 'help@acme.test', active: true, auto_ack: false, display_name: '', auto_ack_template: '' } })

    const confirm = useConfirm()
    await w.find('[data-test="mailbox-delete-m1"]').trigger('click')
    await flushPromises()
    confirm.answer(true)
    await flushPromises()
    expect(calls.at(-1)!.url).toBe('/api/ticket/v1/mailboxes/m1')
    confirm.answer(true)
    await flushPromises()
    expect(calls.filter((c) => c.method !== 'GET').at(-1)).toMatchObject({ url: '/api/ticket/v1/mailboxes/m1?force=true', method: 'DELETE' })
    expect(w.find('[data-test="mailbox-row-m1"]').exists()).toBe(false)
    w.unmount()
  })

  it('view: without mailboxes:manage there are no write actions', async () => {
    fetchMock(api({ items: [mb], refs: false }))
    const w = mount(Mailboxes, { global: withAbility([]), attachTo: document.body })
    await flushPromises()
    expect(w.find('[data-test="mailbox-new"]').exists()).toBe(false)
    expect(w.find('[data-test="mailbox-edit-m1"]').exists()).toBe(false)
    w.unmount()
  })
})
