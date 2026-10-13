import { afterEach, describe, expect, it, vi } from 'vitest'
import { resetSetupNeeded, setupNeeded, setupRedirect } from './setupState'

afterEach(() => resetSetupNeeded())

describe('setup state', () => {
  it('asks the server once', async () => {
    const fetchNeeded = vi.fn().mockResolvedValue(true)
    expect(await setupNeeded(fetchNeeded)).toBe(true)
    expect(await setupNeeded(fetchNeeded)).toBe(true)
    expect(fetchNeeded).toHaveBeenCalledTimes(1)
  })

  it('treats a failed check as "set up"', async () => {
    expect(await setupNeeded(() => Promise.reject(new Error('offline')))).toBe(false)
  })

  it('redirects to and away from setup', () => {
    expect(setupRedirect(true, 'login')).toBe('/setup')
    expect(setupRedirect(true, 'setup')).toBeNull()
    expect(setupRedirect(false, 'setup')).toBe('/login')
    expect(setupRedirect(false, 'dashboard')).toBeNull()
  })
})
