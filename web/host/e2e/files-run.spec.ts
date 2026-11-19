import { test, expect, type Page } from '@playwright/test'
import { audit } from '../../shared/a11y'

// One command on the selected jails and the Files tab of a jail (docs/operations.md: One command on many jails,
// Copying files).

async function open(page: Page, path: string): Promise<void> {
  await page.goto('/?demo')
  await page.waitForSelector('.shell')
  await page.evaluate((p) => { history.pushState({}, '', p); dispatchEvent(new PopStateEvent('popstate')) }, path)
  await page.waitForLoadState('networkidle')
}

test('run a command on the selected jails: a block each, the stopped one named', async ({ page }) => {
  await open(page, '/jails')
  await page.getByLabel('Select git').check()
  await page.getByLabel('Select worker').check()
  await page.getByLabel('Select build').check()
  await page.getByTestId('bulk-run').click()
  const dlg = page.getByRole('dialog')
  await expect(dlg).toContainText('Run a command on 3 jails')
  await expect(page.getByTestId('rc-skipped')).toContainText('build')
  expect(await audit(page)).toEqual([])
  await page.getByTestId('rc-command').fill('hostname')
  await page.getByTestId('rc-run').click()
  await expect(page.getByTestId('rc-result')).toHaveCount(2)
  await expect(page.getByTestId('rc-result').first()).toContainText('.example.net')
  await expect(page.getByTestId('rc-summary')).toHaveText('2 succeeded, 0 failed, 1 skipped')
  await page.getByTestId('rc-command').fill('false')
  await page.getByTestId('rc-run').click()
  await expect(page.getByTestId('rc-summary')).toHaveText('0 succeeded, 2 failed, 1 skipped')
})

test('files: send a file to a jail and read it back', async ({ page }) => {
  await open(page, '/jails/web?tab=files')
  expect(await audit(page)).toEqual([])
  await page.getByTestId('f-file').setInputFiles({ name: 'hello.txt', mimeType: 'text/plain', buffer: Buffer.from('hello jail') })
  await page.getByTestId('f-put').fill('/tmp/')
  await page.getByTestId('f-upload').click()
  await expect(page.getByTestId('files-note')).toContainText('hello.txt (10 B) is in the jail at /tmp/hello.txt')
  await page.getByTestId('f-get').fill('/tmp/hello.txt')
  const dl = page.waitForEvent('download')
  await page.getByTestId('f-download').click()
  expect((await dl).suggestedFilename()).toBe('hello.txt')
  await page.getByTestId('f-get').fill('/tmp/nothing')
  await page.getByTestId('f-download').click()
  await expect(page.getByTestId('error-panel')).toContainText('does not exist')
})
