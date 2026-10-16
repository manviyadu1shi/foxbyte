// SPDX-License-Identifier: AGPL-3.0-or-later
//
// A query result as CSV, tab-separated values, Markdown, JSON or INSERT
// statements, for the console's Copy menu.
//
// `fox sql --format` does the same in Go (internal/resultfmt), and the two are
// held to one behaviour by one set of cases both test suites read,
// copyformat.vectors.json: change one without the other and a test fails.
// Values are what the query API returns: null, booleans, numbers and strings
// (JSON and numeric columns arrive as text, and stay text).

export type CopyFormat = 'csv' | 'tsv' | 'markdown' | 'json' | 'insert'

export type CopyOptions = {
  format: CopyFormat
  // The column names first, for CSV and TSV. Markdown always has them.
  header?: boolean
  // Text a spreadsheet would run as a formula gets a leading apostrophe, for
  // CSV and TSV. Numbers, negative ones included, are left alone.
  safe?: boolean
  // The table INSERT statements name.
  table?: string
}

export const FORMATS: { id: CopyFormat; label: string }[] = [
  { id: 'tsv', label: 'Tab-separated (spreadsheets)' },
  { id: 'csv', label: 'CSV' },
  { id: 'markdown', label: 'Markdown table' },
  { id: 'json', label: 'JSON' },
  { id: 'insert', label: 'INSERT statements' },
]

function text(v: unknown): [string, boolean] {
  if (v === null || v === undefined) return ['', true]
  if (typeof v === 'string') return [v, false]
  if (typeof v === 'boolean') return [v ? 'true' : 'false', false]
  if (typeof v === 'number') return [String(v), false]
  return [typeof v === 'object' ? JSON.stringify(v) : String(v), false]
}

const looksNumeric = /^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$/

function formulaStart(s: string): boolean {
  return ['=', '+', '-', '@', '\t', '\r', '\n', '＝', '＋', '－', '＠'].some(p => s.startsWith(p))
}

function delimited(cols: string[], rows: unknown[][], sep: string, o: CopyOptions): string {
  const field = (s: string) => (s.includes(sep) || /["\r\n]/.test(s)) ? `"${s.replace(/"/g, '""')}"` : s
  const lines: string[] = []
  if (o.header) lines.push(cols.map(field).join(sep))
  for (const r of rows) {
    lines.push(r.map(v => {
      let [s] = text(v)
      if (o.safe && typeof v !== 'number' && !looksNumeric.test(s) && formulaStart(s)) s = "'" + s
      return field(s)
    }).join(sep))
  }
  return lines.map(l => l + '\n').join('')
}

function markdown(cols: string[], rows: unknown[][]): string {
  const cell = (s: string) => s.replace(/\\/g, '\\\\').replace(/\|/g, '\\|').replace(/\r\n|\n|\r/g, '<br>')
  let out = `| ${cols.map(cell).join(' | ')} |\n| ${cols.map(() => '---').join(' | ')} |\n`
  for (const r of rows) {
    out += `| ${r.map(v => { const [s, nul] = text(v); return cell(nul ? 'NULL' : s) }).join(' | ')} |\n`
  }
  return out
}

function keys(cols: string[]): string[] {
  const seen: Record<string, number> = {}
  return cols.map(c => {
    seen[c] = (seen[c] ?? 0) + 1
    return seen[c] > 1 ? `${c}_${seen[c]}` : c
  })
}

function jsonRows(cols: string[], rows: unknown[][]): string {
  if (rows.length === 0) return '[]\n'
  const ks = keys(cols)
  const objs = rows.map(r => {
    const lines = r.map((v, i) => `    ${JSON.stringify(ks[i])}: ${JSON.stringify(v === undefined ? null : v)}`)
    return `  {\n${lines.join(',\n')}\n  }`
  })
  return `[\n${objs.join(',\n')}\n]\n`
}

const RESERVED = new Set(`all analyse analyze and any array as asc asymmetric both case cast check
  collate column constraint create current_catalog current_date current_role current_time
  current_timestamp current_user default deferrable desc distinct do else end except false fetch
  for foreign from grant group having in initially intersect into lateral leading limit localtime
  localtimestamp not null offset on only or order placing primary references returning select
  session_user some symmetric system_user table then to trailing true union unique user using
  variadic when where window with`.split(/\s+/).filter(Boolean))

export function ident(s: string): string {
  return /^[a-z_][a-z0-9_$]*$/.test(s) && !RESERVED.has(s) ? s : `"${s.replace(/"/g, '""')}"`
}

function tableName(t: string): string {
  t = t.trim()
  if (t.includes('"')) return t
  return t.split('.').map(ident).join('.')
}

function literal(v: unknown): string {
  if (v === null || v === undefined) return 'NULL'
  if (typeof v === 'boolean') return v ? 'TRUE' : 'FALSE'
  const [s] = text(v)
  if (typeof v === 'number') return s
  return `'${s.replace(/'/g, "''")}'`
}

function inserts(cols: string[], rows: unknown[][], table: string): string {
  const prefix = `INSERT INTO ${tableName(table)} (${cols.map(ident).join(', ')}) VALUES (`
  return rows.map(r => prefix + r.map(literal).join(', ') + ');\n').join('')
}

// formatResult writes cols and rows in the chosen format.
export function formatResult(cols: string[], rows: unknown[][], o: CopyOptions): string {
  switch (o.format) {
    case 'csv': return delimited(cols, rows, ',', o)
    case 'tsv': return delimited(cols, rows, '\t', o)
    case 'markdown': return markdown(cols, rows)
    case 'json': return jsonRows(cols, rows)
    case 'insert':
      if (!o.table?.trim()) throw new Error('INSERT statements need a table name')
      return inserts(cols, rows, o.table)
  }
}

// guessTable is the first table a query reads FROM, to offer as the INSERT
// target: a suggestion the person can change, never a decision.
export function guessTable(sql: string): string {
  const m = /\bfrom\s+((?:"[^"]+"|[A-Za-z_][\w$]*)(?:\.(?:"[^"]+"|[A-Za-z_][\w$]*))?)/i.exec(sql.replace(/--[^\n]*/g, ''))
  return m ? m[1] : ''
}
