import { describe, expect, it, vi } from 'vitest'
import { canConfirmRestore, formatBytes, jobPercent, lastRestoreFailure, nextRestoreStage, uploadInChunks, UploadAbortedError, type UploadApi } from './backup'

describe('nextRestoreStage', () => {
  const st = (over: Partial<{ finished: boolean; failed: boolean; db_replaced: boolean }> = {}) => ({
    running: !over.finished, phase: 'load', done: 0, total: 0, finished: false, failed: false, db_replaced: false, ...over,
  })

  it('keeps running while the restore runs', () => {
    expect(nextRestoreStage(st(), 2)).toEqual({ stage: 'running', missing: 0 })
  })

  it('reports the outcome when finished', () => {
    expect(nextRestoreStage(st({ finished: true }), 0).stage).toBe('done')
    expect(nextRestoreStage(st({ finished: true, failed: true }), 0).stage).toBe('failed')
    expect(nextRestoreStage(st({ finished: true, failed: true, db_replaced: true }), 0).stage).toBe('partial')
  })

  it('gives up after three polls without a restore', () => {
    expect(nextRestoreStage(undefined, 0)).toEqual({ stage: 'running', missing: 1 })
    expect(nextRestoreStage(null, 1)).toEqual({ stage: 'running', missing: 2 })
    expect(nextRestoreStage(undefined, 2)).toEqual({ stage: 'lost', missing: 3 })
  })
})

describe('lastRestoreFailure', () => {
  const job = { kind: 'restore' as const, running: false, phase: 'failed', done: 0, total: 0, started_at: '' }

  it('returns the error and safety backup of a failed restore', () => {
    expect(lastRestoreFailure({ ...job, error: 'boom', safety_name: 's.zip' })).toEqual({ error: 'boom', safety: 's.zip' })
    expect(lastRestoreFailure({ ...job, error: 'boom' })).toEqual({ error: 'boom', safety: '' })
  })

  it('ignores backups, running jobs and successes', () => {
    expect(lastRestoreFailure(null)).toBeNull()
    expect(lastRestoreFailure({ ...job })).toBeNull()
    expect(lastRestoreFailure({ ...job, error: 'x', running: true })).toBeNull()
    expect(lastRestoreFailure({ ...job, kind: 'backup', error: 'x' })).toBeNull()
  })
})

describe('canConfirmRestore', () => {
  it('needs exactly RESTORE', () => {
    expect(canConfirmRestore('RESTORE')).toBe(true)
    expect(canConfirmRestore('  RESTORE ')).toBe(true)
    expect(canConfirmRestore('restore')).toBe(false)
    expect(canConfirmRestore('')).toBe(false)
  })
})

describe('formatBytes', () => {
  it('uses binary units', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MB')
    expect(formatBytes(3 * 1024 ** 3)).toBe('3.0 GB')
  })
})

describe('jobPercent', () => {
  it('handles unknown totals', () => {
    expect(jobPercent({ done: 5, total: 0 })).toBe(0)
    expect(jobPercent({ done: 50, total: 200 })).toBe(25)
    expect(jobPercent({ done: 300, total: 200 })).toBe(100)
  })
})

describe('uploadInChunks', () => {
  function fakeApi(): UploadApi & { chunks: number[] } {
    const chunks: number[] = []
    return {
      chunks,
      start: vi.fn(async () => ({ id: 'u1' })),
      put: vi.fn(async (_id: string, offset: number, chunk: Blob) => {
        chunks.push(chunk.size)
        return { received: offset + chunk.size }
      }),
      finish: vi.fn(async () => ({ name: 'b.zip' })),
      cancel: vi.fn(async () => ({})),
    }
  }

  it('sends the file in order and finishes', async () => {
    const api = fakeApi()
    const file = Object.assign(new Blob([new Uint8Array(10)]), { name: 'b.zip' })
    const seen: number[] = []
    const res = await uploadInChunks(file, api, (sent) => seen.push(sent), 4)
    expect(api.chunks).toEqual([4, 4, 2])
    expect(seen).toEqual([4, 8, 10])
    expect(res).toEqual({ name: 'b.zip' })
    expect(api.cancel).not.toHaveBeenCalled()
  })

  it('cancels when a chunk fails', async () => {
    const api = fakeApi()
    api.put = vi.fn(async () => { throw new Error('network') })
    const file = Object.assign(new Blob([new Uint8Array(3)]), { name: 'b.zip' })
    await expect(uploadInChunks(file, api, () => {}, 4)).rejects.toThrow('network')
    expect(api.cancel).toHaveBeenCalledWith('u1')
  })

  it('stops between chunks and cancels when aborted', async () => {
    const api = fakeApi()
    const ctrl = new AbortController()
    const file = Object.assign(new Blob([new Uint8Array(10)]), { name: 'b.zip' })
    const run = uploadInChunks(file, api, (sent) => { if (sent === 4) ctrl.abort() }, 4, { signal: ctrl.signal })
    await expect(run).rejects.toBeInstanceOf(UploadAbortedError)
    expect(api.chunks).toEqual([4])
    expect(api.cancel).toHaveBeenCalledWith('u1')
    expect(api.finish).not.toHaveBeenCalled()
  })

  it('does not start when already aborted', async () => {
    const api = fakeApi()
    const ctrl = new AbortController()
    ctrl.abort()
    const file = Object.assign(new Blob([new Uint8Array(3)]), { name: 'b.zip' })
    await expect(uploadInChunks(file, api, () => {}, 4, { signal: ctrl.signal })).rejects.toBeInstanceOf(UploadAbortedError)
    expect(api.start).not.toHaveBeenCalled()
  })
})
