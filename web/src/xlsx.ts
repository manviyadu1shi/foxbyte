// SPDX-License-Identifier: AGPL-3.0-or-later
//
// An Excel workbook of a result, for the console's Export menu (point 1.2).
// `fox sql --format xlsx` builds the same worksheet in Go (internal/resultfmt),
// and the two are held to it by xlsx.vectors.json.
//
// A .xlsx file is a zip of a few XML files. The zip here stores them
// uncompressed, which every spreadsheet opens and which needs no library:
// the only arithmetic is a CRC-32. Each value is written as what it is (a
// number as a number, true/false as a boolean, text as text), so a spreadsheet
// never reads a value as a formula.

const MAX_CELL_TEXT = 32767

function colName(i: number): string {
  let s = ''
  for (i++; i > 0; i = Math.floor((i - 1) / 26)) s = String.fromCharCode(65 + ((i - 1) % 26)) + s
  return s
}

function xmlText(s: string): string {
  let out = ''
  for (const ch of s) {
    const c = ch.codePointAt(0)!
    if (ch === '&') out += '&amp;'
    else if (ch === '<') out += '&lt;'
    else if (ch === '>') out += '&gt;'
    else if (ch === '"') out += '&quot;'
    else if ((c < 0x20 && c !== 0x09 && c !== 0x0a && c !== 0x0d) || c === 0xfffe || c === 0xffff) continue
    else out += ch
  }
  return out
}

function truncate(s: string, n: number): string {
  const cps = Array.from(s)
  return cps.length <= n ? s : cps.slice(0, n).join('')
}

// sheetXML is the worksheet: the column names, then a row per result row.
export function sheetXML(cols: string[], rows: unknown[][]): string {
  let out = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n' +
    '<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>'
  const cell = (ref: string, v: unknown) => {
    if (v === null || v === undefined) return
    if (typeof v === 'boolean') { out += `<c r="${ref}" t="b"><v>${v ? 1 : 0}</v></c>`; return }
    if (typeof v === 'number') { out += `<c r="${ref}"><v>${String(v)}</v></c>`; return }
    const s = typeof v === 'string' ? v : JSON.stringify(v)
    out += `<c r="${ref}" t="inlineStr"><is><t xml:space="preserve">${xmlText(truncate(s, MAX_CELL_TEXT))}</t></is></c>`
  }
  const row = (n: number, vals: unknown[]) => {
    out += `<row r="${n}">`
    vals.forEach((v, i) => cell(colName(i) + n, v))
    out += '</row>'
  }
  row(1, cols)
  rows.forEach((r, i) => row(i + 2, r))
  return out + '</sheetData></worksheet>'
}

const PARTS: [string, string][] = [
  ['[Content_Types].xml', '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>'],
  ['_rels/.rels', '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>'],
  ['xl/workbook.xml', '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Result" sheetId="1" r:id="rId1"/></sheets></workbook>'],
  ['xl/_rels/workbook.xml.rels', '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>'],
]

let crcTable: Uint32Array | null = null
export function crc32(data: Uint8Array): number {
  if (!crcTable) {
    crcTable = new Uint32Array(256)
    for (let n = 0; n < 256; n++) {
      let c = n
      for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1
      crcTable[n] = c >>> 0
    }
  }
  let crc = 0xffffffff
  for (const b of data) crc = crcTable[(crc ^ b) & 0xff] ^ (crc >>> 8)
  return (crc ^ 0xffffffff) >>> 0
}

// zipStore writes a zip of files, stored (not compressed).
export function zipStore(files: [string, Uint8Array][]): Uint8Array {
  const enc = new TextEncoder()
  const chunks: Uint8Array[] = []
  const central: Uint8Array[] = []
  let offset = 0
  // 2026-01-01 00:00 in MS-DOS form, fixed so a result makes the same file.
  const dosTime = 0, dosDate = ((2026 - 1980) << 9) | (1 << 5) | 1
  for (const [name, data] of files) {
    const n = enc.encode(name)
    const crc = crc32(data)
    const local = new DataView(new ArrayBuffer(30))
    local.setUint32(0, 0x04034b50, true); local.setUint16(4, 20, true); local.setUint16(6, 0x0800, true)
    local.setUint16(8, 0, true); local.setUint16(10, dosTime, true); local.setUint16(12, dosDate, true)
    local.setUint32(14, crc, true); local.setUint32(18, data.length, true); local.setUint32(22, data.length, true)
    local.setUint16(26, n.length, true); local.setUint16(28, 0, true)
    chunks.push(new Uint8Array(local.buffer), n, data)
    const cen = new DataView(new ArrayBuffer(46))
    cen.setUint32(0, 0x02014b50, true); cen.setUint16(4, 20, true); cen.setUint16(6, 20, true); cen.setUint16(8, 0x0800, true)
    cen.setUint16(10, 0, true); cen.setUint16(12, dosTime, true); cen.setUint16(14, dosDate, true)
    cen.setUint32(16, crc, true); cen.setUint32(20, data.length, true); cen.setUint32(24, data.length, true)
    cen.setUint16(28, n.length, true); cen.setUint32(42, offset, true)
    central.push(new Uint8Array(cen.buffer), n)
    offset += 30 + n.length + data.length
  }
  const cenSize = central.reduce((a, c) => a + c.length, 0)
  const end = new DataView(new ArrayBuffer(22))
  end.setUint32(0, 0x06054b50, true); end.setUint16(8, files.length, true); end.setUint16(10, files.length, true)
  end.setUint32(12, cenSize, true); end.setUint32(16, offset, true)
  const all = [...chunks, ...central, new Uint8Array(end.buffer)]
  const out = new Uint8Array(all.reduce((a, c) => a + c.length, 0))
  let p = 0
  for (const c of all) { out.set(c, p); p += c.length }
  return out
}

// xlsx is a workbook of one sheet, "Result".
export function xlsx(cols: string[], rows: unknown[][]): Uint8Array {
  const enc = new TextEncoder()
  return zipStore([...PARTS.map(([n, b]) => [n, enc.encode(b)] as [string, Uint8Array]),
    ['xl/worksheets/sheet1.xml', enc.encode(sheetXML(cols, rows))]])
}
