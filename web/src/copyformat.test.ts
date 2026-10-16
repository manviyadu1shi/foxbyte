// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest'
import vectors from './copyformat.vectors.json'
import { formatResult, guessTable, ident, type CopyFormat } from './copyformat'

type Vector = { name: string; columns: string[]; rows: unknown[][]; format: CopyFormat; header: boolean; safe: boolean; table?: string; want: string }

// The same cases `fox sql --format` is held to (internal/resultfmt): the console
// and the CLI write a result the same way, or one of these fails.
describe('copy formats, shared with the CLI', () => {
  for (const v of vectors as Vector[]) {
    it(v.name, () => {
      expect(formatResult(v.columns, v.rows, { format: v.format, header: v.header, safe: v.safe, table: v.table })).toBe(v.want)
    })
  }
})

describe('the rest', () => {
  it('refuses INSERTs with no table', () => {
    expect(() => formatResult(['a'], [[1]], { format: 'insert' })).toThrow(/table name/)
  })
  it('quotes names that need it', () => {
    expect(ident('id')).toBe('id')
    expect(ident('user')).toBe('"user"')
    expect(ident('Name')).toBe('"Name"')
  })
  it('suggests the table a query reads from', () => {
    expect(guessTable('SELECT * FROM public.orders WHERE x')).toBe('public.orders')
    expect(guessTable('-- from nowhere\nselect a from "My T" t')).toBe('"My T"')
    expect(guessTable('SELECT 1')).toBe('')
  })
})
