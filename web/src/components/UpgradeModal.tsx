// SPDX-License-Identifier: AGPL-3.0-or-later
import { useEffect, useId, useRef, useState, type DragEvent } from 'react'
import { activateLicense, type License, type LicenseActivation } from '../api'
import { BRAND } from '../brand'

// Upgrading to Enterprise, from wherever somebody happens to be.
//
// Two different things are both called "upgrading", and this says which one
// applies before asking for anything. An Enterprise build needs a licence: a
// file, uploaded or pasted, and the engine unlocks the paid features the moment
// it has verified it. A Standard build cannot be upgraded by any licence — the
// paid code is not in that binary, which is what keeps the free build purely
// AGPL — so it is shown the one command that installs the Enterprise build,
// rather than a form that would accept a licence and change nothing.

/** What activating produced, which decides what the modal says next. */
export type Outcome = 'activated' | 'warning' | 'locked'

// Three outcomes, not two. The engine installs a licence that has expired or
// that names another machine, because those are states a real customer is in
// and each has its own way out — so "it worked" has to be told apart from "it
// worked, and something wants doing" and from "it is installed and unlocks
// nothing". A green tick on an expired licence would be the one lie in here.
export function outcomeOf(r: LicenseActivation): Outcome {
  if (r.unlocks && r.state === 'active') return 'activated'
  if (r.unlocks) return 'warning'
  return 'locked'
}

// A licence is a few hundred bytes. Anything far larger is the wrong file, and
// reading it whole into a textarea would be a strange way to find out.
const maxLicenceBytes = 64 * 1024

type Mode = 'upload' | 'paste'

