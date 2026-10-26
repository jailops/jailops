import { test, expect } from '@playwright/test'

test('demo mode renders the overview and names the app', async ({ page }) => {
  const errors: string[] = []
  page.on('console', (m) => m.type() === 'error' && errors.push(m.text()))
  await page.goto('/?demo')
  await expect(page.locator('main h1')).toHaveText('Overview')
  await expect(page).toHaveTitle(/__TITLE__/)
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0)
  expect(errors).toEqual([])
})
