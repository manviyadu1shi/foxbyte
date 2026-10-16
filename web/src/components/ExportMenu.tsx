// SPDX-License-Identifier: AGPL-3.0-or-later
import { useEffect, useRef, useState } from 'react'
import { formatResult, guessTable } from '../copyformat'
import { xlsx } from '../xlsx'

export type ExportFormat = 'csv' | 'json' | 'xlsx'

const FORMATS: { id: ExportFormat; label: string; mime: string }[] = [
  { id: 'csv', label: 'CSV', mime: 'text/csv;charset=utf-8' },
  { id: 'json', label: 'JSON', mime: 'application/json' },
  { id: 'xlsx', label: 'Excel workbook (.xlsx)', mime: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet' },
]

// The format and switch someone last used, kept in this browser only.
const KEY = 'fox.export'
type Saved = { format: ExportFormat; safe: boolean }
function load(): Saved {
  const d: Saved = { format: 'csv', safe: true }
  try {
    const s = JSON.parse(localStorage.getItem(KEY) ?? 'null')
    if (s && FORMATS.some(f => f.id === s.format)) return { ...d, ...s }
  } catch { /* defaults */ }
  return d
}
function save(s: Saved) {
  try { localStorage.setItem(KEY, JSON.stringify(s)) } catch { /* not kept */ }
}

// fileName names a download after the table the query read and the minute it
// was made: orders-20261017-1405.csv.
export function fileName(sql: string | undefined, ext: string, now = new Date()): string {
  const base = (guessTable(sql ?? '').replace(/"/g, '').split('.').pop() || 'result')
    .replace(/[^A-Za-z0-9_-]+/g, '_').slice(0, 60) || 'result'
  const p = (n: number) => String(n).padStart(2, '0')
  const stamp = `${now.getFullYear()}${p(now.getMonth() + 1)}${p(now.getDate())}-${p(now.getHours())}${p(now.getMinutes())}`
  return `${base}-${stamp}.${ext}`
}

// exportFile builds the file for a result.
export function exportFile(cols: string[], rows: unknown[][], format: ExportFormat, safe: boolean): Blob {
  const f = FORMATS.find(x => x.id === format)!
  if (format === 'xlsx') return new Blob([xlsx(cols, rows) as BlobPart], { type: f.mime })
  return new Blob([formatResult(cols, rows, { format, header: true, safe })], { type: f.mime })
}

/**
 * Export what is on screen, or the rows ticked, to a CSV, JSON or Excel file
 * (point 1.2). CSV and JSON are the same text the Copy menu and
 * `fox sql --format` write; the workbook is the same as `fox sql --format xlsx`.
 * At most what the console holds, 1,000 rows; exporting a whole result is 1.3.
 */
export default function ExportMenu({ cols, rows, selected, sql }: {
  cols: string[]
  rows: unknown[][]
  selected: number
  sql?: string
}) {
  const [open, setOpen] = useState(false)
  const [opts, setOpts] = useState<Saved>(load)
  const [done, setDone] = useState('')
  const box = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const onDoc = (e: MouseEvent) => { if (box.current && !box.current.contains(e.target as Node)) setOpen(false) }
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('mousedown', onDoc)
    window.addEventListener('keydown', onKey)
    return () => { document.removeEventListener('mousedown', onDoc); window.removeEventListener('keydown', onKey) }
  }, [open])

  const set = (p: Partial<Saved>) => { const n = { ...opts, ...p }; setOpts(n); save(n); setDone('') }
  const what = selected > 0 ? `${selected} selected row${selected === 1 ? '' : 's'}` : `all ${rows.length} row${rows.length === 1 ? '' : 's'}`

  const download = () => {
    const name = fileName(sql, opts.format)
    const url = URL.createObjectURL(exportFile(cols, rows, opts.format, opts.safe))
    const a = document.createElement('a')
    a.href = url
    a.download = name
    document.body.appendChild(a)
    a.click()
    a.remove()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
    setDone(name)
  }

  return (
    <div className="copy-menu" ref={box}>
      <button type="button" className="ghost" aria-haspopup="dialog" aria-expanded={open} onClick={() => setOpen(o => !o)}>
        Export ▾
      </button>
      {open && (
        <div className="copy-pop" role="dialog" aria-label="Export the result">
          <label className="fld">Format
            <select value={opts.format} onChange={e => set({ format: e.target.value as ExportFormat })} aria-label="Export format">
              {FORMATS.map(f => <option key={f.id} value={f.id}>{f.label}</option>)}
            </select>
          </label>
          {opts.format === 'csv' && (
            <label className="check-row" title="Text a spreadsheet would run as a formula (=, +, -, @) gets a leading apostrophe. Numbers are left alone.">
              <input type="checkbox" checked={opts.safe} onChange={e => set({ safe: e.target.checked })} /> Spreadsheet-safe
            </label>
          )}
          <button type="button" className="primary" onClick={download} disabled={rows.length === 0}>{`Download ${what}`}</button>
          {done && <p className="muted">Saved {done}</p>}
          <p className="muted">What the console holds, at most 1,000 rows.</p>
        </div>
      )}
    </div>
  )
}
