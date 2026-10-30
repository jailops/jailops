import { test, expect, type Page } from '@playwright/test'

// The host identity page in demo mode: preview, apply, snapshots and restore. Runs on desktop and phone.

function watch(page: Page): string[] {
  const problems: string[] = []
  page.on('console', (m) => m.type() === 'error' && problems.push(m.text()))
  page.on('pageerror', (e) => problems.push(String(e)))
  return problems
}

async function open(page: Page): Promise<void> {
  await page.goto('/?demo')
  await page.waitForSelector('.shell')
  await page.evaluate(() => {
    history.pushState({}, '', '/host')
    dispatchEvent(new PopStateEvent('popstate'))
  })
  await page.waitForLoadState('networkidle')
}

test('the page shows what the host has and that JailOps manages it', async ({ page }) => {
  const problems = watch(page)
  await open(page)
  await expect(page.locator('main h1')).toHaveText('Host')
  await expect(page.getByTestId('host-managed')).toHaveText('Managed by JailOps')
  await expect(page.getByTestId('host-drift')).toHaveCount(0)
  await expect(page.getByTestId('hostname')).toHaveValue('bsd01.example.net')
  await expect(page.getByTestId('nameserver')).toHaveCount(2)
  await expect(page.getByTestId('ntp-sync')).toContainText('Synchronised')
  await expect(page.getByTestId('host-state')).toContainText('/etc/resolv.conf')
  await expect(page.locator('[data-test="host-snapshots"] [data-snapshot]')).toHaveCount(2)
  expect(problems).toEqual([])
})

test('a change is previewed as a diff before it can be applied, and leaves a snapshot', async ({ page }) => {
  const problems = watch(page)
  await open(page)
  await expect(page.getByTestId('host-apply')).toBeDisabled()
  await page.getByTestId('hostname').fill('bad_name')
  await expect(page.getByTestId('host-form')).toContainText('not an IP address')
  await expect(page.getByTestId('host-preview-btn')).toBeDisabled()
  await page.getByTestId('hostname').fill('host1.example.com')
  await page.getByTestId('nameserver').first().fill('192.0.2.54')
  await page.getByTestId('host-preview-btn').click()
  await expect(page.getByTestId('host-diff')).toContainText('+nameserver 192.0.2.54')
  await expect(page.getByTestId('host-diff')).toContainText('-nameserver 192.0.2.53')
  await expect(page.getByTestId('host-actions')).toContainText('set the hostname to host1.example.com')
  // an edit after the preview needs a new one
  await page.getByTestId('add-search').click()
  await page.getByTestId('search').last().fill('example.org')
  await expect(page.getByTestId('host-apply')).toBeDisabled()
  await page.getByTestId('host-preview-btn').click()
  await expect(page.getByTestId('host-diff')).toContainText('search example.net example.org')
  await page.getByTestId('host-apply').click()
  await expect(page.getByTestId('host-done')).toContainText('snapshot')
  await expect(page.getByTestId('hostname')).toHaveValue('host1.example.com')
  await expect(page.locator('[data-test="host-snapshots"] [data-snapshot]')).toHaveCount(3)
  expect(problems).toEqual([])
})

test('a snapshot is restored after a confirmation', async ({ page }) => {
  const problems = watch(page)
  await open(page)
  const row = page.locator('[data-test="host-snapshots"] [data-snapshot]').last()
  await row.getByTestId('host-restore').click()
  await expect(page.getByRole('dialog')).toContainText('/etc/rc.conf')
  await page.getByRole('dialog').getByTestId('confirm').click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByTestId('hostname')).toHaveValue('freebsd')
  await expect(page.getByTestId('nameserver').first()).toHaveValue('198.51.100.53')
  await expect(page.locator('[data-test="host-snapshots"] [data-snapshot]')).toHaveCount(3)
  expect(problems).toEqual([])
})

test('the nav lists the host page', async ({ page }, info) => {
  await page.goto('/?demo')
  await page.waitForSelector('.shell')
  if (info.project.name === 'phone') await page.getByRole('button', { name: /menu/i }).first().click()
  await page.getByRole('link', { name: 'Host', exact: true }).click()
  await expect(page.locator('main h1')).toHaveText('Host')
})
