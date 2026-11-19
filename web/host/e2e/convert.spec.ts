import { test, expect, type Page } from '@playwright/test'
import { audit } from '../../shared/a11y'

// The layout of a jail's root (extension jail_convert) in the demo: a preview that is a dry run on the server, then the
// conversion; and the cpuset key of the limits form. Desktop and phone.

async function open(page: Page, path: string): Promise<void> {
  await page.goto('/?demo')
  await page.waitForSelector('.shell')
  await page.evaluate((p) => {
    history.pushState({}, '', p)
    dispatchEvent(new PopStateEvent('popstate'))
  }, path)
  await page.waitForLoadState('networkidle')
}

test('a thin jail is converted to thick after a preview, and back to thin', async ({ page }) => {
  await open(page, '/jails/web')
  await expect(page.getByRole('row', { name: /Layout/ })).toContainText('thin')
  await page.getByTestId('jail-convert').click()
  const dlg = page.getByRole('dialog')
  await expect(dlg.getByRole('heading', { name: 'Convert the root of web' })).toBeVisible()
  await expect(page.getByTestId('convert-fromto')).toHaveText('thin to thick')
  await expect(page.getByTestId('convert-go')).toBeDisabled()
  await expect(page.getByTestId('convert-running')).toBeVisible()
  await page.getByTestId('convert-preview').click()
  await expect(page.getByTestId('convert-result')).toContainText('Keeps its data:')
  await expect(page.getByTestId('convert-result')).toContainText('/usr/local')
  await expect(page.getByTestId('convert-result')).toContainText('Fold the persistent datasets into root.new')
  expect((await audit(page)).filter((v) => v.impact === 'serious' || v.impact === 'critical')).toEqual([])
  await page.getByTestId('convert-go').click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByRole('row', { name: /Layout/ })).toContainText('thick', { timeout: 10_000 })

  await page.getByTestId('jail-convert').click()
  await expect(page.getByTestId('convert-fromto')).toHaveText('thick to thin')
  await expect(page.getByTestId('convert-image')).toBeVisible()
  await page.getByTestId('convert-preview').click()
  await expect(page.getByTestId('convert-result')).toContainText('Replaces:')
  // the choice of the generation invalidates the preview
  const opts = await page.getByTestId('convert-image').locator('option').count()
  if (opts > 1) {
    await page.getByTestId('convert-image').selectOption({ index: 1 })
    await expect(page.getByTestId('convert-result')).toContainText('The choice changed since the preview')
    await expect(page.getByTestId('convert-go')).toBeDisabled()
    await page.getByTestId('convert-preview').click()
  }
  await page.getByTestId('convert-go').click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByRole('row', { name: /Layout/ })).toContainText('thin', { timeout: 10_000 })
})

test('a Linux jail has no layout to change', async ({ page }) => {
  await open(page, '/jails/alpine-app')
  await expect(page.getByRole('row', { name: /Layout/ })).toContainText('thick')
  await expect(page.getByTestId('jail-convert')).toHaveCount(0)
})

test('the cpuset key is in the limits form with a check of its own', async ({ page }) => {
  await open(page, '/jails/web')
  await page.locator('[data-tab="config"]').click()
  const key = page.locator('[data-key="limits.cpuset"]')
  await expect(key.getByTestId('restart-badge')).toBeVisible()
  await key.locator('input').fill('3-')
  await expect(key.getByTestId('config-error')).toHaveText('Enter CPU numbers and ranges such as 2,3 or 0-3,6.')
  await key.locator('input').fill('2,3')
  await expect(key.getByTestId('config-error')).toHaveCount(0)
  await page.getByTestId('config-save').click()
  await expect(page.getByTestId('restart-banner')).toContainText('limits.cpuset')
})
