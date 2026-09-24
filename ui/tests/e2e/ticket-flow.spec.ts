import { expect, test, type Page } from '@playwright/test'
import { base, signIn } from './helpers'

// Quickstart flow for the ticket remote at the three reference widths:
// mailboxes → tags → rules builder → new ticket → drawer (status/priority/
// assignee, tag toggle) → internal note → reply. Needs a full platform; skips
// without operator credentials.
const password = process.env.E2E_OPERATOR_PASSWORD ?? ''
const email = process.env.E2E_OPERATOR_EMAIL ?? 'ops@example.org'
const viewports = [{ name: 'phone', width: 320, height: 640 }, { name: 'tablet', width: 768, height: 1024 }, { name: 'desktop', width: 1280, height: 800 }]

async function openNav(page: Page, entry: string): Promise<void> {
  const burger = page.getByRole('button', { name: 'Open navigation' })
  if (await burger.isVisible()) await burger.click()
  const g = page.getByTestId('nav-group-ticket')
  if ((await g.getAttribute('aria-expanded')) !== 'true') await g.click()
  await page.getByTestId('nav-ticket').filter({ hasText: entry }).first().click()
}

test.describe('ticket remote', () => {
  test.skip(!password, 'E2E_OPERATOR_PASSWORD not set')
  for (const vp of viewports) {
    test(`${vp.name}: mailboxes, tags, rules, ticket drawer, note + reply`, async ({ page }) => {
      const run = `${vp.name}-${Date.now().toString(36)}`
      await page.setViewportSize({ width: vp.width, height: vp.height })
      const violations: string[] = []
      await page.addInitScript(() => document.addEventListener('securitypolicyviolation', (e) => console.error('CSP:' + (e as SecurityPolicyViolationEvent).violatedDirective)))
      page.on('console', (m) => { if (m.text().startsWith('CSP:')) violations.push(m.text()) })
      await page.goto(base + '/')
      await signIn(page, email, password)

      // Mailbox: create one (the inbound edge routes by this address).
      await openNav(page, 'Mailboxes')
      await expect(page.getByTestId('mailboxes-table')).toBeVisible()
      await page.getByTestId('mailbox-new').click()
      const mbox = page.getByTestId('mailbox-drawer')
      await mbox.locator('input[data-field=address]').fill(`support-${run}@example.org`)
      await mbox.getByRole('button', { name: 'Save', exact: true }).click()
      await expect(page.getByTestId('mailboxes-table')).toContainText(`support-${run}@example.org`)

      // Tag: create one to toggle on the ticket later.
      await openNav(page, 'Tags')
      await expect(page.getByTestId('tags-table')).toBeVisible()
      await page.getByTestId('tag-new').click()
      const tagDrawer = page.getByTestId('tag-drawer')
      await tagDrawer.locator('input[data-field=name]').fill(`e2e-${run}`)
      await tagDrawer.getByRole('button', { name: 'Save', exact: true }).click()
      await expect(page.getByTestId('tags-table')).toContainText(`e2e-${run}`)

      // Rules builder: an invalid advanced expression is refused with a reason,
      // a condition rule saves and can be dry-run.
      await openNav(page, 'Rules')
      await expect(page.getByTestId('rules-table')).toBeVisible()
      await page.getByTestId('rule-new').click()
      const rule = page.getByTestId('rule-drawer')
      await rule.locator('input[data-field=rule-name]').fill(`urgent-${run}`)
      await rule.getByTestId('rule-advanced').click()
      await rule.locator('textarea[data-field=rule-expression]').fill('subject.contains(')
      await rule.getByTestId('rule-save').click()
      await expect(rule.getByTestId('rule-error').or(rule.getByTestId('rule-issues')).first()).toBeVisible()
      await rule.getByTestId('rule-advanced').click()
      await rule.getByTestId('rule-condition-add').click()
      await rule.locator('input[data-field=cond-value-0]').fill('urgent')
      await rule.getByTestId('rule-save').click()
      await expect(page.getByTestId('rules-table')).toContainText(`urgent-${run}`)

      // New ticket → drawer.
      await openNav(page, 'Tickets')
      await expect(page.getByTestId('tickets-table')).toBeVisible()
      await page.getByTestId('ticket-new').click()
      const create = page.getByTestId('ticket-create')
      await create.locator('input[data-field=subject]').fill(`Printer on fire ${run}`)
      await create.locator('input[data-field=requester_email]').fill('requester@example.org')
      await create.getByRole('button', { name: 'Create', exact: true }).click()
      const drawer = page.getByTestId('ticket-drawer')
      await expect(drawer).toBeVisible()
      await expect(drawer).toContainText(`Printer on fire ${run}`)

      // Status, priority, assignee.
      await drawer.locator('select#ticket-status').selectOption('in_progress')
      await expect(drawer.getByTestId('ticket-chips')).toContainText(/in progress/i)
      await drawer.locator('select#ticket-priority').selectOption('high')
      await expect(drawer.getByTestId('ticket-chips')).toContainText(/high/i)
      const assignee = drawer.locator('select#ticket-assignee')
      if (await assignee.isEnabled() && (await assignee.locator('option').count()) > 1) {
        await assignee.selectOption({ index: 1 })
      }

      // Tag toggle.
      const toggle = drawer.locator('[data-test^=ticket-tag-toggle-]').filter({ hasText: `e2e-${run}` })
      if (await toggle.isVisible()) {
        await toggle.click()
        await expect(toggle).toHaveAttribute('aria-pressed', 'true')
      }

      // Internal note (never emailed), then a public reply.
      await drawer.getByTestId('compose-note').click()
      await drawer.locator('textarea#ticket-compose').fill('Checked the toner, still smoking.')
      await drawer.getByTestId('compose-submit').click()
      await expect(drawer.getByTestId('ticket-timeline')).toContainText('Checked the toner')
      await expect(drawer.getByTestId('ticket-timeline')).toContainText('Internal')
      await drawer.getByTestId('compose-reply').click()
      await drawer.locator('textarea#ticket-compose').fill('We are on it.')
      await drawer.getByTestId('compose-submit').click()
      await expect(drawer.getByTestId('ticket-timeline')).toContainText('We are on it.')
      await expect(drawer.getByTestId('comment-delivery-failed')).toHaveCount(0)

      // History tab records the changes.
      await drawer.getByRole('tab', { name: /History/ }).click()
      await expect(drawer.getByTestId('ticket-history')).toContainText(/in progress/i)
      await page.keyboard.press('Escape')

      await openNav(page, 'Dashboard')
      await expect(page.getByTestId('stats-tiles')).toBeVisible()

      expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0)
      expect(await page.locator('main [style]').count()).toBe(0)
      expect(violations).toEqual([])
    })
  }

  test('hostile HTML renders inert in a sandboxed frame', async ({ page }) => {
    await page.goto(base + '/')
    await signIn(page, email, password)
    await page.goto(base + '/ticket')
    await expect(page.getByTestId('tickets-table')).toBeVisible()
    const first = page.locator('[data-test^=ticket-row-]').first()
    test.skip(!(await first.isVisible()), 'no tickets to open')
    await first.click()
    const frame = page.getByTestId('message-frame')
    if (await frame.isVisible()) {
      const sandbox = (await frame.getAttribute('sandbox')) ?? ''
      expect(sandbox).not.toContain('allow-scripts')
      expect(sandbox).not.toContain('allow-same-origin')
    }
  })
})
