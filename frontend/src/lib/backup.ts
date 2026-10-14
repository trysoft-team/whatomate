import type { BackupJob, MaintenanceStatus } from '@/services/api'

export const RESTORE_WORD = 'RESTORE'

/** Polls in a row without restore progress before the dialog gives up. */
export const LOST_AFTER_POLLS = 3

export type RestoreStage = 'running' | 'done' | 'partial' | 'failed' | 'lost'

/**
 * Decides the dialog stage from one /maintenance answer. A server that
 * restarted mid-restore no longer reports one; after LOST_AFTER_POLLS such
 * answers the restore counts as lost.
 */
export function nextRestoreStage(
  r: MaintenanceStatus['restore'] | null | undefined,
  missing: number,
): { stage: RestoreStage; missing: number } {
  if (!r) {
    const n = missing + 1
    return { stage: n >= LOST_AFTER_POLLS ? 'lost' : 'running', missing: n }
  }
  if (!r.finished) return { stage: 'running', missing: 0 }
  if (r.failed) return { stage: r.db_replaced ? 'partial' : 'failed', missing: 0 }
  return { stage: 'done', missing: 0 }
}

/** The error and safety backup of the last restore, if it failed. */
export function lastRestoreFailure(job: BackupJob | null | undefined): { error: string; safety: string } | null {
  if (!job || job.kind !== 'restore' || job.running || !job.error) return null
  return { error: job.error, safety: job.safety_name ?? '' }
}

export function canConfirmRestore(text: string): boolean {
  return text.trim() === RESTORE_WORD
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

export function jobPercent(job: { done: number; total: number }): number {
  if (!job.total) return 0
  return Math.min(100, Math.round((job.done / job.total) * 100))
}

export const UPLOAD_CHUNK = 4 * 1024 * 1024

export interface UploadApi {
  start(name: string, size: number): Promise<{ id: string }>
  put(id: string, offset: number, chunk: Blob): Promise<{ received: number }>
  finish(id: string): Promise<unknown>
  cancel(id: string): Promise<unknown>
}

export class UploadAbortedError extends Error {
  constructor() {
    super('upload cancelled')
    this.name = 'UploadAbortedError'
  }
}

/**
 * Uploads file in chunks; on failure the partial upload is cancelled. An
 * aborted signal is noticed between chunks and rejects with UploadAbortedError.
 */
export async function uploadInChunks(
  file: Blob & { name: string },
  api: UploadApi,
  onProgress: (sent: number, total: number) => void,
  chunkSize = UPLOAD_CHUNK,
  opts: { signal?: AbortSignal } = {},
): Promise<unknown> {
  const stopIfAborted = () => {
    if (opts.signal?.aborted) throw new UploadAbortedError()
  }
  stopIfAborted()
  const { id } = await api.start(file.name, file.size)
  let offset = 0
  try {
    while (offset < file.size) {
      stopIfAborted()
      const chunk = file.slice(offset, Math.min(offset + chunkSize, file.size))
      const { received } = await api.put(id, offset, chunk)
      offset = received
      onProgress(offset, file.size)
    }
    stopIfAborted()
    return await api.finish(id)
  } catch (e) {
    await api.cancel(id).catch(() => {})
    throw e
  }
}
