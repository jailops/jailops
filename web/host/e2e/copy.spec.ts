import { test, expect, type Page } from '@playwright/test'
import { audit } from '../../shared/a11y'

// Copy a jail (extension jail_copy) in the demo: from the page of a jail, with its data by default (the source may run) or
// the specification only, a snapshot of the source instead of its present state, and Start. Desktop and phone.

async function open(page: Page, path: string): Promise<void> {
  await page.goto('/?demo')
  await page.waitForSelector('.shell')
  await page.evaluate((p) => {
    history.pushState({}, '', p)
    dispatchEvent(new PopStateEvent('popstate'))
  }, path)
  await page.waitForLoadState('networkidle')
}

test('a running jail is copied with its data and the page moves to the copy', async ({ page }) => {
  await open(page, '/jails/web')
  await page.getByTestId('jail-copy').click()
  const dlg = page.getByRole('dialog')
  await expect(dlg.getByRole('heading', { name: 'Copy web' })).toBeVisible()
  await expect(page.getByTestId('copy-data')).toBeChecked()
  await expect(dlg).toContainText('The jail is running: the copy is what a power loss at this moment would leave.')
  await expect(page.getByTestId('copy-snapshot')).toBeVisible()
  expect((await audit(page)).filter((v) => v.impact === 'serious' || v.impact === 'critical')).toEqual([])
  await page.getByTestId('copy-name').fill('Web 2')
  await page.getByTestId('copy-go').click()
  await expect(page.locator('#cp-name-e')).toHaveText('This name does not follow the rule above.')
  await page.getByTestId('copy-name').fill('web')
  await page.getByTestId('copy-go').click()
  await expect(page.locator('#cp-name-e')).toHaveText('This is the name of the jail you copy.')
  await page.getByTestId('copy-name').fill('web-copy')
  await page.getByTestId('copy-go').click()
  await expect(page).toHaveURL(/\/jails\/web-copy$/)
  await expect(page.locator('main h1')).toHaveText('web-copy')
  await expect(page.getByRole('dialog')).toHaveCount(0)
})

test('the specification alone, and a copy that starts', async ({ page }) => {
  await open(page, '/jails/build')
  await page.getByTestId('jail-copy').click()
  await page.getByTestId('copy-data').uncheck()
  await expect(page.getByTestId('copy-snapshot')).toHaveCount(0)
  await expect(page.getByRole('dialog')).toContainText('Only the specification: the new jail starts empty from its image.')
  await page.getByTestId('copy-start').check()
  await page.getByTestId('copy-name').fill('build2')
  await page.getByTestId('copy-go').click()
  await expect(page).toHaveURL(/\/jails\/build2$/)
  await expect(page.locator('main h1')).toHaveText('build2')
})

test('a snapshot of the source is offered and a taken name is refused in words', async ({ page }) => {
  await open(page, '/jails/web')
  await page.getByTestId('jail-copy').click()
  const snap = page.getByTestId('copy-snapshot')
  const options = await snap.locator('option').allTextContents()
  expect(options[0]).toBe('now')
  if (options.length > 1) await snap.selectOption({ index: 1 })
  await page.getByTestId('copy-name').fill('build')
  await page.getByTestId('copy-go').click()
  await expect(page.getByRole('dialog')).toContainText('already exists')
  await expect(page).toHaveURL(/\/jails\/web$/)
})
