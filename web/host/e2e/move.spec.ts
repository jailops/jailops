import { test, expect, type Page } from '@playwright/test'

// A jail moves to the image of a new release (`?demo=newrelease`: the host runs 15.2, the image of 15.2 is made first):
// from the Upgrade dialog of one jail, with the release change in the preview, and from the jail list for several jails.

async function go(page: Page, path: string): Promise<void> {
  await page.goto('/?demo=newrelease')
  await page.waitForSelector('.shell')
  await page.evaluate((p) => { history.pushState({}, '', p); dispatchEvent(new PopStateEvent('popstate')) }, path)
}

// move inside the page: a reload would start the demo over
async function nav(page: Page, path: string): Promise<void> {
  await page.evaluate((p) => { history.pushState({}, '', p); dispatchEvent(new PopStateEvent('popstate')) }, path)
}

async function makeNewImage(page: Page): Promise<void> {
  await go(page, '/images')
  await page.getByTestId('new-release-go').click()
  await page.getByTestId('image-create-go').click()
  await expect(page.getByTestId('image-done')).toHaveText('The image freebsd-15-2 is ready.', { timeout: 15000 })
  await page.getByTestId('image-close').click()
}

test('a jail moves to the image of a new release from its Upgrade dialog', async ({ page }) => {
  await makeNewImage(page)
  await nav(page, '/jails/db')
  await page.getByRole('button', { name: 'Upgrade', exact: true }).click()
  const image = page.getByTestId('up-image')
  await expect(image.locator('option')).toHaveText(['Stay on freebsd-15-1 (15.1-RELEASE): a new generation of it', 'freebsd-15-2 (15.2-RELEASE)'])
  await image.selectOption('freebsd-15-2')
  await expect(page.locator('#up-target option').first()).toHaveText('Newest generation of freebsd-15-2')
  await page.getByTestId('preview').click()
  await expect(page.getByTestId('preview-release')).toHaveText('Release: 15.1-RELEASE to 15.2-RELEASE')
  await expect(page.getByTestId('preview-kept')).toContainText('Kept: etc, local, var-db, var-log, home, the volumes')
  await expect(page.getByTestId('preview-result')).toContainText('Set the image of the jail to freebsd-15-2')
  // the preview belongs to the image chosen: changing it asks for a new one
  await image.selectOption('')
  await expect(page.getByTestId('upgrade-go')).toBeDisabled()
  await image.selectOption('freebsd-15-2')
  await page.getByTestId('preview').click()
  await expect(page.getByTestId('preview-release')).toBeVisible()
  await page.getByTestId('upgrade-go').click()
  await expect(page.getByTestId('up-image')).toHaveCount(0, { timeout: 15000 }) // the dialog closed
  await nav(page, '/jails')
  await expect(page.locator('[data-jail="db"]')).toContainText('freebsd-15-2')
})

test('selected jails move to the image of a new release from the jail list', async ({ page }) => {
  await makeNewImage(page)
  await nav(page, '/jails')
  await page.locator('[aria-label="Select db"]').check()
  await page.locator('[aria-label="Select api"]').check()
  await page.getByTestId('bulk-upgrade').click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('Upgrade 2 jails')
  await expect(page.getByTestId('bu-image')).toHaveValue('freebsd-15-2')
  await expect(page.getByTestId('bu-run')).toBeDisabled() // nothing is checked yet
  await page.getByTestId('bu-check').click()
  await expect(page.getByTestId('bu-row-db')).toContainText('15.1-RELEASE to 15.2-RELEASE')
  await expect(page.getByTestId('bu-row-db')).toContainText('Ready')
  await expect(page.getByTestId('bu-row-api')).toContainText('Ready')
  await expect(page.getByTestId('bu-run')).toHaveText('Move 2 jails')
  await page.getByTestId('bu-run').click()
  // both moved: the dialog closes by itself and the list shows the new image
  await expect(page.getByTestId('bu-run')).toHaveCount(0, { timeout: 30000 })
  await expect(page.locator('[data-jail="db"]')).toContainText('freebsd-15-2')
  await expect(page.locator('[data-jail="api"]')).toContainText('freebsd-15-2')
})
