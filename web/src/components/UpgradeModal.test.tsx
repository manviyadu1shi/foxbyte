// SPDX-License-Identifier: AGPL-3.0-or-later
//
// The Upgrade modal's rules, asserted. Each is a decision a later change could
// quietly undo: a green tick on an expired licence, a licence form shown to a
// build that no licence can unlock, a modal that closes mid-activation.
import { render, screen, fireEvent, waitFor, cleanup, act } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { License, LicenseActivation } from '../api'

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, activateLicense: vi.fn() }
})

import UpgradeModal, { outcomeOf } from './UpgradeModal'
import { activateLicense } from '../api'

const mockActivate = vi.mocked(activateLicense)

const lic = (over: Partial<License> = {}): License => ({
  edition: 'enterprise', present: false, state: 'invalid', unlocks: false,
  features: [], reason: '', action: '', you_are_admin: true, ...over,
})
const activation = (over: Partial<LicenseActivation> = {}): LicenseActivation => ({
  activated: 'FB-0001', customer: 'Acme Ltd', edition: 'enterprise',
  state: 'active', unlocks: true, reason: '', action: '', notes: [], ...over,
})
const licenceText = '{"id":"FB-0001","customer":"Acme Ltd","signature":"abc"}'

// clearAllMocks, as the licence page's tests do, and not mockReset: in this
// vitest, resetting a module-mocked function makes a rejection the component
// *catches* still fail the test — reproduced in isolation, and nothing to do
// with the modal.
beforeEach(() => vi.clearAllMocks())
afterEach(cleanup)

describe('outcomeOf', () => {
  // Three outcomes, not two: the engine installs an expired licence or one
  // naming another machine, and each needs different words.
  it('calls only an active licence that unlocks a plain success', () => {
    expect(outcomeOf(activation())).toBe('activated')
    expect(outcomeOf(activation({ state: 'warning' }))).toBe('warning')
    expect(outcomeOf(activation({ state: 'lapsed', unlocks: false }))).toBe('locked')
    expect(outcomeOf(activation({ state: 'invalid', unlocks: false }))).toBe('locked')
  })
})

