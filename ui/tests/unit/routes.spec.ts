import { describe, expect, it } from 'vitest'
import { routes } from '@/remote/routes'
import { nav } from '@/remote/nav'

describe('ticket remote', () => {
  it('exports the module routes, all tagged with the ticket module', () => {
    expect(routes.map((r) => r.path)).toEqual(['/ticket', '/ticket/dashboard', '/ticket/rules', '/ticket/tags', '/ticket/mailboxes'])
    for (const r of routes) expect(r.meta?.module).toBe('ticket')
    expect(nav()).toEqual([])
  })
  it('every route lazily resolves a component', async () => {
    for (const r of routes) {
      const load = r.component as () => Promise<{ default: unknown }>
      expect((await load()).default).toBeTruthy()
    }
  })
})
