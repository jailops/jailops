import { test, expect } from '@playwright/test'
import { audit } from '../../shared/a11y'

const routes = ['/', '/doctor', '/jails', '/jails/web', '/jails/mail', '/projects', '/projects/shop', '/storage', '/profiles', '/updates', '/images', '/apps', '/apps?tab=installed', '/apps/nginx', '/apps/postgresql', '/apps/isms', '/apps/nsd', '/apps/mail', '/apps/memcached', '/apps/wiki', '/jails/web?tab=logs', '/jails/build?tab=logs', '/networks', '/firewall', '/backups', '/hosts', '/operations', '/events', '/audit', '/access', '/host', '/networks/wireguard', '/jails/db?tab=access', '/jails/web?tab=access', '/logs', '/logs?jail=db&app_logs=true', '/logs?tab=forwarding', '/jails/vault']

for (const scheme of ['light', 'dark'] as const) {
  test.describe(`a11y ${scheme}`, () => {
    test.use({ colorScheme: scheme })
    for (const r of routes) {
      test(`${r}: names, labels, ids, landmarks and AA contrast`, async ({ page }) => {
        await page.goto('/?demo')
        await page.waitForSelector('.shell')
        if (r !== '/') await page.evaluate((p) => { history.pushState({}, '', p); dispatchEvent(new PopStateEvent('popstate')) }, r)
        await page.waitForLoadState('networkidle')
        await expect(page.locator('main h1').first()).toBeVisible()
        expect(await audit(page)).toEqual([])
      })
    }
  })
}

// The banner of a change that waits for its confirmation (the demo starts with one: `?demo=pending`).
for (const scheme of ['light', 'dark'] as const) {
  test(`a11y ${scheme}: /host with a pending change`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme })
    await page.goto('/?demo=pending')
    await page.waitForSelector('.shell')
    await page.evaluate(() => { history.pushState({}, '', '/host'); dispatchEvent(new PopStateEvent('popstate')) })
    await page.waitForLoadState('networkidle')
    await expect(page.getByTestId('pending-banner')).toBeVisible()
    expect(await audit(page)).toEqual([])
  })
}

// The first-run wizard of a host without an administrator (`?demo=setup`).
for (const scheme of ['light', 'dark'] as const) {
  test(`a11y ${scheme}: /setup`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme })
    await page.goto('/?demo=setup')
    await expect(page.getByTestId('setup-step')).toBeVisible()
    expect(await audit(page)).toEqual([])
  })
}
