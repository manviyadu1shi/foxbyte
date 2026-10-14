// SPDX-License-Identifier: AGPL-3.0-or-later
//
// What the badge at the top of every page says, and to whom Upgrade is offered.
import { describe, expect, it } from 'vitest'
import type { License } from '../api'
import { badgeFor, offerUpgrade } from './EditionBadge'

const lic = (over: Partial<License> = {}): License => ({
  edition: 'enterprise', present: true, state: 'active', unlocks: true,
  features: ['anchors'], reason: '', action: '', you_are_admin: true, ...over,
})

describe('badgeFor', () => {
  it('says nothing until the engine has answered', () => {
    expect(badgeFor(null)).toBeNull()
  })

  it('reads Standard on a standard build', () => {
    expect(badgeFor(lic({ edition: 'standard', present: false, unlocks: false }))?.label).toBe('Standard')
  })

  // An Enterprise build with no licence serves what a Standard one does. A
  // badge reading "Enterprise" over a page of locks would contradict the page.
  it('reads Standard on an Enterprise build with no licence', () => {
    const b = badgeFor(lic({ present: false, state: 'invalid', unlocks: false }))
    expect(b?.label).toBe('Standard')
    expect(b?.tone).toBe('standard')
  })

  it('reads Enterprise once a licence unlocks the paid features', () => {
    const b = badgeFor(lic())
    expect(b?.label).toBe('Enterprise')
    expect(b?.tone).toBe('enterprise')
  })

  it('still reads Enterprise, in amber, when the licence works but wants attention', () => {
    const b = badgeFor(lic({ state: 'warning', reason: 'expires in 9 days', action: 'renew it' }))
    expect(b?.label).toBe('Enterprise')
    expect(b?.tone).toBe('warn')
    expect(b?.title).toMatch(/expires in 9 days/)
  })

  it('says the licence has lapsed rather than pretending to be Standard', () => {
    const b = badgeFor(lic({ state: 'lapsed', unlocks: false, reason: 'expired on 1 October 2026' }))
    expect(b?.label).toBe('Licence lapsed')
    expect(b?.tone).toBe('stop')
  })

  it('tells someone who is not an admin who can act', () => {
    const b = badgeFor(lic({ present: false, unlocks: false, you_are_admin: false }))
    expect(b?.title).toMatch(/Ask an admin/)
  })
})

describe('offerUpgrade', () => {
  it('offers it to an admin while the paid features are locked', () => {
    expect(offerUpgrade(lic({ present: false, unlocks: false }))).toBe(true)
    expect(offerUpgrade(lic({ edition: 'standard', present: false, unlocks: false }))).toBe(true)
    expect(offerUpgrade(lic({ state: 'lapsed', unlocks: false }))).toBe(true)
  })

  // The activation endpoint refuses anyone else. A button that ends in a 403
  // is worse than no button and a sentence saying who to ask.
  it('does not offer it to anyone who cannot activate', () => {
    expect(offerUpgrade(lic({ present: false, unlocks: false, you_are_admin: false }))).toBe(false)
    // An older engine that does not say: no button, rather than a guess.
    expect(offerUpgrade(lic({ present: false, unlocks: false, you_are_admin: undefined }))).toBe(false)
  })

  it('does not offer it once there is nothing to upgrade', () => {
    expect(offerUpgrade(lic())).toBe(false)
    expect(offerUpgrade(lic({ state: 'warning' }))).toBe(false)
    expect(offerUpgrade(null)).toBe(false)
  })
})
