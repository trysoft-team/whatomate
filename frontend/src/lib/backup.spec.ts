import { describe, expect, it, vi } from 'vitest'
import { canConfirmRestore, formatBytes, jobPercent, uploadInChunks, type UploadApi } from './backup'

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
})
