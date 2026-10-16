// SPDX-License-Identifier: AGPL-3.0-or-later
import { useEffect, useRef, useState } from 'react'
import { FORMATS, formatResult, guessTable, type CopyFormat } from '../copyformat'

// The format and switches someone last used, kept in this browser only; a
// blocked or empty store just means the defaults.
const KEY = 'fox.copy'
type Saved = { format: CopyFormat; header: boolean; safe: boolean }
function load(): Saved {
  const d: Saved = { format: 'tsv', header: true, safe: true }
  try {
    const s = JSON.parse(localStorage.getItem(KEY) ?? 'null')
    if (s && FORMATS.some(f => f.id === s.format)) return { ...d, ...s }
  } catch { /* defaults */ }
  return d
}
function save(s: Saved) {
  try { localStorage.setItem(KEY, JSON.stringify(s)) } catch { /* not kept */ }
}

/**
 * Copy a result (or the rows ticked in it) as TSV, CSV, Markdown, JSON or
 * INSERT statements (point 1.1). `fox sql --format` writes the same text: both
 * follow copyformat.vectors.json.
 *
 * The clipboard can refuse (an older browser, a page not served over HTTPS), so
 * the text is then shown, selected, for a manual copy.
 */
export default function CopyMenu({ cols, rows, selected, sql }: {
  cols: string[]
  rows: unknown[][]
  // How many rows are ticked; rows is already only those when it is not 0.
  selected: number
  sql?: string
}) {
  const [open, setOpen] = useState(false)
  const [opts, setOpts] = useState<Saved>(load)
  const [table, setTable] = useState(() => guessTable(sql ?? ''))
  const [state, setState] = useState<'idle' | 'copied' | 'manual'>('idle')
  const [text, setText] = useState('')
  const [err, setErr] = useState('')
  const box = useRef<HTMLDivElement>(null)
  const area = useRef<HTMLTextAreaElement>(null)

  useEffect(() => { setTable(guessTable(sql ?? '')) }, [sql])
  useEffect(() => {
    if (!open) return
    const onDoc = (e: MouseEvent) => { if (box.current && !box.current.contains(e.target as Node)) setOpen(false) }
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('mousedown', onDoc)
    window.addEventListener('keydown', onKey)
    return () => { document.removeEventListener('mousedown', onDoc); window.removeEventListener('keydown', onKey) }
  }, [open])
  useEffect(() => { if (state === 'manual') area.current?.select() }, [state])

  const set = (p: Partial<Saved>) => { const n = { ...opts, ...p }; setOpts(n); save(n); setState('idle'); setErr('') }
  const delimited = opts.format === 'csv' || opts.format === 'tsv'
  const what = selected > 0 ? `${selected} selected row${selected === 1 ? '' : 's'}` : `all ${rows.length} row${rows.length === 1 ? '' : 's'}`

  const copy = async () => {
    setErr('')
    let t: string
    try {
      t = formatResult(cols, rows, { format: opts.format, header: opts.header, safe: opts.safe, table })
    } catch (e) { setErr((e as Error).message); return }
    setText(t)
    try {
      await navigator.clipboard.writeText(t)
      setState('copied')
      setTimeout(() => setState('idle'), 2000)
    } catch {
      setState('manual')
    }
  }
  const keys = /Mac|iPhone|iPad/.test(navigator.userAgent) ? '⌘C' : 'Ctrl+C'

  return (
    <div className="copy-menu" ref={box}>
      <button type="button" className="ghost" aria-haspopup="dialog" aria-expanded={open} onClick={() => setOpen(o => !o)}>
        {state === 'copied' ? 'Copied ✓' : 'Copy ▾'}
      </button>
      {open && (
        <div className="copy-pop" role="dialog" aria-label="Copy the result">
          <label className="fld">Format
            <select value={opts.format} onChange={e => set({ format: e.target.value as CopyFormat })} aria-label="Copy format">
              {FORMATS.map(f => <option key={f.id} value={f.id}>{f.label}</option>)}
            </select>
          </label>
          {delimited && (
            <>
              <label className="check-row"><input type="checkbox" checked={opts.header} onChange={e => set({ header: e.target.checked })} /> Column names first</label>
              <label className="check-row" title="Text a spreadsheet would run as a formula (=, +, -, @) gets a leading apostrophe. Numbers are left alone.">
                <input type="checkbox" checked={opts.safe} onChange={e => set({ safe: e.target.checked })} /> Spreadsheet-safe
              </label>
            </>
          )}
          {opts.format === 'insert' && (
            <label className="fld">Table
              <input value={table} onChange={e => { setTable(e.target.value); setState('idle'); setErr('') }} placeholder="schema.table" aria-label="INSERT into table" />
            </label>
          )}
          {err && <div className="err">{err}</div>}
          <button type="button" className="primary" onClick={copy} disabled={rows.length === 0}>
            {state === 'copied' ? 'Copied ✓' : `Copy ${what}`}
          </button>
          {state === 'manual' && (
            <>
              <p className="muted">The browser would not let the page copy. The text is selected: press {keys}.</p>
              <textarea ref={area} className="copy-manual" readOnly value={text} aria-label="Text to copy" />
            </>
          )}
        </div>
      )}
    </div>
  )
}

// cellText is one value as the clipboard should hold it: the value itself, with
// NULL as an empty string rather than the word the grid shows.
export function cellText(v: unknown): string {
  if (v === null || v === undefined) return ''
  return typeof v === 'string' ? v : String(v)
}
