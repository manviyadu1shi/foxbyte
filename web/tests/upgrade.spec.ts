// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Moving from Standard to Enterprise in the console, as the person doing it
// sees it: the badge, the Upgrade button, the modal, the result, and every
// lock on the page opening once it closes.
//
// Run on its own, after the other specs and with the licence removed first,
// by scripts/integration_ui.sh — the other specs need the paid features, and
// this one has to begin without them. The harness puts the licence back
// whatever happens here.
import { test, expect, type Page } from '@playwright/test'

const email = process.env.FOX_E2E_EMAIL ?? 'e2e@foxbyte.dev'
const password = process.env.FOX_E2E_PASSWORD ?? 'password123'
// A throwaway licence the harness's build was made to trust.
const licenceFile = process.env.FOX_E2E_LICENCE ?? '/tmp/fox.licence.json'

async function signIn(page: Page) {
  await page.goto('/login', { waitUntil: 'domcontentloaded' })
  await page.getByPlaceholder('you@example.com').fill(email)
  await page.getByPlaceholder('password').fill(password)
  await page.getByRole('button', { name: /log in/i }).click()
  await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible()
}

test('a licence file uploaded in the modal unlocks the paid features without a reload', async ({ page }) => {
  await signIn(page)
  const badge = page.locator('.edition-badge')

  // 1. Before: Standard, because no licence is honoured — even though this
  //    binary is the Enterprise build.
  await expect(badge).toHaveText('Standard')

  // 2–3. Upgrade opens the modal; the file goes in through the picker.
  await page.getByRole('button', { name: 'Upgrade' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('heading', { name: /Activate .* Enterprise/ })).toBeVisible()
  await dialog.getByLabel('Licence file').setInputFiles(licenceFile)
  await expect(dialog.getByText('fox.licence.json')).toBeVisible()

  // 4. Activate, and the result: a tick, the words, a Close button.
  await dialog.getByRole('button', { name: 'Activate' }).click()
  await expect(dialog.getByRole('heading', { name: 'Enterprise activated' })).toBeVisible()
  await expect(dialog.locator('.status-mark.activated')).toBeVisible()

  // 5. Close, and the page agrees — no reload in between.
  await dialog.getByRole('button', { name: 'Close' }).click()
  await expect(dialog).toBeHidden()
  await expect(badge).toHaveText('Enterprise')
  await expect(page.getByRole('button', { name: 'Upgrade' })).toHaveCount(0)

  // And a page that was locked is not. Locked, this button reads "Add rule
  // (Enterprise)" and is disabled, so the exact name alone proves the change.
  await page.getByRole('link', { name: 'Policies' }).click()
  await expect(page.getByRole('button', { name: 'Add rule', exact: true })).toBeEnabled()
})
