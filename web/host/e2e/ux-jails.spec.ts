import { test, expect, type Page } from '@playwright/test'

// The confirmation tiers, the name rule, the tab address and the phone cards of the jail pages.

async function open(page: Page, path: string): Promise<void> {
  await page.goto('/?demo')
  await page.waitForSelector('.shell')
  if (path !== '/') await page.evaluate((p) => { history.pushState({}, '', p); dispatchEvent(new PopStateEvent('popstate')) }, path)
  await page.waitForLoadState('networkidle')
}

test('deleting a jail needs its name typed and lists what is destroyed', async ({ page }) => {
  await open(page, '/jails/proxy')
  await page.getByTestId('jail-delete').click()
  const dlg = page.getByTestId('confirm-dialog')
  await expect(dlg).toHaveAttribute('data-tier', '3')
  await expect(dlg.getByTestId('confirm-items')).toContainText('root file system of proxy')
  await expect(page.getByTestId('confirm-why')).toContainText('Type proxy')
  // the button is aria-disabled, not disabled: a click explains instead of doing nothing
  await page.getByTestId('confirm').click({ force: true })
  await expect(page.getByTestId('confirm-type')).toBeFocused()
  await expect(page.getByRole('dialog')).toContainText('does not match')
  await page.getByTestId('confirm-type').fill('proxy')
  await page.getByTestId('confirm').click()
  await expect(page).toHaveURL(/\/jails$/)
  await expect(page.locator('[data-jail="proxy"]')).toHaveCount(0, { timeout: 10000 })
})

test('stopping a running jail asks first and names it; Escape cancels', async ({ page }) => {
  await open(page, '/jails')
  await page.locator('[data-stop="db"]').click()
  const dlg = page.getByRole('dialog')
  await expect(dlg).toContainText('Stop db')
  await expect(page.getByTestId('confirm-dialog')).toHaveAttribute('data-tier', '2')
  await page.keyboard.press('Escape')
  await expect(dlg).toHaveCount(0)
  await expect(page.locator('[data-jail="db"] [data-state]')).toHaveAttribute('data-state', 'running')
  await page.locator('[data-stop="db"]').click()
  await page.getByTestId('confirm').click()
  await expect(page.locator('[data-jail="db"] [data-state]')).toHaveAttribute('data-state', /stopping|stopped/)
})

test('a data rollback needs the jail name typed', async ({ page }) => {
  await open(page, '/jails/web')
  await page.getByRole('button', { name: 'Roll back' }).first().click()
  await page.getByRole('radio', { name: /^Data/ }).check()
  await expect(page.getByTestId('rollback-why')).toContainText('Type web')
  await page.getByTestId('rollback-go').click({ force: true })
  await expect(page.locator('#rb-confirm')).toBeFocused()
  await page.locator('#rb-confirm').fill('web')
  await expect(page.getByTestId('rollback-why')).toHaveCount(0)
  await page.getByTestId('rollback-go').click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
})

test('the create form follows the server name rule, says what is missing and focuses it', async ({ page }) => {
  await open(page, '/jails')
  await page.getByTestId('new-jail').click()
  await expect(page.getByRole('dialog')).toContainText('at most 32 characters')
  await expect(page.getByTestId('jail-create-why')).toContainText('Name')
  await page.locator('#jc-name').fill('Web.1')
  await page.getByTestId('jail-create-go').click({ force: true })
  await expect(page.locator('#jc-name')).toBeFocused()
  await expect(page.locator('#jc-name-err')).toBeVisible()
  await page.locator('#jc-name').fill('web-2')
  await page.locator('#jc-vol').selectOption('archive')
  await page.locator('#jc-mount').fill('relative')
  await page.getByTestId('jail-create-go').click({ force: true })
  await expect(page.locator('#jc-mount')).toBeFocused()
  await expect(page.locator('#jc-mount-err')).toContainText('absolute path')
})

test('jail tabs live in ?tab= and move with the arrow keys', async ({ page }) => {
  await open(page, '/jails/web?tab=config')
  await expect(page.locator('[role="tab"][aria-selected="true"]')).toHaveAttribute('data-tab', 'config')
  await page.locator('[data-tab="config"]').focus()
  await page.keyboard.press('ArrowRight')
  await expect(page.locator('[data-tab="devices"]')).toBeFocused()
  await expect(page).toHaveURL(/tab=devices/)
  await page.keyboard.press('Home')
  await expect(page).not.toHaveURL(/tab=/)
  await page.keyboard.press('End')
  await expect(page.locator('[data-tab="console"]')).toHaveAttribute('aria-selected', 'true')
  await expect(page.locator('[data-tab="console"]')).toBeInViewport()
})

test('on a phone the jails are cards with their buttons inside the screen', async ({ page }, info) => {
  test.skip(info.project.name !== 'phone', 'phone layout')
  await open(page, '/jails')
  const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(over).toBeLessThanOrEqual(0)
  const vw = page.viewportSize()!.width
  for (const b of await page.locator('[data-test="jails-table"] td.actions button').all()) {
    const box = (await b.boundingBox())!
    expect(box.x + box.width).toBeLessThanOrEqual(vw)
    expect(box.height).toBeGreaterThanOrEqual(40)
  }
})

test('an empty project offers to create a jail', async ({ page }) => {
  await open(page, '/jails')
  await page.getByTestId('project-select').selectOption('shop')
  const empty = page.getByTestId('jails-empty')
  await expect(empty).toBeVisible()
  await empty.getByTestId('empty-new-jail').click()
  await expect(page.getByRole('dialog')).toBeVisible()
})

test('the notes of a jail are shown rendered, edited and saved, and fit a phone', async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 800 })
  await open(page, '/jails/web?tab=notes')
  const panel = page.getByTestId('notes-panel')
  await expect(panel.getByRole('heading', { name: 'Web front end' })).toBeVisible()
  await expect(panel.getByRole('link', { name: 'the TLS page' })).toHaveAttribute('href', 'https://wiki.example.org/shop/tls')
  await expect(panel.locator('pre')).toContainText('nginx -t')
  await panel.getByTestId('notes-edit').click()
  const area = panel.getByTestId('notes-input')
  await expect(area).toBeFocused()
  await area.fill('# Web front end\n\n- Restart with `service nginx restart`\n- <b>owner</b>: ops@example.org')
  await page.keyboard.press('Control+Enter')
  await expect(area).toHaveCount(0)
  await expect(panel.getByTestId('notes-status')).toHaveText('Notes saved')
  await expect(panel.locator('li code')).toHaveText('service nginx restart')
  await expect(panel.locator('b')).toHaveCount(0)
  await expect(panel.getByTestId('notes-edit')).toBeFocused()
  const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(over).toBeLessThanOrEqual(0)
})
