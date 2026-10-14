import { describe, expect, it, vi } from 'vitest'
import {
  adminErrors, afterSetup, captureSetupToken, clearSetupToken, databaseReady, defaultDatabasePort, defaultSQLitePath,
  hasFailedCheck, keyState, onSetupTokenRejected, parseSetupToken, passwordStrength, restorePhaseIndex, restoreReady,
  saveSetupToken, setupTokenRejected, stepsFor, storedSetupToken, tokenFromHash, validPort, type AdminForm,
} from './setup'
import type { SetupBackupSummary } from '@/services/api'

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
    const long = 'x'.repeat(73)
    expect(adminErrors({ ...goodAdmin, password: long, confirm: long }).password).toBe('setup.admin.errors.passwordTooLong')
    // 37 characters but 74 UTF-8 bytes: bcrypt reads at most 72 bytes.
    const multibyte = 'é'.repeat(37)
    expect(adminErrors({ ...goodAdmin, password: multibyte, confirm: multibyte }).password).toBe('setup.admin.errors.passwordTooLong')
    const max = 'x'.repeat(72)
    expect(adminErrors({ ...goodAdmin, password: max, confirm: max })).toEqual({})
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

function summary(fingerprint: string, matches: boolean): SetupBackupSummary {
  return {
    name: 'b.zip', size: 1, target_driver: 'sqlite', key_matches: matches, key_given: true,
    manifest: {
      format: 1, app_version: 'v1', kind: 'manual', created_at: '2026-01-01T00:00:00Z', source_driver: 'sqlite',
      key_fingerprint: fingerprint, include_files: false, tables: [],
    },
  }
}

describe('stepsFor', () => {
  it('swaps the last step', () => {
    expect(stepsFor('fresh')).toEqual(['welcome', 'checks', 'database', 'admin'])
    expect(stepsFor('restore')).toEqual(['welcome', 'checks', 'database', 'restore'])
  })
})

describe('keyState and restoreReady', () => {
  it('needs no key when the backup has none', () => {
    expect(keyState(summary('', true), '')).toBe('not-needed')
    expect(restoreReady(summary('', true), 'anything')).toBe(true)
  })
  it('allows a blank key with a warning', () => {
    expect(keyState(summary('abc', false), '  ')).toBe('blank')
    expect(restoreReady(summary('abc', false), '')).toBe(true)
  })
  it('blocks a key that does not match', () => {
    expect(keyState(summary('abc', false), 'wrong')).toBe('mismatch')
    expect(restoreReady(summary('abc', false), 'wrong')).toBe(false)
    expect(keyState(summary('abc', true), 'right')).toBe('match')
  })
  it('is not ready without a summary', () => {
    expect(restoreReady(null, '')).toBe(false)
  })
})

function memoryStore() {
  const m = new Map<string, string>()
  return {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => { m.set(k, v) },
    removeItem: (k: string) => { m.delete(k) },
  }
}

describe('setup token', () => {
  it('reads the key from the URL fragment', () => {
    expect(tokenFromHash('#token=abc_DEF-123')).toBe('abc_DEF-123')
    expect(tokenFromHash('#x=1&token=abc')).toBe('abc')
    expect(tokenFromHash('')).toBe('')
    expect(tokenFromHash('#abc')).toBe('')
    expect(tokenFromHash('#access_token=abc')).toBe('')
    expect(tokenFromHash('#token=<script>')).toBe('')
  })

  it('accepts a pasted link or bare key', () => {
    expect(parseSetupToken('  http://192.168.1.5:4747/#token=abc123  ')).toBe('abc123')
    expect(parseSetupToken('abc123')).toBe('abc123')
    expect(parseSetupToken('token=abc123')).toBe('abc123')
    expect(parseSetupToken('http://192.168.1.5:4747/')).toBe('')
    expect(parseSetupToken('http://192.168.1.5:4747/#')).toBe('')
    expect(parseSetupToken('')).toBe('')
  })

  it('moves the key from the address into storage', () => {
    const store = memoryStore()
    expect(captureSetupToken({ pathname: '/', search: '', hash: '#token=abc' }, store)).toBe('/')
    expect(storedSetupToken(store)).toBe('abc')
    expect(captureSetupToken({ pathname: '/setup', search: '?a=1', hash: '#token=xyz' }, store)).toBe('/setup?a=1')
    expect(storedSetupToken(store)).toBe('xyz')
  })

  it('leaves the address and storage alone without a key', () => {
    const store = memoryStore()
    saveSetupToken('kept', store)
    expect(captureSetupToken({ pathname: '/login', search: '', hash: '#section' }, store)).toBeNull()
    expect(storedSetupToken(store)).toBe('kept')
    clearSetupToken(store)
    expect(storedSetupToken(store)).toBe('')
  })

  it('clears the key and notifies when the server refuses it', () => {
    vi.stubGlobal('sessionStorage', memoryStore())
    try {
      saveSetupToken('stale')
      const seen = vi.fn()
      onSetupTokenRejected(seen)
      setupTokenRejected()
      expect(seen).toHaveBeenCalledOnce()
      expect(storedSetupToken()).toBe('')
      onSetupTokenRejected(null)
      setupTokenRejected()
      expect(seen).toHaveBeenCalledOnce()
    } finally {
      vi.unstubAllGlobals()
    }
  })
})

describe('restorePhaseIndex', () => {
  it('orders the phases', () => {
    expect(restorePhaseIndex('prepare')).toBe(0)
    expect(restorePhaseIndex('load')).toBe(2)
    expect(restorePhaseIndex('config')).toBe(5)
    expect(restorePhaseIndex('')).toBe(0)
  })
})
