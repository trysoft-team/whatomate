import { afterEach, describe, expect, it, vi } from 'vitest'
import { isPasskeySignalSupported, signalAcceptedPasskeys, signalUnknownPasskey } from './passkeys'

const user = { id: '0f8fad5b-d9cb-469f-a165-70867728950e', email: 'ana@example.com', full_name: 'Ana' }
// base64url of the 16 UUID bytes, which is what the server uses as the WebAuthn user handle.
const userHandle = 'D4-tW9nLRp-hZXCGdyiVDg'

function stubBrowser(pkc: Record<string, unknown>) {
  vi.stubGlobal('window', { PublicKeyCredential: pkc, location: { hostname: 'app.example.com' } })
  vi.stubGlobal('navigator', { credentials: {} })
}

afterEach(() => vi.unstubAllGlobals())

describe('passkey signals', () => {
  it('sends accepted credentials and user details for this site', async () => {
    const signalAllAcceptedCredentials = vi.fn().mockResolvedValue(undefined)
    const signalCurrentUserDetails = vi.fn().mockResolvedValue(undefined)
    stubBrowser({ signalAllAcceptedCredentials, signalCurrentUserDetails })

    expect(isPasskeySignalSupported()).toBe(true)
    await signalAcceptedPasskeys(user, ['cred-a', 'cred-b'])

    expect(signalAllAcceptedCredentials).toHaveBeenCalledWith({
      rpId: 'app.example.com', userId: userHandle, allAcceptedCredentialIds: ['cred-a', 'cred-b'],
    })
    expect(signalCurrentUserDetails).toHaveBeenCalledWith({
      rpId: 'app.example.com', userId: userHandle, name: 'ana@example.com', displayName: 'Ana',
    })
  })

  it('skips user details when no passkeys remain', async () => {
    const signalAllAcceptedCredentials = vi.fn().mockResolvedValue(undefined)
    const signalCurrentUserDetails = vi.fn().mockResolvedValue(undefined)
    stubBrowser({ signalAllAcceptedCredentials, signalCurrentUserDetails })

    await signalAcceptedPasskeys(user, [])

    expect(signalAllAcceptedCredentials).toHaveBeenCalledWith(expect.objectContaining({ allAcceptedCredentialIds: [] }))
    expect(signalCurrentUserDetails).not.toHaveBeenCalled()
  })

  it('reports an unknown credential and swallows browser errors', async () => {
    const signalUnknownCredential = vi.fn().mockRejectedValue(new Error('nope'))
    stubBrowser({ signalUnknownCredential })

    await expect(signalUnknownPasskey('cred-x')).resolves.toBeUndefined()
    expect(signalUnknownCredential).toHaveBeenCalledWith({ rpId: 'app.example.com', credentialId: 'cred-x' })
  })

  it('does not hang when the browser never settles the signal (Safari 26.0/26.1)', async () => {
    const never = () => new Promise<void>(() => {})
    const signalAllAcceptedCredentials = vi.fn(never)
    const signalCurrentUserDetails = vi.fn(never)
    stubBrowser({ signalAllAcceptedCredentials, signalCurrentUserDetails })

    await signalAcceptedPasskeys(user, ['cred-a'])
    await signalUnknownPasskey('cred-x')

    expect(signalAllAcceptedCredentials).toHaveBeenCalled()
    expect(signalCurrentUserDetails).toHaveBeenCalled()
  })

  it('does nothing where the Signal API is missing', async () => {
    stubBrowser({})
    expect(isPasskeySignalSupported()).toBe(false)
    await expect(signalAcceptedPasskeys(user, ['cred-a'])).resolves.toBeUndefined()
  })

  it('ignores a malformed user id', async () => {
    const signalAllAcceptedCredentials = vi.fn()
    stubBrowser({ signalAllAcceptedCredentials })
    await signalAcceptedPasskeys({ ...user, id: 'not-a-uuid' }, ['cred-a'])
    expect(signalAllAcceptedCredentials).not.toHaveBeenCalled()
  })
})
