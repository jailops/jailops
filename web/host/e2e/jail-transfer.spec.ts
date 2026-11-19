import { test, expect, type Page } from '@playwright/test'
import { audit } from '../../shared/a11y'

// Export and import of a jail (extension jail_export) in the demo: the jail page downloads the file, the jails page reads
// the manifest of a chosen file, asks the preflight and makes the jail. Desktop and phone.

async function open(page: Page, path: string): Promise<void> {
  await page.goto('/?demo')
  await page.waitForSelector('.shell')
  await page.evaluate((p) => {
    history.pushState({}, '', p)
    dispatchEvent(new PopStateEvent('popstate'))
  }, path)
  await page.waitForLoadState('networkidle')
}

test('a jail is exported from its page, and the file is imported under another name', async ({ page }) => {
  // the native file picker cannot be driven: without it the page offers an ordinary download
  await page.addInitScript(() => { delete (window as unknown as Record<string, unknown>).showSaveFilePicker })
  await open(page, '/jails/web')
  await page.getByTestId('jail-export').click()
  const dlg = page.getByRole('dialog')
  await expect(dlg.getByRole('heading', { name: 'Export jail web' })).toBeVisible()
  await audit(page)
  const download = page.waitForEvent('download')
  await page.getByTestId('export-go').click()
  const file = await download
  expect(file.suggestedFilename()).toBe('web.jailexport')
  await expect(page.getByTestId('export-done')).toContainText('web.jailexport was written')
  const path = await file.path()
  await page.keyboard.press('Escape')

  await open(page, '/jails')
  await page.getByTestId('import-jail').click()
  await expect(page.getByTestId('import-why')).toHaveText('Choose a file first.')
  await page.getByTestId('import-file').setInputFiles(path)
  await expect(page.getByTestId('import-summary')).toContainText('Jail web of project web')
  await expect(page.getByTestId('import-errors')).toContainText('A jail named web exists')
  await page.getByTestId('import-name').fill('web-copy')
  await expect(page.getByTestId('import-errors')).toHaveCount(0)
  await page.getByTestId('import-go').click()
  await expect(page).toHaveURL(/\/jails\/web-copy$/)
  await expect(page.getByRole('heading', { name: 'web-copy' })).toBeVisible()
})
