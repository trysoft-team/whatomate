/** Looping ringtone for incoming call / transfer alerts. */
let ringtone: HTMLAudioElement | null = null
let ringCount = 0

export function startCallRingtone(): void {
  ringCount++
  if (!ringtone) {
    ringtone = new Audio('/notification.mp3')
    ringtone.volume = 0.7
    ringtone.loop = true
  }
  ringtone.currentTime = 0
  ringtone.play().catch(() => {
    // Autoplay may be blocked until the user has interacted with the page.
  })
}

export function stopCallRingtone(): void {
  ringCount = Math.max(0, ringCount - 1)
  if (ringCount === 0 && ringtone) {
    ringtone.pause()
    ringtone.currentTime = 0
  }
}

export function stopAllCallRingtones(): void {
  ringCount = 0
  if (ringtone) {
    ringtone.pause()
    ringtone.currentTime = 0
  }
}

/** @deprecated Alias — toasts removed; stops ringtone only. */
export function dismissCallAlert(_id?: string): void {
  stopCallRingtone()
}

/** @deprecated Alias — toasts removed; stops all ringtones. */
export function dismissAllCallAlerts(): void {
  stopAllCallRingtones()
}
