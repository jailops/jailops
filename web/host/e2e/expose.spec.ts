import { test, expect, type Page } from '@playwright/test'
import { audit } from '../../shared/a11y'

// Expose a jail in demo mode: the Firewall tab of a jail with an IPv4 and an IPv6 address, the Expose dialog (the request of
// `jailops expose`), the list of exposed ports and Unexpose.

async function open(page: Page, path: string): Promise<void> {
  await page.goto('/?demo')
  await page.waitForSelector('.shell')
  await page.evaluate((p) => { history.pushState({}, '', p); dispatchEvent(new PopStateEvent('popstate')) }, path)
  await page.waitForLoadState('networkidle')
}

test('ports are exposed over both families and taken away again', async ({ page }) => {
  await open(page, '/jails/proxy?tab=reach')
  await expect(page.getByTestId('expose-none')).toBeVisible()
  await page.getByTestId('expose-open').click()
  const dlg = page.getByRole('dialog')
  await dlg.getByTestId('expose-ports').fill('80,443')
  expect(await audit(page)).toEqual([])
  await dlg.getByTestId('expose-preview').click()
  await expect(dlg.getByTestId('expose-result')).toContainText('IPv6')
  await expect(dlg.getByTestId('expose-result')).toContainText('IPv4')
  await expect(page.locator('[data-expose]')).toHaveCount(0)
  await dlg.getByTestId('expose-go').click()
  await expect(dlg.getByTestId('expose-result')).toContainText('the host forwards 80->80, 443->443')
  await dlg.getByTestId('expose-close').click()
  await expect(page.locator('[data-expose="80,443/tcp"]')).toBeVisible()
  await expect(page.getByTestId('reach-rows')).toContainText('expose')
  await page.getByTestId('unexpose-80,443/tcp').click()
  await expect(page.getByTestId('unexpose-dialog')).toContainText('no longer reaches 80,443/tcp')
  await page.getByRole('dialog').getByTestId('confirm').click()
  await expect(page.locator('[data-expose]')).toHaveCount(0)
})

test('bad ports are refused before anything is sent', async ({ page }) => {
  await open(page, '/jails/proxy?tab=reach')
  await page.getByTestId('expose-open').click()
  const dlg = page.getByRole('dialog')
  await dlg.getByTestId('expose-ports').fill('http')
  await dlg.getByTestId('expose-go').click({ force: true })
  await expect(dlg).toContainText('Name the ports')
  await expect(page.locator('[data-expose]')).toHaveCount(0)
})
