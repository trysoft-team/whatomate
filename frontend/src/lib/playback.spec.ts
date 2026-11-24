import { describe, expect, it } from 'vitest'
import { formatPlaybackTime, unmaskPlayback } from './playback'

describe('unmaskPlayback', () => {
  it('restores bytes masked the same way as the server', () => {
    const raw = new Uint8Array([79, 103, 103, 83]) // OggS
    const masked = new Uint8Array([78, 101, 100, 87])
    expect(unmaskPlayback(masked, 'AQIDBA==')).toEqual(raw)
  })

  it('rejects a missing mask', () => {
    expect(() => unmaskPlayback(new Uint8Array([1]), '')).toThrow(/mask/)
  })
})

describe('formatPlaybackTime', () => {
  it('formats minutes and seconds', () => {
    expect(formatPlaybackTime(0)).toBe('0:00')
    expect(formatPlaybackTime(4.2)).toBe('0:04')
    expect(formatPlaybackTime(65)).toBe('1:05')
  })
})
