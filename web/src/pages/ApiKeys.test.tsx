// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest'
import { keyKind } from './ApiKeys'

// The kind column is how someone reviewing their keys tells the one they gave
// an agent from their own: it must never call an agent key an account key.
describe('keyKind', () => {
  const k = { id: '1', name: 'n', prefix: 'key_x', created: 0 }
  it('names each kind of key', () => {
    expect(keyKind(k)).toBe('account')
    expect(keyKind({ ...k, kind: 'agent' })).toBe('agent')
    expect(keyKind({ ...k, scope: 'agent-7' })).toBe('branch · agent-7')
    expect(keyKind({ ...k, kind: 'realtime', scope: 'main' })).toBe('realtime · main')
  })
})
