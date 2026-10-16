// SPDX-License-Identifier: AGPL-3.0-or-later
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ExportMenu, { exportFile, fileName } from './ExportMenu'

const cols = ['id', 'f']
const rows = [[1, '=1+1'], [2, null]]

describe('ExportMenu', () => {
  let blobs: Blob[]
  beforeEach(() => {
    localStorage.clear()
    blobs = []
    URL.createObjectURL = vi.fn((b: Blob) => { blobs.push(b); return 'blob:x' })
    URL.revokeObjectURL = vi.fn()
  })
  afterEach(cleanup)

  it('downloads CSV by default, spreadsheet-safe, named after the table', async () => {
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    render(<ExportMenu cols={cols} rows={rows} selected={0} sql="SELECT * FROM public.orders" />)
    fireEvent.click(screen.getByRole('button', { name: /export ▾/i }))
    fireEvent.click(screen.getByRole('button', { name: /download all 2 rows/i }))
    expect(click).toHaveBeenCalled()
    expect(await blobs[0].text()).toBe("id,f\n1,'=1+1\n2,\n")
    expect(screen.getByText(/saved orders-\d{8}-\d{4}\.csv/i)).toBeTruthy()
    click.mockRestore()
  })

  it('exports the ticked rows only, and remembers the format', async () => {
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    render(<ExportMenu cols={cols} rows={[rows[1]]} selected={1} />)
    fireEvent.click(screen.getByRole('button', { name: /export ▾/i }))
    fireEvent.change(screen.getByLabelText('Export format'), { target: { value: 'json' } })
    fireEvent.click(screen.getByRole('button', { name: /download 1 selected row$/i }))
    expect(JSON.parse(await blobs[0].text())).toEqual([{ id: 2, f: null }])
    expect(JSON.parse(localStorage.getItem('fox.export') ?? '{}').format).toBe('json')
    click.mockRestore()
  })
})

describe('exportFile and fileName', () => {
  it('builds a workbook for Excel', async () => {
    const b = exportFile(cols, rows, 'xlsx', true)
    expect(b.type).toContain('spreadsheetml')
    const bytes = new Uint8Array(await b.arrayBuffer())
    expect([...bytes.slice(0, 4)]).toEqual([0x50, 0x4b, 0x03, 0x04]) // a zip
  })
  it('can leave a formula as it is', async () => {
    expect(await exportFile(cols, rows, 'csv', false).text()).toBe('id,f\n1,=1+1\n2,\n')
  })
  it('names a file after the table and the minute', () => {
    const at = new Date(2026, 9, 17, 14, 5)
    expect(fileName('select * from "Sales Q3"', 'csv', at)).toBe('Sales_Q3-20261017-1405.csv')
    expect(fileName('select 1', 'xlsx', at)).toBe('result-20261017-1405.xlsx')
    expect(fileName('SELECT * FROM public.orders o', 'json', at)).toBe('orders-20261017-1405.json')
  })
})
