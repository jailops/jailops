import { test, expect, type Page } from '@playwright/test'
import { audit } from '../../shared/a11y'

// The Live usage page in demo mode: the host numbers, the sortable table and the project filter.

async function open(page: Page, path: string): Promise<void> {
  await page.goto('/?demo')
  await page.waitForSelector('.shell')
  await page.evaluate((p) => { history.pushState({}, '', p); dispatchEvent(new PopStateEvent('popstate')) }, path)
  await page.waitForLoadState('networkidle')
}

test('live usage shows the host and the running jails', async ({ page }) => {
  await open(page, '/top')
  await expect(page.getByRole('heading', { name: 'Live usage' })).toBeVisible()
  await expect(page.getByTestId('top-host')).toBeVisible()
  await expect(page.getByTestId('top-note')).toContainText('racct is off')
  expect(await page.locator('[data-test=top-table] tbody tr').count()).toBeGreaterThan(0)
  await audit(page)
})

test('live usage sorts by name and pauses', async ({ page }) => {
  await open(page, '/top')
  await page.getByTestId('top-sort').selectOption('name')
  const names = await page.locator('[data-test=top-table] tbody tr').evaluateAll((r) => r.map((x) => `${x.getAttribute('data-project')}/${x.getAttribute('data-jail')}`))
  expect(names).toEqual([...names].sort())
  await page.getByRole('button', { name: 'Pause' }).click()
  await expect(page.getByTestId('top-link')).toHaveText('Paused')
})
