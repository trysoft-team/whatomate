// Unmasks a call recording. The mask travels in a response header, so saving
// the body from the network panel does not produce a playable file.
export function unmaskPlayback(data: Uint8Array, maskBase64: string): Uint8Array {
  const mask = decodeMask(maskBase64)
  const out = new Uint8Array(data.length)
  for (let i = 0; i < data.length; i++) {
    out[i] = data[i] ^ mask[i % mask.length]
  }
  return out
}

function decodeMask(maskBase64: string): Uint8Array {
  const cleaned = maskBase64.trim()
  if (!cleaned) throw new Error('missing playback mask')
  const binary = atob(cleaned)
  const mask = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) mask[i] = binary.charCodeAt(i)
  if (mask.length === 0) throw new Error('missing playback mask')
  return mask
}

export function formatPlaybackTime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) seconds = 0
  const total = Math.floor(seconds)
  const m = Math.floor(total / 60)
  const s = total % 60
  return `${m}:${s.toString().padStart(2, '0')}`
}
