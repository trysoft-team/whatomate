export const RESTORE_WORD = 'RESTORE'

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

/** Uploads file in chunks; on failure the partial upload is cancelled. */
export async function uploadInChunks(
  file: Blob & { name: string },
  api: UploadApi,
  onProgress: (sent: number, total: number) => void,
  chunkSize = UPLOAD_CHUNK,
): Promise<unknown> {
  const { id } = await api.start(file.name, file.size)
  let offset = 0
  try {
    while (offset < file.size) {
      const chunk = file.slice(offset, Math.min(offset + chunkSize, file.size))
      const { received } = await api.put(id, offset, chunk)
      offset = received
      onProgress(offset, file.size)
    }
    return await api.finish(id)
  } catch (e) {
    await api.cancel(id).catch(() => {})
    throw e
  }
}