describe('on an Enterprise build', () => {
  it('activates a pasted licence, with a loader while it works, and says so with a tick', async () => {
    let resolve!: (r: LicenseActivation) => void
    mockActivate.mockReturnValue(new Promise(r => { resolve = r }))
    const onClose = vi.fn()
    render(<UpgradeModal lic={lic()} onClose={onClose} />)

    fireEvent.click(screen.getByRole('tab', { name: 'Paste licence' }))
    const activate = screen.getByRole('button', { name: 'Activate' })
    expect((activate as HTMLButtonElement).disabled).toBe(true) // nothing to activate yet
    fireEvent.change(screen.getByLabelText('Licence text'), { target: { value: licenceText } })
    fireEvent.click(activate)

    // The loader, and nothing that would close the modal mid-flight.
    expect(await screen.findByText('Activating…')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Cancel' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).not.toHaveBeenCalled()
    expect(mockActivate).toHaveBeenCalledWith(licenceText)

    await act(async () => resolve(activation()))
    expect(screen.getByRole('heading', { name: 'Enterprise activated' })).toBeTruthy()
    expect(document.querySelector('.status-mark.activated')).not.toBeNull()
    expect(screen.getByText(/FB-0001/)).toBeTruthy()

    // Closing reports that something changed, which is what makes the page
    // behind it re-check every lock.
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(onClose).toHaveBeenCalledWith(true)
  })

  it('activates an uploaded file the same way', async () => {
    mockActivate.mockResolvedValue(activation())
    render(<UpgradeModal lic={lic()} onClose={vi.fn()} />)
    const file = new File([licenceText], 'acme.lic', { type: 'application/json' })
    fireEvent.change(screen.getByLabelText('Licence file'), { target: { files: [file] } })
    expect(await screen.findByText('acme.lic')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Activate' }))
    await screen.findByRole('heading', { name: 'Enterprise activated' })
    expect(mockActivate).toHaveBeenCalledWith(licenceText)
  })

  it('refuses a file far too large to be a licence, before sending anything', async () => {
    render(<UpgradeModal lic={lic()} onClose={vi.fn()} />)
    const big = new File(['x'.repeat(70 * 1024)], 'backup.tar', { type: 'application/x-tar' })
    fireEvent.change(screen.getByLabelText('Licence file'), { target: { files: [big] } })
    expect(await screen.findByRole('alert')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Activate' }) as HTMLButtonElement).disabled).toBe(true)
    expect(mockActivate).not.toHaveBeenCalled()
  })

  // A green tick on a licence that still wants something done would be the
  // one lie in the modal.
  it('says "with a warning" when the licence works but wants attention', async () => {
    mockActivate.mockResolvedValue(activation({
      state: 'warning', reason: 'this licence was activated on a different machine',
      action: 'run `fox license rebind` to move it here',
    }))
    render(<UpgradeModal lic={lic()} onClose={vi.fn()} />)
    fireEvent.click(screen.getByRole('tab', { name: 'Paste licence' }))
    fireEvent.change(screen.getByLabelText('Licence text'), { target: { value: licenceText } })
    fireEvent.click(screen.getByRole('button', { name: 'Activate' }))
    await screen.findByRole('heading', { name: 'Enterprise activated, with a warning' })
    expect(document.querySelector('.status-mark.warning')).not.toBeNull()
    expect(screen.getByText(/different machine/)).toBeTruthy()
  })

  it('does not call an expired licence a success, and offers to try another', async () => {
    mockActivate.mockResolvedValue(activation({
      state: 'lapsed', unlocks: false, reason: 'this licence expired on 1 October 2026', action: 'renew it',
    }))
    render(<UpgradeModal lic={lic()} onClose={vi.fn()} />)
    fireEvent.click(screen.getByRole('tab', { name: 'Paste licence' }))
    fireEvent.change(screen.getByLabelText('Licence text'), { target: { value: licenceText } })
    fireEvent.click(screen.getByRole('button', { name: 'Activate' }))
    await screen.findByRole('heading', { name: 'Licence installed, but nothing is unlocked' })
    expect(screen.queryByRole('heading', { name: 'Enterprise activated' })).toBeNull()
    expect(document.querySelector('.status-mark.locked')).not.toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Try another licence' }))
    expect(screen.getByRole('button', { name: 'Activate' })).toBeTruthy()
  })

  it('shows the engine\'s refusal and stays on the form', async () => {
    mockActivate.mockRejectedValue(new Error('the licence signature does not verify — nothing was installed'))
    const onClose = vi.fn()
    render(<UpgradeModal lic={lic()} onClose={onClose} />)
    fireEvent.click(screen.getByRole('tab', { name: 'Paste licence' }))
    fireEvent.change(screen.getByLabelText('Licence text'), { target: { value: licenceText } })
    fireEvent.click(screen.getByRole('button', { name: 'Activate' }))
    expect((await screen.findByRole('alert')).textContent).toMatch(/nothing was installed/)
    expect(screen.getByRole('button', { name: 'Activate' })).toBeTruthy()
    expect(onClose).not.toHaveBeenCalled()

    // Found walking the flow in a real browser: the refusal stayed on screen
    // while the licence was being corrected, sitting under text that was by
    // then a perfectly good licence. Editing takes it away.
    fireEvent.change(screen.getByLabelText('Licence text'), { target: { value: licenceText + ' ' } })
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('passes on what is still left to do', async () => {
    mockActivate.mockResolvedValue(activation({
      notes: ['Your computer keeps its own copy the next time you run `fox start` or `fox license show` there.'],
    }))
    render(<UpgradeModal lic={lic()} onClose={vi.fn()} />)
    fireEvent.click(screen.getByRole('tab', { name: 'Paste licence' }))
    fireEvent.change(screen.getByLabelText('Licence text'), { target: { value: licenceText } })
    fireEvent.click(screen.getByRole('button', { name: 'Activate' }))
    expect(await screen.findByText(/keeps its own copy/)).toBeTruthy()
  })

  it('closing without activating reports that nothing changed', () => {
    const onClose = vi.fn()
    render(<UpgradeModal lic={lic()} onClose={onClose} />)
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledWith(false)
  })
})

describe('on a Standard build', () => {
  // No licence can unlock code that is not in the binary, so the modal must
  // not offer to take one.
  it('shows how to install the Enterprise build instead of a licence form', () => {
    render(<UpgradeModal lic={lic({ edition: 'standard' })} onClose={vi.fn()} />)
    expect(screen.queryByRole('button', { name: 'Activate' })).toBeNull()
    expect(screen.queryByLabelText('Licence file')).toBeNull()
    expect(screen.getByText(/EDITION=enterprise sh/)).toBeTruthy()
    expect(screen.getByText(/EDITION = 'enterprise'/)).toBeTruthy()
    // And the step people miss: the running engine keeps the old program.
    expect(screen.getByText(/Restart onto it/)).toBeTruthy()
  })
})

describe('the shared refresh', () => {
  it('makes every mounted licence reader ask again', async () => {
    const api = await import('../api')
    const spy = vi.spyOn(api, 'getLicense').mockResolvedValue(lic())
    const { useLicense, refreshEntitlement } = await import('../features')
    function Reader() { const l = useLicense(); return <span>{l ? l.state : 'loading'}</span> }
    render(<Reader />)
    await waitFor(() => expect(spy).toHaveBeenCalledTimes(1))
    spy.mockResolvedValue(lic({ present: true, state: 'active', unlocks: true }))
    act(() => refreshEntitlement())
    await waitFor(() => expect(spy).toHaveBeenCalledTimes(2))
    expect(await screen.findByText('active')).toBeTruthy()
    spy.mockRestore()
  })
})
