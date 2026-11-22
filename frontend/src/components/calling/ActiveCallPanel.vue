<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useCallingStore } from '@/stores/calling'
import { useAuthStore } from '@/stores/auth'
import { Button } from '@/components/ui/button'
import { Phone, PhoneOff, PhoneIncoming, Mic, MicOff, ArrowRightLeft, Pause, Play } from 'lucide-vue-next'
import CallTransferPicker from '@/components/calling/CallTransferPicker.vue'
import { startCallRingtone, stopAllCallRingtones } from '@/lib/callAlert'
import { toast } from 'vue-sonner'
import type { CallTransfer } from '@/services/api'

const { t } = useI18n()
const store = useCallingStore()
const authStore = useAuthStore()
const acceptingId = ref<string | null>(null)
const hangingUpId = ref<string | null>(null)
const showTransferPicker = ref(false)

const formattedDuration = computed(() => {
  const m = Math.floor(store.callDuration / 60)
  const s = store.callDuration % 60
  return `${m.toString().padStart(2, '0')}:${s.toString().padStart(2, '0')}`
})

const firstWaiting = computed(() => store.waitingTransfers[0] ?? null)

const currentTransfer = computed<CallTransfer | null>(() => {
  if (store.activeTransfer) return store.activeTransfer
  return firstWaiting.value
})

function transferName(transfer: CallTransfer | null): string {
  if (!transfer) return t('calling.unknownCaller')
  return (
    transfer.contact_name ||
    transfer.contact?.profile_name ||
    transfer.caller_phone ||
    transfer.contact?.phone_number ||
    t('calling.unknownCaller')
  )
}

function transferPhone(transfer: CallTransfer | null): string {
  if (!transfer) return ''
  return transfer.caller_phone || transfer.contact?.phone_number || ''
}

function transferTeam(transfer: CallTransfer | null): string {
  if (!transfer) return ''
  return transfer.team_name || transfer.team?.name || ''
}

const displayName = computed(() => {
  if (store.isOutgoingCall) {
    return store.outgoingContactName || store.outgoingContactPhone || t('calling.unknownCaller')
  }
  return transferName(currentTransfer.value)
})

const detailLines = computed(() => {
  const lines: string[] = []
  if (store.isOutgoingCall) {
    if (store.outgoingContactPhone && store.outgoingContactPhone !== displayName.value) {
      lines.push(store.outgoingContactPhone)
    }
    return lines
  }
  const transfer = currentTransfer.value
  if (!transfer) return lines
  const phone = transferPhone(transfer)
  if (phone && phone !== displayName.value) lines.push(phone)
  if (transfer.whatsapp_account) lines.push(transfer.whatsapp_account)
  const team = transferTeam(transfer)
  if (team) lines.push(`${t('calling.team')}: ${team}`)
  return lines
})

const statusText = computed(() => {
  if (store.isOutgoingCall) {
    switch (store.outgoingCallStatus) {
      case 'initiating': return `${t('outgoingCalls.initiating')}...`
      case 'ringing': return `${t('outgoingCalls.ringing')}...`
      case 'answered': return t('outgoingCalls.answered')
      default: return ''
    }
  }
  if (store.isOnCall && store.isOnHold) {
    return t('calling.onHold')
  }
  if (store.isOnCall) {
    return t('callTransfers.callConnected')
  }
  if (firstWaiting.value) {
    return t('calling.incomingCallAlert')
  }
  return ''
})

const showPanel = computed(() => store.isOnCall || store.waitingTransfers.length > 0)
const isRinging = computed(() => !store.isOnCall && !!firstWaiting.value)

// Ringtone while a waiting transfer is offered and the agent is Available.
let panelRinging = false
watch(
  [firstWaiting, () => authStore.isAvailable, () => store.isOnCall],
  ([waiting, available, onCall]) => {
    const shouldRing = !!(waiting && available && !onCall)
    if (shouldRing && !panelRinging) {
      panelRinging = true
      startCallRingtone()
    } else if (!shouldRing && panelRinging) {
      panelRinging = false
      stopAllCallRingtones()
    }
  },
  { immediate: true },
)

async function handleAccept(id: string) {
  acceptingId.value = id
  stopAllCallRingtones()
  try {
    await store.acceptTransfer(id)
    toast.success(t('callTransfers.callConnected'))
  } catch (err: any) {
    const serverMsg = err.response?.data?.message || err.message || ''
    toast.error(t('callTransfers.acceptFailed'), {
      description: serverMsg,
    })
  } finally {
    acceptingId.value = null
  }
}

async function handleHangupWaiting(id: string) {
  hangingUpId.value = id
  try {
    await store.hangupWaitingTransfer(id)
  } finally {
    hangingUpId.value = null
  }
}
</script>

