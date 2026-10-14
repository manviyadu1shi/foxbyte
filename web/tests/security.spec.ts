// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The console's browser security headers (14 Oct 2026 audit, S3): its page may
// not be framed by another site and runs scripts from its own server only — and
// no page of the console breaks that policy, which is the half a header check
// alone cannot see. Chromium reports every blocked script, style or request as a
// console message naming the Content Security Policy.
import { test, expect, type Page } from '@playwright/test'

const email = process.env.FOX_E2E_EMAIL ?? 'e2e@foxbyte.dev'
const password = process.env.FOX_E2E_PASSWORD ?? 'password123'

async function signIn(page: Page) {
  await page.goto('/login', { waitUntil: 'domcontentloaded' })
  await page.getByPlaceholder('you@example.com').fill(email)
  await page.getByPlaceholder('password').fill(password)
  await page.getByRole('button', { name: /log in/i }).click()
  await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible()
}

test('the console page forbids framing and runs only its own scripts', async ({ page }) => {
  const res = await page.goto('/login', { waitUntil: 'domcontentloaded' })
  const h = res!.headers()
  expect(h['content-security-policy']).toContain("frame-ancestors 'none'")
  expect(h['content-security-policy']).toContain("script-src 'self';")
  expect(h['x-frame-options']).toBe('DENY')
  expect(h['x-content-type-options']).toBe('nosniff')
  // The theme is still chosen before the first paint, from a file now.
  expect(await page.evaluate(() => document.documentElement.getAttribute('data-theme'))).toMatch(/^(light|dark)$/)
})

test('no page of the console breaks its own policy', async ({ page }) => {
  const violations: string[] = []
  page.on('console', m => {
    if (/Content Security Policy/i.test(m.text())) violations.push(`${page.url()}: ${m.text()}`)
  })
  await signIn(page)
  for (const path of ['/', '/dashboard', '/console', '/blackbox', '/integrity', '/policies', '/import',
    '/pipelines', '/realtime', '/requests', '/keys', '/license', '/guide', '/docs']) {
    await page.goto(path, { waitUntil: 'load' })
    await page.waitForTimeout(400) // let the page's first requests and styles land
  }
  expect(violations).toEqual([])
})
