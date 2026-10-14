import type {
  SetupBackupSummary, SetupCheck, SetupDatabase, SetupDatabaseResult, SetupDriver, SetupStatus,
} from '@/services/api'

export const SETUP_STEPS = ['welcome', 'checks', 'database', 'admin'] as const
export const RESTORE_STEPS = ['welcome', 'checks', 'database', 'restore'] as const
export type SetupStep = (typeof SETUP_STEPS)[number] | (typeof RESTORE_STEPS)[number]
export type SetupMode = 'fresh' | 'restore'

export function stepsFor(mode: SetupMode): readonly SetupStep[] {
  return mode === 'restore' ? RESTORE_STEPS : SETUP_STEPS
}

export function hasFailedCheck(checks: SetupCheck[]): boolean {
  return checks.some(c => c.status === 'fail')
}

export function defaultDatabasePort(driver: SetupDriver): number | undefined {
  if (driver === 'postgres') return 5432
  if (driver === 'mysql') return 3306
  return undefined
}

export function validPort(port: number): boolean {
  return Number.isInteger(port) && port >= 1 && port <= 65535
}

export interface AdminForm {
  organization: string
  fullName: string
  email: string
  password: string
  confirm: string
}

export type AdminErrors = Partial<Record<keyof AdminForm, string>>

const EMAIL = /^[^\s@]+@[^\s@]+$/

/** adminErrors returns an i18n key per invalid field. */
export function adminErrors(f: AdminForm): AdminErrors {
  const e: AdminErrors = {}
  const org = f.organization.trim()
  if (!org) e.organization = 'setup.admin.errors.organization'
  else if (org.length > 100) e.organization = 'setup.admin.errors.organizationLength'
  if (!f.fullName.trim()) e.fullName = 'setup.admin.errors.fullName'
  if (!EMAIL.test(f.email.trim())) e.email = 'setup.admin.errors.email'
  if (f.password.length < 8) e.password = 'setup.admin.errors.passwordLength'
  // bcrypt reads at most 72 bytes, so the server refuses longer passwords.
  else if (new TextEncoder().encode(f.password).length > 72) e.password = 'setup.admin.errors.passwordTooLong'
  if (f.confirm !== f.password) e.confirm = 'setup.admin.errors.confirm'
  return e
}

/** passwordStrength: 0 too short, 1 weak, 2 fair, 3 strong. */
export function passwordStrength(pw: string): 0 | 1 | 2 | 3 {
  if (pw.length < 8) return 0
  let score = 1
  if (pw.length >= 12) score++
  if (/[a-z]/.test(pw) && /[A-Z]/.test(pw)) score++
  if (/\d/.test(pw) && /[^A-Za-z0-9]/.test(pw)) score++
  return Math.min(score, 3) as 1 | 2 | 3
}

/** databaseReady: the fields are filled and the last test passed for them. */
export function databaseReady(db: SetupDatabase, result: SetupDatabaseResult | null): boolean {
  if (!result?.ok) return false
  if (db.driver === 'sqlite') return !!db.path
  return !!db.host && !!db.user && !!db.name && validPort(db.port ?? 0) && result.database_exists
}

export function defaultSQLitePath(dataDir: string): string {
  const sep = dataDir.includes('\\') ? '\\' : '/'
  return dataDir.replace(/[\\/]+$/, '') + sep + 'whatomate.db'
}

export type AfterSetup = { kind: 'wait' } | { kind: 'login' } | { kind: 'moved'; url: string }

/** afterSetup reads the status poll after Finish. */
export function afterSetup(status: SetupStatus, currentOrigin: string): AfterSetup {
  if (status.needed || !status.ready) return { kind: 'wait' }
  if (status.url) {
    const origin = new URL(status.url).origin
    if (origin !== currentOrigin) return { kind: 'moved', url: origin }
  }
  return { kind: 'login' }
}

export type KeyState = 'not-needed' | 'blank' | 'match' | 'mismatch'

/** keyState: summary must come from inspecting with this key. */
export function keyState(summary: SetupBackupSummary, key: string): KeyState {
  if (!summary.manifest.key_fingerprint) return 'not-needed'
  if (!key.trim()) return 'blank'
  return summary.key_matches ? 'match' : 'mismatch'
}

export function restoreReady(summary: SetupBackupSummary | null, key: string): boolean {
  return !!summary && keyState(summary, key) !== 'mismatch'
}

const SETUP_TOKEN_KEY = 'whatomate_setup_token'
const TOKEN_CHARS = /^[A-Za-z0-9_-]+$/

type TokenStore = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

/** tokenFromHash reads the setup key from a URL fragment such as "#token=…". */
export function tokenFromHash(hash: string): string {
  const m = /(?:^|[#&])token=([^&]*)/.exec(hash)
  return m && TOKEN_CHARS.test(m[1]) ? m[1] : ''
}

/** parseSetupToken accepts the full link from the server log or the bare key. */
export function parseSetupToken(input: string): string {
  const s = input.trim()
  if (s.includes('#')) return tokenFromHash(s.slice(s.indexOf('#')))
  return TOKEN_CHARS.test(s) ? s : tokenFromHash(s)
}

/**
 * captureSetupToken moves the key from the address bar into store, so it
 * isn't left in history or bookmarks. It returns the address to show instead,
 * or null when the hash holds no key.
 */
export function captureSetupToken(loc: { pathname: string; search: string; hash: string }, store: TokenStore): string | null {
  const token = tokenFromHash(loc.hash)
  if (!token) return null
  store.setItem(SETUP_TOKEN_KEY, token)
  return loc.pathname + loc.search
}

export function storedSetupToken(store: TokenStore = sessionStorage): string {
  return store.getItem(SETUP_TOKEN_KEY) ?? ''
}

export function saveSetupToken(token: string, store: TokenStore = sessionStorage): void {
  store.setItem(SETUP_TOKEN_KEY, token)
}

export function clearSetupToken(store: TokenStore = sessionStorage): void {
  store.removeItem(SETUP_TOKEN_KEY)
}

let onTokenRejected: (() => void) | null = null

/** onSetupTokenRejected registers what to do when the setup server refuses the key. */
export function onSetupTokenRejected(fn: (() => void) | null): void {
  onTokenRejected = fn
}

export function setupTokenRejected(): void {
  clearSetupToken()
  onTokenRejected?.()
}

export const RESTORE_PHASES = ['prepare', 'verify', 'load', 'links', 'swap', 'config'] as const

export function restorePhaseIndex(phase: string): number {
  return Math.max(0, RESTORE_PHASES.indexOf(phase as (typeof RESTORE_PHASES)[number]))
}