<template>
  <Teleport to="body">
    <div
      v-if="showPanel"
      class="fixed bottom-6 right-6 z-50 bg-zinc-900 border border-zinc-700 rounded-xl shadow-2xl p-4 w-[320px]"
    >
      <!-- Caller info -->
      <div class="flex items-start gap-3 mb-3">
        <div
          class="w-10 h-10 rounded-full flex items-center justify-center shrink-0"
          :class="isRinging ? 'bg-green-600/20' : 'bg-green-600/20'"
        >
          <PhoneIncoming v-if="isRinging" class="h-5 w-5 text-green-400 animate-pulse" />
          <Phone v-else class="h-5 w-5 text-green-400" />
        </div>
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-zinc-100 truncate">
            {{ displayName }}
          </p>
          <p class="text-xs text-zinc-400">{{ statusText }}</p>
          <p
            v-for="(line, i) in detailLines"
            :key="i"
            class="text-xs text-zinc-500 truncate mt-0.5"
          >
            {{ line }}
          </p>
        </div>
      </div>

      <!-- Timer (active call) -->
      <div v-if="store.isOnCall" class="text-center mb-3">
        <span class="text-2xl font-mono text-zinc-200 tabular-nums">{{ formattedDuration }}</span>
      </div>

      <!-- Call controls -->
      <div class="flex items-center justify-center gap-3">
        <!-- Mute -->
        <Button
          v-if="store.isOnCall"
          size="sm"
          variant="ghost"
          class="h-10 w-10 rounded-full p-0 border !text-zinc-300"
          :class="store.isMuted ? '!bg-red-900/30 !border-red-700 hover:!bg-red-900/50' : '!bg-zinc-800 !border-zinc-600 hover:!bg-zinc-700'"
          :title="store.isMuted ? t('callTransfers.unmute') : t('callTransfers.mute')"
          @click="store.toggleMute()"
        >
          <MicOff v-if="store.isMuted" class="h-4 w-4 !text-red-400" />
          <Mic v-else class="h-4 w-4" />
        </Button>

        <!-- Hold / Resume -->
        <Button
          v-if="store.isOnCall"
          size="sm"
          variant="ghost"
          class="h-10 w-10 rounded-full p-0 border !text-zinc-300"
          :class="store.isOnHold ? '!bg-amber-900/30 !border-amber-700 hover:!bg-amber-900/50' : '!bg-zinc-800 !border-zinc-600 hover:!bg-zinc-700'"
          :title="store.isOnHold ? t('calling.resume') : t('calling.hold')"
          @click="store.isOnHold ? store.resumeCall() : store.holdCall()"
        >
          <Play v-if="store.isOnHold" class="h-4 w-4 !text-amber-400" />
          <Pause v-else class="h-4 w-4" />
        </Button>

        <!-- Transfer -->
        <Button
          v-if="store.isOnCall"
          size="sm"
          variant="ghost"
          class="h-10 w-10 rounded-full p-0 border !bg-zinc-800 !border-zinc-600 !text-zinc-300 hover:!bg-zinc-700"
          :title="t('callTransfers.transfer')"
          @click="showTransferPicker = true"
        >
          <ArrowRightLeft class="h-4 w-4" />
        </Button>

        <!-- Answer incoming -->
        <Button
          v-if="firstWaiting"
          variant="ghost"
          size="sm"
          class="h-10 w-10 rounded-full p-0 !bg-green-600 !text-white hover:!bg-green-500"
          :disabled="acceptingId === firstWaiting.id"
          :title="t('calling.answer')"
          @click="handleAccept(firstWaiting.id)"
        >
          <Phone class="h-4 w-4" />
        </Button>

        <!-- Hangup waiting (before answer) -->
        <Button
          v-if="firstWaiting"
          variant="ghost"
          size="sm"
          class="h-10 w-10 rounded-full p-0 !bg-red-600 !text-white hover:!bg-red-500"
          :disabled="hangingUpId === firstWaiting.id"
          :title="t('callTransfers.hangup')"
          @click="handleHangupWaiting(firstWaiting.id)"
        >
          <PhoneOff class="h-4 w-4" />
        </Button>

        <!-- Hangup active call -->
        <Button
          v-if="store.isOnCall"
          variant="ghost"
          size="sm"
          class="h-10 w-10 rounded-full p-0 !bg-red-600 !text-white hover:!bg-red-500"
          :title="t('callTransfers.hangup')"
          @click="store.endCall()"
        >
          <PhoneOff class="h-4 w-4" />
        </Button>
      </div>
    </div>

    <CallTransferPicker v-if="showTransferPicker" @close="showTransferPicker = false" />
  </Teleport>
</template>
