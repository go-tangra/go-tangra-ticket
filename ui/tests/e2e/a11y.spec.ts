import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { base, signIn } from './helpers'

// T074 / SC-006: every ticket view inside the shell, both themes, zero serious or
// critical axe findings. Needs a full platform; skips without operator credentials.
const password = process.env.E2E_OPERATOR_PASSWORD ?? ''
const email = process.env.E2E_OPERATOR_EMAIL ?? 'ops@example.org'
const routes = ['/ticket', '/ticket/dashboard', '/ticket/rules', '/ticket/tags', '/ticket/mailboxes']

test.describe('ticket accessibility', () => {
  test.skip(!password, 'E2E_OPERATOR_PASSWORD not set')

  for (const theme of ['freya-light', 'freya-dark']) {
    test(`views are axe clean in ${theme}`, async ({ page }) => {
      await page.addInitScript((t) => localStorage.setItem('freya.theme', t), theme)
      await page.goto(base + '/')
      await signIn(page, email, password)
      for (const route of routes) {
        await page.goto(base + route)
        await expect(page.locator('main h1, main h2').first()).toBeVisible({ timeout: 15_000 })
        expect(await page.evaluate(() => document.documentElement.getAttribute('data-theme'))).toBe(theme)
        const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
        const blocking = results.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
        expect(blocking, route + ': ' + JSON.stringify(blocking.map((v) => ({ id: v.id, nodes: v.nodes.map((n) => n.target) })))).toEqual([])
        expect(await page.locator('[style]').count(), route + ': no inline styles').toBe(0)
      }
      // The ticket drawer (conversation, composer, sandboxed message frame).
      await page.goto(base + '/ticket')
      const row = page.locator('[data-test^=ticket-row-]').first()
      if (await row.isVisible()) {
        await row.click()
        await expect(page.getByTestId('ticket-drawer')).toBeVisible()
        const results = await new AxeBuilder({ page }).include('[data-test=ticket-drawer]').withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
        const blocking = results.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
        expect(blocking, 'ticket drawer: ' + JSON.stringify(blocking.map((v) => ({ id: v.id, nodes: v.nodes.map((n) => n.target) })))).toEqual([])
      }
    })
  }
})
