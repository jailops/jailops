import { test, expect, type Page } from '@playwright/test'

async function open(page: Page, path: string): Promise<void> {
  await page.goto('/?demo')
  await page.waitForSelector('.shell')
  await page.evaluate((p) => {
    history.pushState({}, '', p)
    dispatchEvent(new PopStateEvent('popstate'))
  }, path)
  await page.waitForLoadState('networkidle')
}

test('the Logs page merges the jails of the project by time, with a chip per jail and the files that could not be read', async ({ page }) => {
  await open(page, '/logs')
  await expect(page.locator('main h1').first()).toHaveText('Logs')
  const view = page.getByTestId('ul-view')
  await expect(view.locator('.ulog-line').first()).toBeVisible()
  // lines of several jails, each named by its chip
  await expect(view.locator('.ulog-line[data-jail="web"]').first()).toContainText('web/messages')
  await expect(view.locator('.ulog-line[data-jail="db"]').first()).toBeVisible()
  // the stopped jail is listed as not mounted, with the dataset to mount
  await expect(page.getByTestId('ul-failed')).toContainText('jail "build" is not mounted')
  await expect(page.getByTestId('ul-failed')).toContainText('zfs mount')
  // ordered by time: the time column never goes back
  const times = (await view.locator('.ulog-time').allTextContents()).filter(Boolean)
  expect(times.length).toBeGreaterThan(5)
  expect([...times].sort()).toEqual(times)
})

test('filters travel in the address, and the app logs bring lines without a time of their own', async ({ page }) => {
  await open(page, '/logs?jail=db&since=1d')
  const view = page.getByTestId('ul-view')
  await expect(view.locator('.ulog-line').first()).toBeVisible()
  await expect(view.locator('.ulog-line:not([data-jail="db"])')).toHaveCount(0)
  await expect(page.locator('[data-jail="db"][aria-pressed="true"]')).toBeVisible()
  await page.getByTestId('ul-app-logs').check()
  await expect(view).toContainText('db/postgresql/postgresql.log')
  await expect(view).toContainText('DETAIL:')
  expect(page.url()).toContain('app_logs=true')
  // text filter, applied by the server
  await page.getByTestId('ul-grep').fill('checkpoint')
  await page.getByTestId('ul-apply').click()
  await expect(page).toHaveURL(/grep=checkpoint/)
  for (const t of await view.locator('.ul-t').allTextContents()) expect(t.toLowerCase()).toContain('checkpoint')
  // every project
  await page.getByTestId('ul-project').selectOption('*')
  await expect(page).toHaveURL(/project=\*/)
})

test('following appends what the jails write', async ({ page }) => {
  await open(page, '/logs?jail=web&since=15m')
  const view = page.getByTestId('ul-view')
  await expect(page.getByTestId('ul-count')).toContainText('line')
  const before = await view.locator('.ulog-line').count()
  await page.getByTestId('ul-follow').click()
  await expect(page.getByTestId('ul-follow')).toHaveAttribute('aria-pressed', 'true')
  await expect.poll(async () => view.locator('.ulog-line').count(), { timeout: 15_000 }).toBeGreaterThan(before)
  await page.getByTestId('ul-follow').click()
  await expect(page.getByTestId('ul-follow')).toHaveAttribute('aria-pressed', 'false')
})

test('a jail\'s Logs tab links to the Logs page with the jail chosen', async ({ page }) => {
  await open(page, '/jails/web?tab=logs')
  await page.getByTestId('logs-open-all').click()
  await expect(page).toHaveURL(/\/logs\?jail=web/)
  await expect(page.locator('[data-jail="web"][aria-pressed="true"]')).toBeVisible()
})

test('the Forwarding tab shows each sink with its state, tests it, and adds, edits and removes sinks', async ({ page }) => {
  await open(page, '/logs')
  await page.getByRole('tab', { name: 'Forwarding' }).click()
  await expect(page).toHaveURL(/tab=forwarding/)
  await expect(page.getByTestId('fw-origin')).toHaveAttribute('data-origin', 'config')
  const central = page.locator('[data-sink="central"]')
  const alerts = page.locator('[data-sink="alerts"]')
  await expect(central.getByTestId('fw-state')).toHaveText('Working')
  await expect(alerts.getByTestId('fw-state')).toHaveText('Failing')
  await expect(alerts.getByTestId('fw-dropped')).toHaveText('341')
  await expect(alerts.getByTestId('fw-error')).toContainText('503 Service Unavailable')
  // a test send, delivered and not
  await central.getByTestId('fw-test').click()
  await expect(central.getByTestId('fw-test-result')).toContainText('Delivered')
  await alerts.getByTestId('fw-test').click()
  await expect(alerts.getByTestId('fw-test-result')).toContainText('Not delivered')

  // add a plain tcp sink: the form checks the destination and warns about the clear text
  await page.getByTestId('fw-add').click()
  await page.getByTestId('lsf-save').click()
  await expect(page.locator('#lsf-name-e')).toContainText('Required')
  await page.locator('#lsf-name').fill('lab')
  await page.locator('#lsf-to').fill('https://logs.example.com')
  await expect(page.locator('#lsf-to-e')).toContainText('udp://, tcp:// or tls://')
  await page.locator('#lsf-to').fill('tcp://192.0.2.50:514')
  await expect(page.getByTestId('lsf-plaintext')).toBeVisible()
  await page.locator('#lsf-jails').fill('web, db')
  await page.getByTestId('lsf-save').click()
  const lab = page.locator('[data-sink="lab"]')
  await expect(lab).toContainText('tcp://192.0.2.50:514')
  await expect(lab.getByTestId('fw-warning')).toBeVisible()
  await expect(page.getByTestId('fw-origin')).toHaveAttribute('data-origin', 'api')

  // edit it to tls: the warning goes
  await lab.getByTestId('fw-edit').click()
  await page.locator('#lsf-to').fill('tls://logs.example.com:6514')
  await page.getByTestId('lsf-save').click()
  await expect(lab).toContainText('tls://logs.example.com:6514')
  await expect(lab.getByTestId('fw-warning')).toHaveCount(0)

  // a webhook with a token file the server cannot find: its error and hint are shown
  await page.getByTestId('fw-add').click()
  await page.locator('#lsf-name').fill('hook')
  await page.getByTestId('lsf-type').selectOption('webhook')
  await page.locator('#lsf-to').fill('https://hooks.example.com/x')
  await page.locator('#lsf-token').fill('/root/token')
  await page.getByTestId('lsf-save').click()
  await expect(page.getByRole('dialog')).toContainText('does not exist')
  await page.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click()

  // remove behind a confirmation, then back to the file's list
  await lab.getByTestId('fw-remove').click()
  await page.getByTestId('confirm').click()
  await expect(lab).toHaveCount(0)
  await page.getByTestId('fw-reset').click()
  await page.getByTestId('confirm').click()
  await expect(page.getByTestId('fw-origin')).toHaveAttribute('data-origin', 'config')
})
