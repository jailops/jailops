import { test, expect, type Page } from '@playwright/test'

// What reaches a jail: the Firewall tab of a jail and the Exposed tab of the firewall page. Demo mode.

async function open(page: Page, path: string): Promise<void> {
  await page.goto('/?demo')
  await page.waitForSelector('.shell')
  await page.evaluate((p) => { history.pushState({}, '', p); dispatchEvent(new PopStateEvent('popstate')) }, path)
  await page.waitForLoadState('networkidle')
}

test('the Exposed tab lists what the Internet reaches, with the rule that opens it', async ({ page }) => {
  await open(page, '/firewall')
  await page.locator('[data-tab="exposed"]').click()
  await expect(page).toHaveURL(/tab=exposed/)
  await expect(page.getByTestId('exposed-panel')).toBeVisible()
  await expect(page.getByTestId('exposed-rows')).toContainText('policy web-default rule')
})

test('a jail has a Firewall tab with what reaches it and what it reaches', async ({ page }) => {
  await open(page, '/jails')
  const first = page.locator('a[href^="/jails/"]').first()
  await first.click()
  await page.locator('[data-tab="reach"]').click()
  await expect(page.getByTestId('reach-tab')).toBeVisible()
  await expect(page.getByTestId('reach-rows').locator('tr[data-source="internet"]').first()).toBeVisible()
  await expect(page.getByTestId('reach-out')).toContainText('Internet')
})

test('the Firewall tab says whether pf has the rules the policies render', async ({ page }) => {
  await open(page, '/jails/web')
  await page.locator('[data-tab="reach"]').click()
  const applied = page.getByTestId('firewall-applied')
  await expect(applied).toBeVisible()
  await expect(applied).toHaveAttribute('data-status', 'True')
  await expect(applied).toContainText('are loaded and pf evaluates them')
})

test('the Firewall tab lists the policies of the jail in order and attaches and detaches one at a time', async ({ page }) => {
  await open(page, '/jails')
  await page.locator('a[href^="/jails/"]').first().click()
  await page.locator('[data-tab="reach"]').click()
  const list = page.getByTestId('policies-list')
  await expect(list.locator('tr[data-policy="web-default"]')).toBeVisible()
  await page.getByTestId('policy-pick').selectOption('db-internal')
  await page.getByTestId('policy-attach-go').click()
  const row = list.locator('tr[data-policy="db-internal"]')
  await expect(row).toBeVisible()
  await expect(row).toHaveAttribute('data-source', 'jail')
  // the policy that was there stays, in front of the one just attached, which is no longer offered
  await expect(list.locator('tr[data-policy="web-default"]')).toBeVisible()
  await expect(list.locator('tr').nth(1)).toHaveAttribute('data-policy', 'web-default')
  await expect(list.locator('tr').nth(2)).toHaveAttribute('data-policy', 'db-internal')
  await expect(page.getByTestId('policy-pick').locator('option[value="db-internal"]')).toHaveCount(0)
  await page.getByTestId('detach-db-internal').click()
  await expect(row).toHaveCount(0)
  await expect(page.getByTestId('policy-pick').locator('option[value="db-internal"]')).toHaveCount(1)
})
