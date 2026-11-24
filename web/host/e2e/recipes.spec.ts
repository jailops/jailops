import { test, expect, type Page } from '@playwright/test'
import { audit } from '../../shared/a11y'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

// The Recipes tab of a jail (docs/provisioning.md): import a recipe, fill a variable, validate, dry run and apply.

async function open(page: Page, path: string): Promise<void> {
  await page.goto('/?demo')
  await page.waitForSelector('.shell')
  await page.evaluate((p) => { history.pushState({}, '', p); dispatchEvent(new PopStateEvent('popstate')) }, path)
  await page.waitForLoadState('networkidle')
}

const SECRET = 'Zq9-correct-horse-battery'
const recipe = `vars:
  site: {default: example.test}
  pw: {secret: true, required: true}
steps:
  - package: [nginx]
  - file:
      path: /usr/local/etc/nginx/conf.d/site.conf
      source: files/site.conf
      template: true
      mode: "0644"
  - file:
      path: /usr/local/etc/app.secret
      template: true
      mode: "0600"
      content: "password=\${pw}\\n"
  - service: {name: nginx, enabled: true, running: true}
`

test('recipes: import a recipe, fill the variables, validate, dry run, apply', async ({ page }) => {
  await open(page, '/jails/web?tab=recipes')
  await expect(page.getByRole('tab', { name: 'Recipes' })).toHaveAttribute('aria-selected', 'true')
  expect(await audit(page)).toEqual([])

  // a recipe file alone: the file it names as source: is not beside it, and the page says so before it asks the server
  await page.getByTestId('r-files').setInputFiles({ name: 'recipe.yaml', mimeType: 'text/yaml', buffer: Buffer.from(recipe) })
  await expect(page.getByTestId('r-note')).toContainText('recipe.yaml is loaded')
  await expect(page.getByTestId('r-var-site')).toHaveAttribute('placeholder', 'example.test')
  await expect(page.getByTestId('r-var-pw')).toHaveAttribute('type', 'password')
  await page.getByTestId('r-var-pw').fill(SECRET)
  await page.getByTestId('r-validate').click()
  await expect(page.getByTestId('r-problem')).toContainText('files/site.conf is not a regular file')
  await expect(page.getByTestId('r-problem')).not.toContainText(SECRET)

  // the same recipe as a directory: the file beside it is read in the browser, and a vars file fills the form
  const dir = mkdtempSync(join(tmpdir(), 'recipe-'))
  try {
    mkdirSync(join(dir, 'files'))
    writeFileSync(join(dir, 'recipe.yaml'), recipe)
    writeFileSync(join(dir, 'files', 'site.conf'), 'server_name ${site};\n')
    writeFileSync(join(dir, 'site.vars'), 'site=shop.example.test\n')
    await page.getByTestId('r-dir').setInputFiles(dir)
    await expect(page.getByTestId('r-note')).toContainText('recipe.yaml is loaded with 1 file beside it')
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
  await expect(page.getByTestId('r-var-site')).toHaveValue('shop.example.test')
  await page.getByTestId('r-var-pw').fill(SECRET)
  expect(await audit(page)).toEqual([])

  await page.getByTestId('r-validate').click()
  await expect(page.getByTestId('r-valid')).toContainText('The recipe is valid: 4 steps')

  await page.getByTestId('r-dry').click()
  await expect(page.getByTestId('r-summary')).toHaveText('4 of 4 steps would change.')
  await expect(page.getByTestId('r-step')).toHaveCount(4)
  await expect(page.getByTestId('r-step').first()).toContainText('would change')

  await page.getByTestId('r-apply').click()
  await expect(page.getByTestId('r-summary')).toHaveText('4 of 4 steps changed.')
  await expect(page.getByTestId('r-step').first()).toContainText('changed')
  // the secret is cleared, and is nowhere in the page
  await expect(page.getByTestId('r-var-pw')).toHaveValue('')
  expect(await page.content()).not.toContain(SECRET)
  expect(await audit(page)).toEqual([])

  // again: nothing to change
  await page.getByTestId('r-var-pw').fill(SECRET)
  await page.getByTestId('r-apply').click()
  await expect(page.getByTestId('r-summary')).toHaveText('0 of 4 steps changed.')
  await expect(page.getByTestId('r-step').first()).toContainText('unchanged')
})

test('recipes: a failed step names the step and the exit status', async ({ page }) => {
  await open(page, '/jails/web?tab=recipes')
  await page.getByTestId('r-text').fill('steps:\n  - package: [curl]\n  - run: ["false"]\n')
  await page.getByTestId('r-apply').click()
  await expect(page.getByTestId('error-panel')).toContainText('step 2 of 2 failed (run false): exit status 1')
  await expect(page.getByTestId('error-hint')).toContainText('Before it: package curl changed')
  await expect(page.getByTestId('r-failed')).toBeVisible()
})

test('recipes: a server refusal of the recipe is shown by step and field', async ({ page }) => {
  await open(page, '/jails/web?tab=recipes')
  await page.getByTestId('r-text').fill('steps:\n  - file: {path: relative/path, content: x}\n')
  await page.getByTestId('r-validate').click()
  await expect(page.getByTestId('error-panel')).toContainText('steps[0].file.path')
})

test('recipes: a stopped jail can validate but not dry run or apply', async ({ page }) => {
  await open(page, '/jails/build?tab=recipes')
  await expect(page.getByTestId('recipes-why')).toContainText('not running')
  await page.getByTestId('r-text').fill('steps:\n  - package: [curl]\n')
  await expect(page.getByTestId('r-apply')).toBeDisabled()
  await expect(page.getByTestId('r-dry')).toBeDisabled()
  await page.getByTestId('r-validate').click()
  await expect(page.getByTestId('r-valid')).toContainText('valid: 1 step')
  expect(await audit(page)).toEqual([])
})

test('recipes: a Linux jail says a recipe is for FreeBSD jails', async ({ page }) => {
  await open(page, '/jails/alpine-app?tab=recipes')
  await expect(page.getByTestId('recipes-why')).toContainText('Linux jail')
  await expect(page.getByTestId('r-apply')).toBeDisabled()
})
