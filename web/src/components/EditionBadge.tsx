// SPDX-License-Identifier: AGPL-3.0-or-later
import { Link } from 'react-router-dom'
import type { License } from '../api'

// The edition, at the top of every page, and the way up from it.
//
// Driven by what the engine is *honouring*, not by which binary it is. An
// Enterprise build with no licence serves exactly what a Standard one does, so
// it says Standard: a badge reading "Enterprise" over a page full of locks
// would be the badge disagreeing with the product. And there are more than two
// states, because the two in between are the ones somebody needs told about —
// a licence that still works but wants attention, and one that has stopped.

export type BadgeTone = 'standard' | 'enterprise' | 'warn' | 'stop'
export type Badge = { label: string; tone: BadgeTone; title: string }

/** What the badge says for a licence, or null before the engine has answered. */
export function badgeFor(lic: License | null): Badge | null {
  if (!lic) return null
  const ask = lic.you_are_admin ? '' : ' Ask an admin to activate a licence.'
  if (!lic.present) {
    return {
      label: 'Standard',
      tone: 'standard',
      title: lic.edition === 'standard'
        ? 'This is the standard edition.' + (lic.you_are_admin ? '' : ' An admin can see how to move to Enterprise.')
        : 'No licence is activated, so the paid features are locked.' + ask,
    }
  }
  if (lic.unlocks && lic.state === 'active') {
    const until = lic.expires ? ` Runs to ${new Date(lic.expires).toLocaleDateString()}.` : ''
    return { label: 'Enterprise', tone: 'enterprise', title: 'The paid features are unlocked.' + until }
  }
  if (lic.unlocks) {
    // Still working, and something wants doing before it stops.
    return { label: 'Enterprise', tone: 'warn', title: `${lic.reason}. ${lic.action}`.trim() }
  }
  return {
    label: lic.state === 'lapsed' ? 'Licence lapsed' : 'Licence invalid',
    tone: 'stop',
    title: `${lic.reason}. ${lic.action}`.trim() + ask,
  }
}

/** Whether to offer Upgrade: to someone who can use it, when there is something to upgrade. */
export function offerUpgrade(lic: License | null): boolean {
  return !!lic && !!lic.you_are_admin && !lic.unlocks
}

export default function EditionBadge({ lic }: { lic: License | null }) {
  const b = badgeFor(lic)
  if (!b) return null
  // A link, so the details — who the licence is for, when it runs to, which
  // machine it is bound to — are one click from the summary.
  return (
    <Link to="/license" className={'edition-badge ' + b.tone} title={b.title} aria-label={`${b.label}. ${b.title}`}>
      {b.tone === 'warn' && <span className="edition-dot" aria-hidden />}
      {b.label}
    </Link>
  )
}
