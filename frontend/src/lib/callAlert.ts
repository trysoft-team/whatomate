import { toast } from 'vue-sonner'
import { i18n } from '@/i18n'

/** Looping ringtone for incoming call / transfer alerts. */
let ringtone: HTMLAudioElement | null = null
let ringCount = 0

/** Active toast ids keyed by transfer/call id so we can dismiss them. */
const activeToasts = new Map<string, string | number>()

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

export function dismissCallAlert(id: string): void {
  const toastId = activeToasts.get(id)
  if (toastId != null) {
    toast.dismiss(toastId)
    activeToasts.delete(id)
  }
  stopCallRingtone()
}

export function dismissAllCallAlerts(): void {
  for (const toastId of activeToasts.values()) {
    toast.dismiss(toastId)
  }
  activeToasts.clear()
  stopAllCallRingtones()
}

function formatCallDetails(payload: {
  contact_name?: string
  caller_phone?: string
  contact?: { profile_name?: string; phone_number?: string }
  whatsapp_account?: string
  team_name?: string
  team?: { name?: string }
}): string {
  const t = i18n.global.t
  const name =
    payload.contact_name ||
    payload.contact?.profile_name ||
    payload.caller_phone ||
    payload.contact?.phone_number ||
    t('calling.unknownCaller')
  const phone = payload.caller_phone || payload.contact?.phone_number || ''
  const account = payload.whatsapp_account || ''
  const team = payload.team_name || payload.team?.name || ''

  const parts: string[] = [name]
  if (phone && phone !== name) parts.push(phone)
  if (account) parts.push(account)
  if (team) parts.push(`${t('calling.team')}: ${team}`)
  return parts.join(' · ')
}

/**
 * Persistent ringing toast for a waiting transfer. Answer accepts via
 * onAnswer; Dismiss only silences this client (call keeps ringing for others).
 */
export function showIncomingTransferAlert(
  payload: {
    id: string
    contact_name?: string
    caller_phone?: string
    contact?: { profile_name?: string; phone_number?: string }
    whatsapp_account?: string
    team_name?: string
    team?: { name?: string }
  },
  onAnswer: (transferId: string) => void | Promise<void>,
): void {
  if (!payload.id || activeToasts.has(payload.id)) return

  const t = i18n.global.t
  startCallRingtone()

  const toastId = toast(t('calling.incomingCallAlert'), {
    description: formatCallDetails(payload),
    duration: Infinity,
    closeButton: true,
    important: true,
    action: {
      label: t('calling.answer'),
      onClick: () => {
        dismissCallAlert(payload.id)
        void onAnswer(payload.id)
      },
    },
    cancel: {
      label: t('calling.dismiss'),
      onClick: () => {
        dismissCallAlert(payload.id)
      },
    },
    onDismiss: () => {
      if (activeToasts.get(payload.id) === toastId) {
        activeToasts.delete(payload.id)
        stopCallRingtone()
      }
    },
  })

  activeToasts.set(payload.id, toastId)
}

/**
 * Early ringing notice (before a transfer is offered). No Answer — agents
 * pick up once the call reaches the queue / sticky agent transfer.
 */
export function showIncomingCallRingingAlert(payload: {
  call_id?: string
  call_log_id?: string
  contact_name?: string
  caller_phone?: string
  whatsapp_account?: string
}): void {
  const id = payload.call_id || payload.call_log_id
  if (!id || activeToasts.has(id)) return

  const t = i18n.global.t
  startCallRingtone()

  const toastId = toast(t('calling.incomingCallRinging'), {
    description: formatCallDetails(payload),
    duration: Infinity,
    closeButton: true,
    important: true,
    cancel: {
      label: t('calling.dismiss'),
      onClick: () => {
        dismissCallAlert(id)
      },
    },
    onDismiss: () => {
      if (activeToasts.get(id) === toastId) {
        activeToasts.delete(id)
        stopCallRingtone()
      }
    },
  })

  activeToasts.set(id, toastId)
}
