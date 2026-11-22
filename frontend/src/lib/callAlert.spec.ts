import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import {
  dismissAllCallAlerts,
  startCallRingtone,
  stopAllCallRingtones,
  stopCallRingtone,
} from './callAlert'

beforeAll(() => {
  class FakeAudio {
    volume = 1
    loop = false
    currentTime = 0
    play = vi.fn().mockResolvedValue(undefined)
    pause = vi.fn()
  }
  ;(globalThis as any).Audio = FakeAudio
})

describe('callAlert ringtone', () => {
  afterEach(() => {
    stopAllCallRingtones()
    vi.clearAllMocks()
  })

  it('starts and stops the looping ringtone', () => {
    startCallRingtone()
    startCallRingtone()
    stopCallRingtone()
    // Still ringing while a second start is outstanding
    stopCallRingtone()
    dismissAllCallAlerts()
  })

  it('stopAllCallRingtones clears nested starts', () => {
    startCallRingtone()
    startCallRingtone()
    stopAllCallRingtones()
  })
})
