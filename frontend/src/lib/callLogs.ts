import { Phone, PhoneMissed, PhoneOff, PhoneOutgoing, Clock, User, Headphones, Monitor } from 'lucide-vue-next'
import type { CallLog } from '@/services/api'

export type BadgeVariant = 'default' | 'secondary' | 'destructive' | 'outline'

export function formatCallDuration(seconds?: number): string {
  if (!seconds) return '-'
  const m = Math.floor(seconds / 60)
  const s = seconds % 60
  return m > 0 ? `${m}m ${s}s` : `${s}s`
}

export function callStatusVariant(status: string): BadgeVariant {
  switch (status) {
    case 'completed':
    case 'answered':
    case 'accepted':
      return 'default'
    case 'missed':
      return 'outline'
    case 'rejected':
    case 'failed':
      return 'destructive'
    default:
      return 'secondary'
  }
}

export function callStatusIcon(status: string) {
  switch (status) {
    case 'completed':
    case 'answered':
    case 'accepted':
      return Phone
    case 'missed':
      return PhoneMissed
    case 'ringing':
    case 'initiating':
      return Clock
    case 'transferring':
      return PhoneOutgoing
    default:
      return PhoneOff
  }
}

export function disconnectedByIcon(value: string) {
  switch (value) {
    case 'client': return User
    case 'agent': return Headphones
    case 'system': return Monitor
    default: return null
  }
}

export function disconnectedByVariant(value: string): 'default' | 'secondary' | 'outline' {
  switch (value) {
    case 'agent': return 'secondary'
    case 'system': return 'default'
    default: return 'outline'
  }
}

export function disconnectedByKey(value: string): string {
  return `calling.disconnectedBy${value.charAt(0).toUpperCase()}${value.slice(1)}`
}

export function hasAICallArtifacts(log: CallLog | null): boolean {
  const arts = log?.ai_call_artifacts
  if (!arts) return false
  return !!(arts.transcript?.length || arts.recordings?.user || arts.recordings?.assistant || arts.recordings?.stereo)
}

export function resolveMediaURL(url: string): string {
  if (url.startsWith('http')) return url
  const basePath = ((window as any).__BASE_PATH__ ?? '').replace(/\/$/, '')
  return `${basePath}${url}`
}
