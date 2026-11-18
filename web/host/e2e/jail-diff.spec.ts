import { test, expect, type Page } from '@playwright/test'
import { audit } from '../../shared/a11y'

// The Changes tab of a jail in demo mode: what the jail holds on top of its image.

async function open(page: Page, path: string): Promise<void> {
  await page.goto('/?demo')
  await page.waitForSelector('.shell')
  await page.evaluate((p) => { history.pushState({}, '', p); dispatchEvent(new PopStateEvent('popstate')) }, path)
  await page.waitForLoadState('networkidle')
}

test('the Changes tab lists the changes of every dataset', async ({ page }) => {
  await open(page, '/jails/proxy?tab=changes')
  const tab = page.getByTestId('changes-tab')
  await expect(tab).toContainText('since freebsd-15-1#g4')
  await expect(tab.locator('[data-dataset="etc"]')).toContainText('/etc/rc.conf')
  await expect(tab.getByTestId('diff-more')).toBeVisible()
  expect(await audit(page)).toEqual([])
  await tab.getByTestId('diff-all').click()
  await expect(tab.getByTestId('diff-more')).toHaveCount(0)
  await expect(tab).toContainText('/obj/file6.o')
  await tab.getByTestId('diff-packages').check()
  await expect(tab.getByTestId('diff-package-list')).toContainText('nginx-1.30.6,3')
})
