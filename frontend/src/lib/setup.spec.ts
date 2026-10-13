import { describe, expect, it } from 'vitest'
import {
  adminErrors, afterSetup, databaseReady, defaultDatabasePort, defaultSQLitePath,
  hasFailedCheck, passwordStrength, validPort, type AdminForm,
} from './setup'

const goodAdmin: AdminForm = {
  organization: 'Acme', fullName: 'Ana', email: 'ana@example.com', password: 'longenough', confirm: 'longenough',
}

describe('setup logic', () => {
  it('blocks only on failed checks', () => {
    expect(hasFailedCheck([{ id: 'a', status: 'pass', detail: '' }, { id: 'b', status: 'warn', detail: '' }])).toBe(false)
    expect(hasFailedCheck([{ id: 'a', status: 'fail', detail: '' }])).toBe(true)
  })

  it('knows default ports and valid ports', () => {
    expect(defaultDatabasePort('postgres')).toBe(5432)
    expect(defaultDatabasePort('mysql')).toBe(3306)
    expect(defaultDatabasePort('sqlite')).toBeUndefined()
    expect(validPort(4747)).toBe(true)
    expect(validPort(0)).toBe(false)
    expect(validPort(65536)).toBe(false)
    expect(validPort(80.5)).toBe(false)
  })

  it('validates the admin form', () => {
    expect(adminErrors(goodAdmin)).toEqual({})
    expect(adminErrors({ ...goodAdmin, organization: ' ' }).organization).toBe('setup.admin.errors.organization')
    expect(adminErrors({ ...goodAdmin, organization: 'x'.repeat(101) }).organization).toBe('setup.admin.errors.organizationLength')
    expect(adminErrors({ ...goodAdmin, fullName: '' }).fullName).toBe('setup.admin.errors.fullName')
    expect(adminErrors({ ...goodAdmin, email: 'nope' }).email).toBe('setup.admin.errors.email')
    expect(adminErrors({ ...goodAdmin, password: 'short', confirm: 'short' }).password).toBe('setup.admin.errors.passwordLength')
    expect(adminErrors({ ...goodAdmin, confirm: 'different!' }).confirm).toBe('setup.admin.errors.confirm')
  })

  it('rates password strength', () => {
    expect(passwordStrength('short')).toBe(0)
    expect(passwordStrength('alllowercase')).toBe(2)
    expect(passwordStrength('lowercase')).toBe(1)
    expect(passwordStrength('MixedCase1!')).toBe(3)
  })

  it('needs a passing test before the database step continues', () => {
    expect(databaseReady({ driver: 'sqlite', path: '/d/w.db' }, { ok: true, database_exists: false })).toBe(true)
    expect(databaseReady({ driver: 'sqlite', path: '/d/w.db' }, null)).toBe(false)
    const pg = { driver: 'postgres' as const, host: 'h', port: 5432, user: 'u', name: 'n' }
    expect(databaseReady(pg, { ok: true, database_exists: true })).toBe(true)
    expect(databaseReady(pg, { ok: true, database_exists: false })).toBe(false)
    expect(databaseReady(pg, { ok: false, database_exists: false })).toBe(false)
    expect(databaseReady({ ...pg, host: '' }, { ok: true, database_exists: true })).toBe(false)
  })

  it('builds the default SQLite path with the folder separator', () => {
    expect(defaultSQLitePath('/var/lib/whatomate')).toBe('/var/lib/whatomate/whatomate.db')
    expect(defaultSQLitePath('C:\\ProgramData\\whatomate')).toBe('C:\\ProgramData\\whatomate\\whatomate.db')
    expect(defaultSQLitePath('/x/')).toBe('/x/whatomate.db')
  })

  it('decides where to go after setup', () => {
    const here = 'http://localhost:4747'
    expect(afterSetup({ needed: true }, here)).toEqual({ kind: 'wait' })
    expect(afterSetup({ needed: false, ready: false, url: here }, here)).toEqual({ kind: 'wait' })
    expect(afterSetup({ needed: false, ready: true }, here)).toEqual({ kind: 'login' })
    expect(afterSetup({ needed: false, ready: true, url: here }, here)).toEqual({ kind: 'login' })
    expect(afterSetup({ needed: false, ready: true, url: 'http://localhost:9000' }, here))
      .toEqual({ kind: 'moved', url: 'http://localhost:9000' })
  })
})
