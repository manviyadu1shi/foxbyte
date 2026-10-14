// SPDX-License-Identifier: AGPL-3.0-or-later
import { useEffect, useState } from 'react'
import { getLicense, getStatus, has, type Feature, type License, type Status } from './api'

// Which paid features this engine can actually serve, for the pages that show a
// control locked rather than hidden.
//
// Two rules live here so no page has to remember them.
//
// **Unknown is not refused.** Until /api/status answers — and on an engine too
// old to have the field at all — every feature reads as available. A newer
// console must not disable the controls of an older engine; the engine refuses
// for itself, with a message, and that is the authority.
//
// **The edition is reported too**, because the two reasons a feature is missing
// need different sentences: a Standard install needs a different binary, an
// Enterprise one needs a licence. One is a download, the other a purchase.

// --- one refresh for every lock on the screen -------------------------------
//
// Each page asks for the status when it mounts, which was enough while a
// licence could only change from a terminal followed by a restart. Activating
// from the console changes it under a page that is already showing a lock — so
// whoever changes the entitlement calls refreshEntitlement(), and every badge,
// banner and locked control that is mounted asks again. Without this, closing
// the Upgrade modal would leave the page behind it locked until a reload, which
// is exactly the moment somebody is looking at it.
type Listener = () => void
const listeners = new Set<Listener>()

/** Ask every mounted lock, badge and banner to read the entitlement again. */
export function refreshEntitlement() {
  for (const l of [...listeners]) l()
}

function useEntitlementVersion() {
  const [v, setV] = useState(0)
  useEffect(() => {
    const bump = () => setV(n => n + 1)
    listeners.add(bump)
    return () => { listeners.delete(bump) }
  }, [])
  return v
}

export function useFeatures() {
  const [status, setStatus] = useState<Status | null>(null)
  const version = useEntitlementVersion()
  useEffect(() => { getStatus().then(setStatus).catch(() => setStatus(null)) }, [version])
  return {
    can: (f: Feature) => !status || has(status, f),
    // null until the engine answers, so a page can wait before saying anything.
    edition: status?.edition ?? null,
  }
}

/** The licence as the engine is honouring it, re-read whenever it changes. */
export function useLicense() {
  const [lic, setLic] = useState<License | null>(null)
  const version = useEntitlementVersion()
  useEffect(() => {
    let live = true
    getLicense().then(l => { if (live) setLic(l) }).catch(() => { if (live) setLic(null) })
    return () => { live = false }
  }, [version])
  return lic
}

// why explains a locked control in one sentence, naming the way out.
export function why(edition: 'standard' | 'enterprise' | null, what: string) {
  if (edition === 'enterprise') return `${what} needs a licence covering it — use Upgrade, at the top of the page, to activate one.`
  return `${what} is part of Enterprise, and this is the standard edition — Upgrade, at the top of the page, says how to switch.`
}
