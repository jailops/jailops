import { test, expect, type Page } from '@playwright/test'

// Encrypted volumes in demo mode: the jail "vault" starts locked; its passphrase is demo-vault-passphrase.

async function open(page: Page, path: string): Promise<void> {
  await page.goto('/?demo')
  await page.waitForSelector('.shell')
  if (path !== '/') {
    await page.evaluate((p) => {
      history.pushState({}, '', p)
      dispatchEvent(new PopStateEvent('popstate'))
    }, path)
  }
  await page.waitForLoadState('networkidle')
}

test('the dashboard counts locked jails and links each one', async ({ page }) => {
  await open(page, '/')
  const banner = page.getByTestId('locked-banner')
  await expect(banner).toContainText('1 jail waits for a passphrase')
  await expect(banner.getByRole('button')).toHaveCount(0) // no unlock-all: each jail is unlocked with its own passphrase
  await banner.locator('[data-locked-link="vault"]').click()
  await expect(page).toHaveURL(/\/jails\/vault/)
  await expect(page.getByTestId('locked-callout')).toBeVisible()
})

test('a locked jail is marked in the list and unlocked with its passphrase, then locked again', async ({ page }) => {
  await open(page, '/jails')
  await expect(page.locator('[data-locked="vault"]')).toContainText('secrets')
  await page.locator('[data-unlock="vault"]').click()
  const dialog = page.getByRole('dialog')
  const field = dialog.locator('#unlock-pass')
  await expect(field).toHaveAttribute('type', 'password')
  await field.fill('not-the-passphrase')
  await dialog.getByRole('button', { name: 'Show the passphrase' }).click()
  await expect(field).toHaveAttribute('type', 'text')
  await dialog.getByTestId('unlock-go').click()
  await expect(dialog.getByTestId('unlock-error')).toHaveText('The passphrase is not correct.')
  await expect(field).toHaveValue('')
  await expect(page.locator('body')).not.toContainText('not-the-passphrase')
  await field.fill('demo-vault-passphrase')
  await dialog.getByLabel('Start the jail after the unlock').check()
  await dialog.getByTestId('unlock-go').click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  const row = page.locator('[data-test="jails-table"] tr', { has: page.locator('a', { hasText: /^vault$/ }) })
  await expect(row.locator('[data-state="running"]')).toBeVisible()
  await expect(page.locator('[data-locked="vault"]')).toHaveCount(0)
  expect(page.url()).not.toContain('passphrase')
  const stored = await page.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }))
  expect(stored).not.toContain('demo-vault-passphrase')

  // lock it again from its page: confirmed, stopped and locked
  await row.locator('a', { hasText: /^vault$/ }).click()
  await page.getByTestId('lock').click()
  await page.getByRole('dialog').getByTestId('confirm').click()
  await expect(page.locator('main [data-state="locked"]').first()).toBeVisible()
  await expect(page.getByTestId('unlock')).toBeVisible()
})

test('starting a locked jail is refused with the unlock hint', async ({ page }) => {
  await open(page, '/jails/vault')
  await expect(page.getByTestId('unlock')).toBeVisible()
  await expect(page.locator('main').getByRole('button', { name: 'Start' })).toHaveCount(0)
  await expect(page.locator('main [data-encrypted="secrets"]')).toBeVisible()
})

test('the create form asks for the passphrase of an encrypted volume twice', async ({ page }) => {
  await open(page, '/jails')
  await page.getByTestId('new-jail').click()
  await page.locator('#jc-name').fill('vault2')
  await page.locator('#jc-vol').fill('secrets')
  await page.locator('#jc-mount').fill('/var/db/secrets')
  await page.getByTestId('jc-encrypted').check()
  await page.locator('#jc-pass').fill('another-long-passphrase')
  await page.locator('#jc-pass2').fill('another-long-passphrasX')
  await page.locator('#jc-pass2').blur()
  await expect(page.getByTestId('jc-vol-error')).toHaveText('The passphrases do not match.')
  await expect(page.getByTestId('jail-create-go')).toHaveAttribute('aria-disabled', 'true')
  await expect(page.getByTestId('jail-create-why')).toContainText('do not match')
  await page.locator('#jc-pass2').fill('another-long-passphrase')
  await page.getByTestId('jail-create-go').click()
  await expect(page.locator('[data-test="jails-table"]')).toContainText('vault2')
})
