import { test, expect } from '@playwright/test'
import { audit } from '../../shared/a11y'

const routes = ['/', '/doctor', '/jails', '/jails/web', '/jails/mail', '/projects', '/projects/shop', '/storage', '/profiles', '/updates', '/images', '/apps', '/apps?tab=installed', '/apps/nginx', '/apps/postgresql', '/apps/isms', '/apps/nsd', '/apps/mail', '/apps/wiki', '/jails/web?tab=logs', '/jails/build?tab=logs', '/networks', '/firewall', '/backups', '/hosts', '/operations', '/events', '/audit', '/access']

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
