import { test, expect, type Page } from '@playwright/test'

// The host firewall in demo mode: the rules, the editor with inline validation, the preview, and commit confirmed:
// the pending banner with its countdown, which survives a reload, and Confirm and Revert. Runs on desktop and phone.

function watch(page: Page): string[] {
  const problems: string[] = []
  page.on('console', (m) => m.type() === 'error' && problems.push(m.text()))
  page.on('pageerror', (e) => problems.push(String(e)))
  return problems
}

async function open(page: Page, demo = '?demo'): Promise<void> {
  await page.goto('/' + demo)
  await page.waitForSelector('.shell')
  await page.evaluate(() => {
    history.pushState({}, '', '/host')
    dispatchEvent(new PopStateEvent('popstate'))
  })
  await page.waitForLoadState('networkidle')
}

/** Adds an allow row for port 8080, previews it and applies it with a 300 second window. */
async function applyChange(page: Page): Promise<void> {
  await page.getByTestId('fw-add-allow').click()
  const row = page.getByTestId('fw-allow-row').last()
  await row.getByTestId('fw-allow-ports').fill('99999')
  await expect(row).toContainText('99999 is not a port')
  await expect(row.getByTestId('fw-allow-ports')).toHaveAttribute('aria-invalid', 'true')
  await expect(page.getByTestId('fw-preview-btn')).toBeDisabled()
  await row.getByTestId('fw-allow-ports').fill('8080')
  await row.getByTestId('fw-allow-comment').fill('test app')
  await page.getByTestId('fw-within').fill('300')
  await expect(page.getByTestId('fw-revert-explain')).toContainText('reverts itself in 300 seconds')
  await expect(page.getByTestId('fw-apply')).toBeDisabled()
  await page.getByTestId('fw-preview-btn').click()
  await expect(page.getByTestId('fw-diff')).toContainText('+pass in proto tcp from any to (self) port 8080')
  await expect(page.getByTestId('fw-diff')).toContainText('+# test app')
  await expect(page.getByTestId('fw-preview-rules')).toContainText('port 8080')
  await expect(page.getByTestId('fw-actions')).toContainText('reverts in 300 seconds unless it is confirmed')
  await page.getByTestId('fw-apply').click()
  await expect(page.getByTestId('fw-done')).toContainText('300 seconds')
  await expect(page.getByTestId('pending-banner')).toContainText(/firewall change reverts in [45]:\d\d/)
}

test('the firewall section shows the rules and that ssh is narrowed', async ({ page }) => {
  const problems = watch(page)
  await open(page)
  await expect(page.getByTestId('fw-managed')).toHaveText('Managed by JailOps')
  await expect(page.getByTestId('fw-pf')).toHaveText('pf is running')
  await expect(page.getByTestId('fw-ssh-open')).toHaveCount(0)
  await expect(page.locator('[data-test="fw-rules"] [data-rule]')).toHaveCount(11)
  await expect(page.getByTestId('fw-rules')).toContainText('@admins')
  await expect(page.getByTestId('fw-allow-row')).toHaveCount(2)
  await expect(page.getByTestId('pending-banner')).toHaveCount(0)
  // ssh from anywhere is warned about in the preview
  await page.getByTestId('fw-ssh-from').fill('')
  await page.getByTestId('fw-preview-btn').click()
  await expect(page.getByTestId('fw-warning')).toContainText('ssh is open to the world')
  expect(problems).toEqual([])
})

test('an apply waits for its confirmation, also after a reload', async ({ page }) => {
  const problems = watch(page)
  await open(page)
  await applyChange(page)
  await expect(page.locator('[data-test="fw-rules"] [data-rule]')).toHaveCount(12)
  // the change shows on every page, and is still there after a reload
  await page.reload()
  await page.waitForSelector('.shell')
  await expect(page.getByTestId('pending-banner')).toBeVisible()
  const first = await page.getByTestId('pending-countdown').textContent()
  await expect(page.getByTestId('pending-countdown')).not.toHaveText(first ?? '')
  // a second change waits for the first
  await page.getByTestId('fw-icmp').click()
  await page.getByTestId('fw-preview-btn').click()
  await page.getByTestId('fw-apply').click()
  await expect(page.getByTestId('fw-form')).toContainText('waiting for its confirmation')
  await page.getByTestId('pending-confirm').click()
  await expect(page.getByTestId('pending-banner')).toHaveCount(0)
  await expect(page.getByTestId('pending-said')).toContainText('confirmed')
  await expect(page.getByTestId('fw-rules')).toContainText('8080')
  expect(problems).toEqual([])
})

test('a pending change can be reverted at once', async ({ page }) => {
  const problems = watch(page)
  await open(page)
  await applyChange(page)
  await page.getByTestId('pending-revert').click()
  await expect(page.getByTestId('pending-banner')).toHaveCount(0)
  await expect(page.getByTestId('pending-said')).toContainText('reverted')
  await expect(page.locator('[data-test="fw-rules"] [data-rule]')).toHaveCount(11)
  await expect(page.getByTestId('fw-rules')).not.toContainText('8080')
  expect(problems).toEqual([])
})
