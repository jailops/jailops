import { test, expect } from '@playwright/test'
import { audit } from '../../shared/a11y'

const routes = ['/', '/doctor', '/jails', '/jails/web', '/jails/mail', '/projects', '/projects/shop', '/storage', '/profiles', '/updates', '/images', '/apps', '/apps?tab=installed', '/apps/nginx', '/apps/postgresql', '/apps/isms', '/apps/nsd', '/apps/mail', '/apps/memcached', '/apps/wiki', '/jails/web?tab=logs', '/jails/build?tab=logs', '/networks', '/firewall', '/backups', '/hosts', '/operations', '/events', '/audit', '/access', '/host', '/networks/wireguard', '/jails/db?tab=access', '/jails/web?tab=access', '/logs', '/logs?jail=db&app_logs=true', '/logs?tab=forwarding', '/jails/vault',
  '/jails/web?tab=config', '/firewall?tab=sets', '/backups?tab=policies', '/profiles?tab=host', '/access?tab=sessions', '/access?tab=trust', '/access?tab=tokens', '/nope']

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

// Dialogs and states of the confirmation tiers and the form checks: each one opened, then audited.
const states: Array<{ name: string; route: string; steps: Array<{ click?: string; force?: boolean; fill?: [string, string] }> }> = [
  { name: 'delete jail (typed)', route: '/jails/proxy', steps: [{ click: '[data-test="jail-delete"]' }, { click: '[data-test="confirm"]', force: true }] },
  { name: 'stop a running jail', route: '/jails', steps: [{ click: '[data-stop="db"]' }] },
  { name: 'uninstall an app', route: '/apps?tab=installed', steps: [{ click: '[data-test^="uninstall-"]' }] },
  { name: 'create jail after a failed submit', route: '/jails', steps: [{ click: '[data-test="new-jail"]' }, { click: '[data-test="jail-create-go"]', force: true }] },
  { name: 'image upgrade confirm', route: '/images', steps: [{ click: '[data-test^="upgrade-"]' }] },
  { name: 'network edit', route: '/networks', steps: [{ click: '[data-test="edit-net-lan"]' }] },
  { name: 'network delete (typed)', route: '/networks', steps: [{ click: '[data-test="delete-net-lan"]' }] },
  { name: 'create firewall policy', route: '/firewall', steps: [{ click: '[data-test="new-policy"]' }, { click: '[data-test="policy-create-go"]', force: true }] },
  { name: 'new token after a failed submit', route: '/access?tab=tokens', steps: [{ click: '.page-head .btn-primary' }, { click: '[data-test="token-create"]', force: true }] },
  { name: 'add a log sink', route: '/logs?tab=forwarding', steps: [{ click: '[data-test="fw-add"]' }] },
]
for (const scheme of ['light', 'dark'] as const) {
  test.describe(`a11y ${scheme} dialogs`, () => {
    test.use({ colorScheme: scheme })
    for (const st of states) {
      test(`${st.name}`, async ({ page }) => {
        await page.goto('/?demo')
        await page.waitForSelector('.shell')
        await page.evaluate((p) => { history.pushState({}, '', p); dispatchEvent(new PopStateEvent('popstate')) }, st.route)
        await page.waitForLoadState('networkidle')
        for (const x of st.steps) if (x.click) await page.locator(x.click).first().click({ force: x.force })
        await page.waitForTimeout(300)
        expect(await audit(page)).toEqual([])
      })
    }
  })
}