export default function UpgradeModal({ lic, onClose }: { lic: License; onClose: (changed: boolean) => void }) {
  const titleId = useId()
  const [mode, setMode] = useState<Mode>('upload')
  const [text, setText] = useState('')
  const [file, setFile] = useState<{ name: string; size: number } | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [result, setResult] = useState<LicenseActivation | null>(null)
  const [dragging, setDragging] = useState(false)
  const closeRef = useRef<HTMLButtonElement>(null)

  // Closing tells the page whether anything changed, so it can ask every lock
  // to look again; dismissing without activating changes nothing.
  const close = () => { if (!busy) onClose(result !== null) }

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') close() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })
  // The result is the thing to read next, and Close the thing to press.
  useEffect(() => { if (result) closeRef.current?.focus() }, [result])

  const readFile = async (f: File) => {
    setErr('')
    if (f.size > maxLicenceBytes) {
      setFile(null); setText('')
      setErr(`${f.name} is ${Math.round(f.size / 1024)} KB — a licence is a few hundred bytes, so this is not one.`)
      return
    }
    const body = await f.text()
    setFile({ name: f.name, size: f.size })
    setText(body)
  }
  const onDrop = (e: DragEvent) => {
    e.preventDefault()
    setDragging(false)
    const f = e.dataTransfer.files?.[0]
    if (f) void readFile(f)
  }

  const activate = async () => {
    setBusy(true)
    setErr('')
    try {
      setResult(await activateLicense(text))
    } catch (e) {
      // The engine's own words: it says what was wrong with the licence and
      // that nothing was installed, which is better than anything generic.
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="modal-overlay" onClick={close}>
      <div className="modal upgrade-modal fade-up" role="dialog" aria-modal="true" aria-labelledby={titleId}
        onClick={e => e.stopPropagation()}>
        {lic.edition === 'standard'
          ? <StandardBuild titleId={titleId} onClose={close} />
          : result
            ? <Result r={result} titleId={titleId} closeRef={closeRef} onClose={close}
                onAgain={() => { setResult(null); setText(''); setFile(null) }} />
            : (
              <>
                <h3 id={titleId}>Activate {BRAND.product} Enterprise</h3>
                <p className="modal-body">
                  Upload the licence file you were sent, or paste what is in it. It is signed, so this
                  engine can tell a real one from a forged one before it installs anything.
                </p>

                <div className="seg" role="tablist" aria-label="How to provide the licence">
                  <button role="tab" aria-selected={mode === 'upload'} className={mode === 'upload' ? 'active' : ''}
                    onClick={() => { setMode('upload'); setErr('') }} disabled={busy}>Upload file</button>
                  <button role="tab" aria-selected={mode === 'paste'} className={mode === 'paste' ? 'active' : ''}
                    onClick={() => { setMode('paste'); setErr('') }} disabled={busy}>Paste licence</button>
                </div>

                {mode === 'upload' ? (
                  <label className={'drop' + (dragging ? ' dragging' : '') + (file ? ' has-file' : '')}
                    onDragOver={e => { e.preventDefault(); setDragging(true) }}
                    onDragLeave={() => setDragging(false)} onDrop={onDrop}>
                    <input type="file" accept=".json,.lic,.txt,application/json,text/plain" disabled={busy}
                      aria-label="Licence file"
                      onChange={e => { const f = e.target.files?.[0]; if (f) void readFile(f) }} />
                    {file
                      ? <span><b>{file.name}</b> <span className="muted">· {file.size} bytes · choose another</span></span>
                      : <span><b>Choose a licence file</b> <span className="muted">or drop it here</span></span>}
                  </label>
                ) : (
                  <textarea className="lic-paste" rows={7} value={text} disabled={busy} autoFocus
                    aria-label="Licence text" spellCheck={false}
                    placeholder='{"id":"FB-…","customer":"…","signature":"…"}'
                    onChange={e => { setText(e.target.value); setFile(null); setErr('') }} />
                )}

                {err && <p className="err" role="alert">{err}</p>}

                <div className="modal-actions">
                  <button className="ghost" onClick={close} disabled={busy}>Cancel</button>
                  <button className="primary" onClick={activate} disabled={busy || !text.trim()}>
                    {busy ? <><span className="spinner" aria-hidden />Activating…</> : 'Activate'}
                  </button>
                </div>
              </>
            )}
      </div>
    </div>
  )
}

function Result({ r, titleId, closeRef, onClose, onAgain }: {
  r: LicenseActivation
  titleId: string
  closeRef: React.RefObject<HTMLButtonElement | null>
  onClose: () => void
  onAgain: () => void
}) {
  const o = outcomeOf(r)
  const title = o === 'activated' ? 'Enterprise activated'
    : o === 'warning' ? 'Enterprise activated, with a warning'
      : 'Licence installed, but nothing is unlocked'
  return (
    <div className="upgrade-result" role="status">
      <StatusMark outcome={o} />
      <h3 id={titleId}>{title}</h3>
      {o === 'activated' && (
        <p className="modal-body">
          Licence <b>{r.activated}</b> for <b>{r.customer}</b>. The paid features are unlocked now —
          close this and they are ready.
        </p>
      )}
      {o !== 'activated' && (
        <p className="modal-body">
          {r.reason && <>{r.reason}. </>}
          {r.action}
        </p>
      )}
      {r.notes && r.notes.length > 0 && (
        <ul className="upgrade-notes">
          {r.notes.map(n => <li key={n}>{n}</li>)}
        </ul>
      )}
      <div className="modal-actions centered">
        {o === 'locked' && <button className="ghost" onClick={onAgain}>Try another licence</button>}
        <button ref={closeRef} className="primary" onClick={onClose}>Close</button>
      </div>
    </div>
  )
}

/** The circle at the top of a result: a tick, or a mark that asks for attention. */
function StatusMark({ outcome }: { outcome: Outcome }) {
  return (
    <svg className={'status-mark ' + outcome} width="56" height="56" viewBox="0 0 56 56" aria-hidden>
      <circle cx="28" cy="28" r="26" />
      {outcome === 'activated'
        ? <path d="M17 29l7.5 7.5L39.5 21" />
        : <><path d="M28 16v15" /><path d="M28 39.5v.5" /></>}
    </svg>
  )
}

function StandardBuild({ titleId, onClose }: { titleId: string; onClose: () => void }) {
  const unix = `curl -fsSL https://raw.githubusercontent.com/${BRAND.repo}/main/deploy/install.sh | ${BRAND.envPrefix}EDITION=enterprise sh`
  const win = `$env:${BRAND.envPrefix}EDITION = 'enterprise'; irm https://raw.githubusercontent.com/${BRAND.repo}/main/deploy/install.ps1 | iex`
  return (
    <>
      <h3 id={titleId}>Moving to {BRAND.product} Enterprise</h3>
      <p className="modal-body">
        This is the standard edition, and a licence cannot change that: the paid features are not
        in this build at all, which is what keeps it free to share. Install the Enterprise build
        over it. That replaces the <code>{BRAND.cli}</code> program and nothing else, so your
        databases, accounts and settings stay where they are.
      </p>
      <p className="modal-body"><b>1. Install it</b> — macOS and Linux:</p>
      <pre className="cmd"><code>{unix}</code></pre>
      <p className="modal-body">Windows (PowerShell):</p>
      <pre className="cmd"><code>{win}</code></pre>
      <p className="modal-body">
        <b>2. Restart onto it</b> — the running engine keeps the old program until it stops:
        {' '}<code>{BRAND.cli} stop</code>, then <code>{BRAND.cli} setup</code> on macOS or Windows (which also
        puts the Enterprise engine into the VM), or <code>sudo {BRAND.cli} start</code> on Linux.
      </p>
      <p className="modal-body">
        <b>3. Come back here</b> and choose Upgrade again to activate your licence.
      </p>
      <div className="modal-actions">
        <button className="primary" onClick={onClose} autoFocus>Close</button>
      </div>
    </>
  )
}
