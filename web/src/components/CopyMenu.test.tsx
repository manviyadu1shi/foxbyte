// SPDX-License-Identifier: AGPL-3.0-or-later
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import CopyMenu, { cellText } from './CopyMenu'

const cols = ['id', 'name']
const rows = [[1, 'Ada'], [2, null]]

describe('CopyMenu', () => {
  let write: ReturnType<typeof vi.fn>
  beforeEach(() => {
    localStorage.clear()
    write = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: write }, configurable: true })
  })
  afterEach(cleanup)

  it('copies the whole result as tab-separated values by default', async () => {
    render(<CopyMenu cols={cols} rows={rows} selected={0} sql="SELECT * FROM people" />)
    fireEvent.click(screen.getByRole('button', { name: /copy ▾/i }))
    fireEvent.click(screen.getByRole('button', { name: /copy all 2 rows/i }))
    await waitFor(() => expect(write).toHaveBeenCalledWith('id\tname\n1\tAda\n2\t\n'))
  })

  it('copies in the chosen format, and remembers it', async () => {
    render(<CopyMenu cols={cols} rows={rows} selected={0} />)
    fireEvent.click(screen.getByRole('button', { name: /copy ▾/i }))
    fireEvent.change(screen.getByLabelText('Copy format'), { target: { value: 'markdown' } })
    fireEvent.click(screen.getByRole('button', { name: /copy all/i }))
    await waitFor(() => expect(write).toHaveBeenCalledWith('| id | name |\n| --- | --- |\n| 1 | Ada |\n| 2 | NULL |\n'))
    expect(JSON.parse(localStorage.getItem('fox.copy') ?? '{}').format).toBe('markdown')
  })

  it('names only the ticked rows, and offers INSERTs into the table the query read', async () => {
    render(<CopyMenu cols={cols} rows={[rows[0]]} selected={1} sql="select * from public.people" />)
    fireEvent.click(screen.getByRole('button', { name: /copy ▾/i }))
    fireEvent.change(screen.getByLabelText('Copy format'), { target: { value: 'insert' } })
    expect((screen.getByLabelText('INSERT into table') as HTMLInputElement).value).toBe('public.people')
    fireEvent.click(screen.getByRole('button', { name: /copy 1 selected row$/i }))
    await waitFor(() => expect(write).toHaveBeenCalledWith("INSERT INTO public.people (id, name) VALUES (1, 'Ada');\n"))
  })

  it('says what is wrong instead of copying an INSERT with no table', async () => {
    render(<CopyMenu cols={cols} rows={rows} selected={0} />)
    fireEvent.click(screen.getByRole('button', { name: /copy ▾/i }))
    fireEvent.change(screen.getByLabelText('Copy format'), { target: { value: 'insert' } })
    fireEvent.click(screen.getByRole('button', { name: /copy all/i }))
    expect(await screen.findByText(/need a table name/)).toBeTruthy()
    expect(write).not.toHaveBeenCalled()
  })

  it('shows the text to copy by hand when the browser refuses', async () => {
    write.mockRejectedValue(new Error('denied'))
    render(<CopyMenu cols={cols} rows={rows} selected={0} />)
    fireEvent.click(screen.getByRole('button', { name: /copy ▾/i }))
    fireEvent.click(screen.getByRole('button', { name: /copy all/i }))
    const area = await screen.findByLabelText('Text to copy') as HTMLTextAreaElement
    expect(area.value).toBe('id\tname\n1\tAda\n2\t\n')
    expect(screen.getByText(/would not let the page copy/)).toBeTruthy()
  })

  it('copies a NULL cell as nothing, not as the word', () => {
    expect(cellText(null)).toBe('')
    expect(cellText(3)).toBe('3')
    expect(cellText('x')).toBe('x')
  })
})
