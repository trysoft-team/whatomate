import type { SetupCheck, SetupDatabase, SetupDatabaseResult, SetupDriver, SetupStatus } from '@/services/api'

export const SETUP_STEPS = ['welcome', 'checks', 'database', 'admin'] as const
export type SetupStep = (typeof SETUP_STEPS)[number]

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
