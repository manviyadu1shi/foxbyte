// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest'
import vectors from './xlsx.vectors.json'
import { crc32, sheetXML, xlsx } from './xlsx'

// A store-only zip, read back: the files and their contents, with each CRC
// checked, which is what a spreadsheet does when it opens one.
function unzip(z: Uint8Array): Record<string, string> {
  const dv = new DataView(z.buffer, z.byteOffset, z.byteLength)
  const out: Record<string, string> = {}
  let p = 0
  while (dv.getUint32(p, true) === 0x04034b50) {
    const crc = dv.getUint32(p + 14, true), size = dv.getUint32(p + 18, true)
    const nlen = dv.getUint16(p + 26, true), xlen = dv.getUint16(p + 28, true)
    const name = new TextDecoder().decode(z.subarray(p + 30, p + 30 + nlen))
    const data = z.subarray(p + 30 + nlen + xlen, p + 30 + nlen + xlen + size)
    if (crc32(data) !== crc) throw new Error(`bad CRC for ${name}`)
    out[name] = new TextDecoder().decode(data)
    p += 30 + nlen + xlen + size
  }
  if (dv.getUint32(z.length - 22, true) !== 0x06054b50) throw new Error('no end of central directory')
  return out
}

// The same worksheets `fox sql --format xlsx` writes (internal/resultfmt).
describe('worksheet, shared with the CLI', () => {
  for (const v of vectors as { name: string; columns: string[]; rows: unknown[][]; sheet: string }[]) {
    it(v.name, () => expect(sheetXML(v.columns, v.rows)).toBe(v.sheet))
  }
})

describe('workbook', () => {
  it('holds the five parts Excel needs, each with a good CRC', () => {
    const files = unzip(xlsx(['id', 'name'], [[1, 'Ada']]))
    expect(Object.keys(files)).toEqual(['[Content_Types].xml', '_rels/.rels', 'xl/workbook.xml', 'xl/_rels/workbook.xml.rels', 'xl/worksheets/sheet1.xml'])
    expect(files['xl/worksheets/sheet1.xml']).toContain('<c r="A2"><v>1</v></c>')
    expect(files['xl/workbook.xml']).toContain('name="Result"')
  })
  it('computes CRC-32 as zip does', () => {
    expect(crc32(new TextEncoder().encode('123456789'))).toBe(0xcbf43926)
  })
  it('makes the same file from the same result', () => {
    expect(xlsx(['a'], [[1]])).toEqual(xlsx(['a'], [[1]]))
  })
})
