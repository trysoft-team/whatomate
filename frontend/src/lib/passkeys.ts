// Browser helpers for WebAuthn passkeys. The server sends options as JSON with
// base64url-encoded binary fields ({ publicKey: {...} }); these convert them to
// the ArrayBuffers navigator.credentials expects, and serialize the result back.

function b64urlToBuffer(value: string): ArrayBuffer {
  const base64 = value.replace(/-/g, '+').replace(/_/g, '/')
  const padded = base64 + '='.repeat((4 - (base64.length % 4)) % 4)
  const binary = atob(padded)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
  return bytes.buffer
}

function bufferToB64url(buffer: ArrayBuffer | null | undefined): string | undefined {
  if (!buffer) return undefined
  const bytes = new Uint8Array(buffer)
  let binary = ''
  for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i])
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

function toDescriptors(list: any[] | undefined): PublicKeyCredentialDescriptor[] | undefined {
  return list?.map(c => ({ ...c, id: b64urlToBuffer(c.id) }))
}

export function isPasskeySupported(): boolean {
  return typeof window !== 'undefined' && !!window.PublicKeyCredential && !!navigator.credentials
}

/** True when the user dismissed or timed out the browser's passkey prompt. */
export function isPasskeyCancelled(err: unknown): boolean {
  const name = (err as { name?: string })?.name
  return name === 'NotAllowedError' || name === 'AbortError'
}

export async function createPasskey(options: { publicKey: any }): Promise<Record<string, unknown>> {
  const pk = options.publicKey
  const credential = (await navigator.credentials.create({
    publicKey: {
      ...pk,
      challenge: b64urlToBuffer(pk.challenge),
      user: { ...pk.user, id: b64urlToBuffer(pk.user.id) },
      excludeCredentials: toDescriptors(pk.excludeCredentials),
    },
  })) as PublicKeyCredential | null
  if (!credential) throw new Error('No passkey was created')
  const response = credential.response as AuthenticatorAttestationResponse
  return {
    id: credential.id,
    rawId: bufferToB64url(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment ?? undefined,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      clientDataJSON: bufferToB64url(response.clientDataJSON),
      attestationObject: bufferToB64url(response.attestationObject),
      transports: typeof response.getTransports === 'function' ? response.getTransports() : undefined,
    },
  }
}

/**
 * True when the browser can offer passkeys in the username field's autofill
 * (WebAuthn conditional mediation), like the Apple and Google sign-in pages.
 */
export async function isPasskeyAutofillAvailable(): Promise<boolean> {
  if (!isPasskeySupported()) return false
  const check = (window.PublicKeyCredential as any).isConditionalMediationAvailable
  if (typeof check !== 'function') return false
  try {
    return await check.call(window.PublicKeyCredential)
  } catch {
    return false
  }
}

export interface GetPasskeyOptions {
  /** Offer passkeys in autofill instead of opening a modal prompt. */
  conditional?: boolean
  signal?: AbortSignal
}

export async function getPasskey(
  options: { publicKey: any },
  { conditional = false, signal }: GetPasskeyOptions = {},
): Promise<Record<string, unknown>> {
  const pk = options.publicKey
  const credential = (await navigator.credentials.get({
    publicKey: {
      ...pk,
      challenge: b64urlToBuffer(pk.challenge),
      allowCredentials: toDescriptors(pk.allowCredentials),
    },
    ...(conditional ? { mediation: 'conditional' as CredentialMediationRequirement } : {}),
    signal,
  })) as PublicKeyCredential | null
  if (!credential) throw new Error('No passkey was selected')
  const response = credential.response as AuthenticatorAssertionResponse
  return {
    id: credential.id,
    rawId: bufferToB64url(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment ?? undefined,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      clientDataJSON: bufferToB64url(response.clientDataJSON),
      authenticatorData: bufferToB64url(response.authenticatorData),
      signature: bufferToB64url(response.signature),
      userHandle: bufferToB64url(response.userHandle),
    },
  }
}

// WebAuthn Signal API: a site can't delete a passkey from the user's password
// manager, but it can tell the manager which passkeys it still accepts so
// removed ones get hidden or deleted. Unsupported browsers ignore these calls.
// The RP ID must match the one the server registers passkeys under (the page
// hostname), and the user handle is the 16 bytes of the user's UUID.

export const PASSKEY_UNKNOWN_REASON = 'passkey_unknown'

function currentRpId(): string {
  return window.location.hostname
}

function uuidToUserHandle(uuid: string): string | null {
  const hex = uuid.replace(/-/g, '')
  if (!/^[0-9a-f]{32}$/i.test(hex)) return null
  const bytes = new Uint8Array(16)
  for (let i = 0; i < 16; i++) bytes[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16)
  return bufferToB64url(bytes.buffer) ?? null
}

function signalApi(name: 'signalUnknownCredential' | 'signalAllAcceptedCredentials' | 'signalCurrentUserDetails') {
  if (!isPasskeySupported()) return null
  const fn = (window.PublicKeyCredential as any)[name]
  return typeof fn === 'function' ? (fn.bind(window.PublicKeyCredential) as (opts: object) => Promise<void>) : null
}

export function isPasskeySignalSupported(): boolean {
  return signalApi('signalAllAcceptedCredentials') !== null
}

/** Tell the password manager a passkey no longer exists on the server. */
export async function signalUnknownPasskey(credentialId: string): Promise<void> {
  const signal = signalApi('signalUnknownCredential')
  if (!signal || !credentialId) return
  signal({ rpId: currentRpId(), credentialId }).catch(() => {})
}

/**
 * Tell the password manager the full set of this user's passkeys the server
 * still accepts for this site (others are hidden), and refresh the name shown
 * on them.
 */
export async function signalAcceptedPasskeys(
  user: { id: string; email: string; full_name?: string },
  credentialIds: string[],
): Promise<void> {
  const userId = uuidToUserHandle(user.id)
  if (!userId) return
  const rpId = currentRpId()
  // Fired without awaiting each other: Safari 26.0/26.1 applies these signals
  // but never settles the promises (WebKit bug 298951).
  const pending: Promise<void>[] = []
  const accepted = signalApi('signalAllAcceptedCredentials')
  if (accepted) {
    pending.push(accepted({ rpId, userId, allAcceptedCredentialIds: credentialIds }))
  }
  const details = signalApi('signalCurrentUserDetails')
  if (details && credentialIds.length > 0) {
    pending.push(details({ rpId, userId, name: user.email, displayName: user.full_name || user.email }))
  }
  pending.forEach(p => p.catch(() => {}))
}

/** A default passkey name from the browser, e.g. "Chrome on macOS". */
export function defaultPasskeyName(): string {
  const ua = navigator.userAgent
  const browser = /Edg\//.test(ua) ? 'Edge'
    : /Firefox\//.test(ua) ? 'Firefox'
    : /Chrome\//.test(ua) ? 'Chrome'
    : /Safari\//.test(ua) ? 'Safari'
    : 'Browser'
  const os = /iPhone|iPad/.test(ua) ? 'iOS'
    : /Android/.test(ua) ? 'Android'
    : /Mac OS X/.test(ua) ? 'macOS'
    : /Windows/.test(ua) ? 'Windows'
    : /Linux/.test(ua) ? 'Linux'
    : ''
  return os ? `${browser} on ${os}` : browser
}
