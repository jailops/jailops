import { test, expect, type Page } from '@playwright/test'
import { DEMO_SETUP_TOKEN } from '../src/demo/setupToken'

// The first-run wizard in demo mode (`?demo=setup`: a host without an administrator): the setup token, the account,
// the skippable steps, the firewall with its confirmation, the defaults and the way to the app catalog. Runs on
// desktop and phone.

function watch(page: Page): string[] {
  const problems: string[] = []
  page.on('console', (m) => m.type() === 'error' && problems.push(m.text()))
  page.on('pageerror', (e) => problems.push(String(e)))
  return problems
}

async function account(page: Page, token: string): Promise<void> {
  await page.getByTestId('setup-token').fill(token)
  await page.getByTestId('setup-name').fill('admin')
  await page.getByTestId('setup-password').fill('correct horse battery')
  await page.getByTestId('setup-repeat').fill('correct horse battery')
  await page.getByTestId('setup-create').click()
}

test('the wizard creates the administrator, then every later step', async ({ page }) => {
  const problems = watch(page)
  await page.goto('/?demo=setup')
  await expect(page).toHaveURL(/\/setup$/)
  const step = page.getByTestId('setup-step')
  await expect(step).toHaveText('Step 1 of 6: Administrator')
  await expect(page.getByTestId('setup-demo-token')).toContainText(DEMO_SETUP_TOKEN)
  // the account cannot be skipped
  await expect(page.getByTestId('setup-skip')).toHaveCount(0)

  // the browser's own checks first
  await page.getByTestId('setup-password').fill('short')
  await page.getByTestId('setup-create').click()
  await expect(page.getByTestId('setup-account')).toContainText('The password needs at least 12 characters.')
  await expect(page.getByTestId('setup-password')).toHaveAttribute('aria-invalid', 'true')
  await expect(page.getByTestId('setup-account')).toContainText('Enter the setup token.')

  // a wrong token is refused by the server, the right one creates the administrator and signs in
  await account(page, 'WRONGTOKEN')
  await expect(page.getByTestId('setup-account')).toContainText('the setup token is not valid')
  await account(page, DEMO_SETUP_TOKEN.toLowerCase().replace(/(.{8})/g, '$1 '))
  await expect(step).toHaveText('Step 2 of 6: Host name')
  await expect(step).toBeFocused()

  // the host name: applied through the identity module
  await page.getByTestId('setup-hostname').fill('jails1.example.com')
  await page.getByTestId('setup-apply').click()
  await expect(step).toHaveText('Step 3 of 6: Certificate')
  await expect(page.getByTestId('cert-source')).toHaveText('Self-signed')
  await page.getByTestId('setup-skip').click()

  // the firewall: ssh from this browser only, applied and confirmed
  await expect(step).toHaveText('Step 4 of 6: Firewall')
  await expect(page.getByTestId('setup-fw-plan')).toContainText('ssh only from 203.0.113.5')
  await page.getByTestId('setup-fw-apply').click()
  await expect(page.getByTestId('setup-fw-pending')).toBeVisible()
  await expect(page.getByTestId('pending-banner')).toBeVisible()
  await page.getByTestId('setup-fw-confirm').click()
  await expect(page.getByTestId('setup-fw-done')).toBeVisible()
  await page.getByTestId('setup-next').click()

  // the defaults of a host that has none
  await expect(step).toHaveText('Step 5 of 6: Storage and network')
  await expect(page.getByTestId('setup-make-storage')).toContainText('tank/jailops')
  // any prefix the server takes (/8 to /30, no host bits); the gateway and range are shown before anything is made
  await page.getByTestId('setup-subnet').fill('10.77.0.1/24')
  await expect(page.getByTestId('setup-make')).toBeDisabled()
  await page.getByTestId('setup-subnet').fill('10.77.0.0/31')
  await expect(page.getByTestId('setup-make')).toBeDisabled()
  await page.getByTestId('setup-subnet').fill('10.77.0.0/16')
  await expect(page.getByTestId('setup-subnet-plan')).toContainText('Gateway 10.77.0.1, jail addresses 10.77.0.2-10.77.255.254')
  await page.getByTestId('setup-subnet').fill('10.77.0.0/24')
  await page.getByTestId('setup-uplink').fill('em0')
  await page.getByTestId('setup-make').click()
  await expect(step).toHaveText('Step 6 of 6: Done')
  await expect(page.getByTestId('setup-done')).toContainText('Network lan with 10.77.0.0/24')
  await expect(page.getByTestId('setup-done')).toContainText('Project main')

  await page.getByTestId('setup-apps').click()
  await expect(page).toHaveURL(/\/apps$/)
  await expect(page.locator('main h1').first()).toHaveText('Apps')
  // the wizard is gone once the host has an administrator
  await page.evaluate(() => { history.pushState({}, '', '/setup'); dispatchEvent(new PopStateEvent('popstate')) })
  await expect(page.locator('main h1').first()).toHaveText('Overview')
  expect(problems.filter((p) => !/401|404|429/.test(p))).toEqual([])
})

test('every step after the account can be skipped', async ({ page }) => {
  await page.goto('/?demo=setup')
  await account(page, DEMO_SETUP_TOKEN)
  const step = page.getByTestId('setup-step')
  for (const name of ['Host name', 'Certificate', 'Firewall', 'Storage and network']) {
    await expect(step).toContainText(name)
    await page.getByTestId('setup-skip').click()
  }
  await expect(step).toHaveText('Step 6 of 6: Done')
  await expect(page.getByTestId('setup-apps')).toHaveAttribute('href', '/apps')
})

test('wrong setup tokens are locked out after five tries', async ({ page }) => {
  await page.goto('/?demo=setup')
  for (let i = 0; i < 5; i++) {
    await account(page, `WRONG${i}`)
    await expect(page.getByTestId('setup-account')).toContainText('the setup token is not valid')
    await page.getByTestId('setup-token').fill('')
  }
  await account(page, DEMO_SETUP_TOKEN)
  await expect(page.getByTestId('setup-account')).toContainText('too many failed attempts')
  await expect(page.getByTestId('setup-step')).toHaveText('Step 1 of 6: Administrator')
})

test('a host with an administrator shows the sign-in, not the wizard', async ({ page }) => {
  await page.goto('/?demo=login')
  await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()
  await expect(page).toHaveURL(/\/login/)
  await expect(page.getByTestId('setup')).toHaveCount(0)
})
