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
