<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { callLogsService, type CallLog, type CallTransfer } from '@/services/api'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import DetailPageLayout from '@/components/shared/DetailPageLayout.vue'
import IVRPathTree from '@/components/calling/IVRPathTree.vue'
import { formatDateTime, formatLabel } from '@/lib/utils'
import {
  formatCallDuration,
  callStatusVariant,
  callStatusIcon,
  disconnectedByIcon,
  disconnectedByVariant,
  disconnectedByKey,
  hasAICallArtifacts,
  resolveMediaURL,
} from '@/lib/callLogs'
import {
  Phone, PhoneIncoming, PhoneOutgoing, Clock, CalendarClock, PhoneOff, Info, Bot, User, Headphones,
  ArrowRightLeft, Mic, Route, AlertTriangle, MessageSquare, Copy, Workflow, Loader2,
} from 'lucide-vue-next'

const route = useRoute()
const { t } = useI18n()

const callId = computed(() => route.params.id as string)
const log = ref<CallLog | null>(null)
const transfers = ref<CallTransfer[]>([])
const isLoading = ref(true)

const recordingURL = ref<string | null>(null)
const recordingLoading = ref(false)
const aiRecordingURLs = ref<Partial<Record<'user' | 'assistant' | 'stereo', string>>>({})
const aiRecordingLoading = ref(false)

const displayName = computed(() => log.value?.contact?.profile_name || log.value?.caller_phone || t('calling.callDetail'))

const initials = computed(() => {
  const name = log.value?.contact?.profile_name
  if (!name) return ''
  return name.split(/\s+/).filter(Boolean).slice(0, 2).map(p => p[0]!.toUpperCase()).join('')
})

const breadcrumbs = computed(() => [
  { label: t('nav.calling'), href: '/calling/logs' },
  { label: t('calling.callLogs'), href: '/calling/logs' },
  { label: displayName.value },
])

const statusTone = computed(() => {
  switch (log.value?.status) {
    case 'completed':
    case 'answered':
    case 'accepted':
      return 'bg-emerald-500/10 text-emerald-500 ring-emerald-500/20'
    case 'missed':
      return 'bg-amber-500/10 text-amber-500 ring-amber-500/20'
    case 'rejected':
    case 'failed':
      return 'bg-red-500/10 text-red-500 ring-red-500/20'
    default:
      return 'bg-sky-500/10 text-sky-500 ring-sky-500/20'
  }
})

const transcript = computed(() => log.value?.ai_call_artifacts?.transcript ?? [])
const hasAIRecordings = computed(() => {
  const r = log.value?.ai_call_artifacts?.recordings
  return !!(r && (r.user || r.assistant || r.stereo))
})

const timeline = computed(() => {
  if (!log.value) return []
  const items: Array<{ key: string; label: string; at: string; tone: string }> = []
  const startedAt = log.value.started_at || log.value.created_at
  if (startedAt) items.push({ key: 'started', label: t('calling.startedAt'), at: startedAt, tone: 'bg-sky-500' })
  if (log.value.answered_at) items.push({ key: 'answered', label: t('calling.answeredAt'), at: log.value.answered_at, tone: 'bg-emerald-500' })
  for (const tr of transfers.value) {
    if (tr.transferred_at) items.push({ key: `transfer-${tr.id}`, label: t('calling.transferred'), at: tr.transferred_at, tone: 'bg-violet-500' })
  }
  if (log.value.ended_at) items.push({ key: 'ended', label: t('calling.endedAt'), at: log.value.ended_at, tone: 'bg-muted-foreground' })
  return items.sort((a, b) => new Date(a.at).getTime() - new Date(b.at).getTime())
})

function isAssistant(role: string): boolean {
  return role === 'assistant' || role === 'model' || role === 'bot'
}

function endsGroup(idx: number): boolean {
  const next = transcript.value[idx + 1]
  return !next || isAssistant(next.role) !== isAssistant(transcript.value[idx]!.role)
}

function formatDate(dateStr?: string): string {
  return dateStr ? formatDateTime(dateStr) : '-'
}

async function copyCallId() {
  if (!log.value?.whatsapp_call_id) return
  try {
    await navigator.clipboard.writeText(log.value.whatsapp_call_id)
    toast.success(t('common.copied'))
  } catch {
    // Clipboard unavailable (e.g. insecure context)
  }
}

function loadRecordings(detail: CallLog) {
  if (detail.recording_s3_key) {
    recordingLoading.value = true
    callLogsService.getRecordingURL(detail.id)
      .then(res => {
        const data = (res.data as any).data ?? res.data
        recordingURL.value = resolveMediaURL(data.url as string)
      })
      .catch(() => { recordingURL.value = null })
      .finally(() => { recordingLoading.value = false })
  }

  const aiRecs = detail.ai_call_artifacts?.recordings
  if (aiRecs && (aiRecs.user || aiRecs.assistant || aiRecs.stereo)) {
    aiRecordingLoading.value = true
    const kinds = (['stereo', 'user', 'assistant'] as const).filter(k => !!aiRecs[k])
    Promise.all(kinds.map(async (kind) => {
      try {
        const res = await callLogsService.getRecordingURL(detail.id, kind)
        const data = (res.data as any).data ?? res.data
        return [kind, resolveMediaURL(data.url as string)] as const
      } catch {
        return [kind, ''] as const
      }
    }))
      .then((entries) => {
        const next: Partial<Record<'user' | 'assistant' | 'stereo', string>> = {}
        for (const [kind, url] of entries) {
          if (url) next[kind] = url
        }
        aiRecordingURLs.value = next
      })
      .finally(() => { aiRecordingLoading.value = false })
  }
}

async function load() {
  isLoading.value = true
  log.value = null
  transfers.value = []
  recordingURL.value = null
  aiRecordingURLs.value = {}
  try {
    const res = await callLogsService.get(callId.value)
    const data = (res.data as any).data ?? res.data
    log.value = data.call_log ?? null
    transfers.value = data.transfers ?? []
  } catch {
    // handled by isNotFound
  } finally {
    isLoading.value = false
  }
  if (log.value) loadRecordings(log.value)
}

onMounted(load)
watch(callId, load)
</script>

<template>
  <DetailPageLayout
    :title="t('calling.callDetail')"
    :icon="Phone"
    back-link="/calling/logs"
    :breadcrumbs="breadcrumbs"
    :is-loading="isLoading"
    :is-not-found="!isLoading && !log"
    :not-found-title="t('calling.callNotFound')"
  >
    <template v-if="log">
      <!-- Summary -->
      <Card class="overflow-hidden">
        <CardContent class="p-0">
          <div class="flex flex-col gap-4 p-6 sm:flex-row sm:items-center">
            <div
              class="flex h-14 w-14 shrink-0 items-center justify-center rounded-full text-lg font-semibold ring-1"
              :class="statusTone"
            >
              <span v-if="initials">{{ initials }}</span>
              <component :is="callStatusIcon(log.status)" v-else class="h-6 w-6" />
            </div>
            <div class="min-w-0 flex-1">
              <h2 class="truncate text-xl font-semibold">{{ displayName }}</h2>
              <p v-if="log.contact?.profile_name" class="text-sm text-muted-foreground">{{ log.caller_phone }}</p>
              <div class="mt-2 flex flex-wrap items-center gap-2">
                <Badge :variant="callStatusVariant(log.status)">
                  <component :is="callStatusIcon(log.status)" class="mr-1 h-3 w-3" />
                  {{ t(`calling.${log.status}`) }}
                </Badge>
                <Badge variant="outline">
                  <PhoneIncoming v-if="log.direction === 'incoming'" class="mr-1 h-3 w-3" />
                  <PhoneOutgoing v-else class="mr-1 h-3 w-3" />
                  {{ t(`calling.${log.direction}`) }}
                </Badge>
                <Badge v-if="log.recording_s3_key || hasAICallArtifacts(log)" variant="secondary">
                  <Mic class="mr-1 h-3 w-3" />
                  {{ t('calling.recording') }}
                </Badge>
              </div>
            </div>
          </div>

          <div class="grid grid-cols-2 divide-x divide-y border-t sm:grid-cols-4 sm:divide-y-0">
            <div class="p-4">
              <p class="flex items-center gap-1.5 text-xs text-muted-foreground">
                <Clock class="h-3.5 w-3.5" /> {{ t('calling.duration') }}
              </p>
              <p class="mt-1 text-lg font-semibold tabular-nums">{{ formatCallDuration(log.duration) }}</p>
            </div>
            <div class="p-4">
              <p class="flex items-center gap-1.5 text-xs text-muted-foreground">
                <CalendarClock class="h-3.5 w-3.5" /> {{ t('calling.startedAt') }}
              </p>
              <p class="mt-1 text-sm font-medium">{{ formatDate(log.started_at || log.created_at) }}</p>
            </div>
            <div class="p-4">
              <p class="flex items-center gap-1.5 text-xs text-muted-foreground">
                <PhoneOff class="h-3.5 w-3.5" /> {{ t('calling.endedAt') }}
              </p>
              <p class="mt-1 text-sm font-medium">{{ formatDate(log.ended_at) }}</p>
            </div>
            <div class="p-4">
              <p class="flex items-center gap-1.5 text-xs text-muted-foreground">
                <User class="h-3.5 w-3.5" /> {{ t('calling.disconnectedBy') }}
              </p>
              <div class="mt-1">
                <Badge v-if="log.disconnected_by" :variant="disconnectedByVariant(log.disconnected_by)">
                  <component :is="disconnectedByIcon(log.disconnected_by)" class="mr-1 h-3 w-3" />
                  {{ t(disconnectedByKey(log.disconnected_by)) }}
                </Badge>
                <span v-else class="text-sm text-muted-foreground">-</span>
              </div>
            </div>
          </div>
        </CardContent>
      </Card>

      <!-- Error -->
      <div
        v-if="log.error_message"
        class="flex items-start gap-3 rounded-lg border border-destructive/30 bg-destructive/10 p-4 text-sm"
      >
        <AlertTriangle class="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
        <div>
          <p class="font-medium text-destructive">{{ t('calling.error') }}</p>
          <p class="mt-0.5 break-words text-destructive/90">{{ log.error_message }}</p>
        </div>
      </div>

      <!-- AI conversation -->
      <Card v-if="hasAICallArtifacts(log)">
        <CardHeader class="pb-3">
          <div class="flex items-center gap-2">
            <Bot class="h-4 w-4 text-muted-foreground" />
            <CardTitle class="text-sm font-medium">{{ t('calling.aiConversation') }}</CardTitle>
          </div>
        </CardHeader>
        <CardContent class="space-y-4">
          <div v-if="hasAIRecordings">
            <div v-if="aiRecordingLoading" class="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 class="h-4 w-4 animate-spin" />
              {{ t('common.loading') }}
            </div>
            <div v-else class="space-y-3">
              <div v-if="aiRecordingURLs.stereo" class="rounded-lg bg-muted/40 p-3">
                <p class="mb-2 text-xs font-medium text-muted-foreground">{{ t('calling.aiRecordingStereo') }}</p>
                <audio :src="aiRecordingURLs.stereo" controls preload="none" class="w-full" />
              </div>
              <div v-if="aiRecordingURLs.user || aiRecordingURLs.assistant" class="grid gap-3 sm:grid-cols-2">
                <div v-if="aiRecordingURLs.user" class="rounded-lg bg-muted/40 p-3">
                  <p class="mb-2 text-xs font-medium text-muted-foreground">{{ t('calling.aiRecordingUser') }}</p>
                  <audio :src="aiRecordingURLs.user" controls preload="none" class="w-full" />
                </div>
                <div v-if="aiRecordingURLs.assistant" class="rounded-lg bg-muted/40 p-3">
                  <p class="mb-2 text-xs font-medium text-muted-foreground">{{ t('calling.aiRecordingAssistant') }}</p>
                  <audio :src="aiRecordingURLs.assistant" controls preload="none" class="w-full" />
                </div>
              </div>
            </div>
          </div>

          <div v-if="transcript.length" class="max-h-[32rem] overflow-y-auto rounded-lg border bg-muted/20 p-4">
            <div
              v-for="(line, idx) in transcript"
              :key="idx"
              class="flex items-end gap-2"
              :class="[
                isAssistant(line.role) ? 'justify-start' : 'flex-row-reverse',
                endsGroup(idx) ? 'mb-4 last:mb-0' : 'mb-1',
              ]"
            >
              <div
                class="flex h-7 w-7 shrink-0 items-center justify-center rounded-full"
                :class="[
                  isAssistant(line.role) ? 'bg-primary/10 text-primary' : 'bg-muted text-muted-foreground',
                  !endsGroup(idx) && 'invisible',
                ]"
              >
                <Bot v-if="isAssistant(line.role)" class="h-3.5 w-3.5" />
                <User v-else class="h-3.5 w-3.5" />
              </div>
              <div
                class="max-w-[80%] whitespace-pre-wrap rounded-2xl px-3.5 py-2 text-sm"
                :class="isAssistant(line.role)
                  ? 'rounded-bl-sm bg-card ring-1 ring-border'
                  : 'rounded-br-sm bg-primary text-primary-foreground'"
              >
                {{ line.text }}
              </div>
            </div>
          </div>
          <p v-else class="text-sm text-muted-foreground">{{ t('calling.noTranscript') }}</p>
        </CardContent>
      </Card>

      <!-- Call recording -->
      <Card v-if="log.recording_s3_key">
        <CardHeader class="pb-3">
          <div class="flex items-center gap-2">
            <Mic class="h-4 w-4 text-muted-foreground" />
            <CardTitle class="text-sm font-medium">{{ t('calling.recording') }}</CardTitle>
            <span v-if="log.recording_duration" class="ml-auto text-xs text-muted-foreground tabular-nums">
              {{ formatCallDuration(log.recording_duration) }}
            </span>
          </div>
        </CardHeader>
        <CardContent>
          <div v-if="recordingLoading" class="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 class="h-4 w-4 animate-spin" />
            {{ t('common.loading') }}
          </div>
          <audio v-else-if="recordingURL" :src="recordingURL" controls preload="none" class="w-full" />
        </CardContent>
      </Card>

      <!-- Call path -->
      <Card v-if="log.ivr_path?.steps?.length">
        <CardHeader class="pb-3">
          <div class="flex items-center gap-2">
            <Route class="h-4 w-4 text-muted-foreground" />
            <CardTitle class="text-sm font-medium">{{ t('calling.ivrPath') }}</CardTitle>
          </div>
        </CardHeader>
        <CardContent>
          <IVRPathTree :steps="log.ivr_path.steps" />
        </CardContent>
      </Card>

      <!-- Transfers -->
      <Card v-if="transfers.length">
        <CardHeader class="pb-3">
          <div class="flex items-center gap-2">
            <ArrowRightLeft class="h-4 w-4 text-muted-foreground" />
            <CardTitle class="text-sm font-medium">{{ t('calling.transferHistory') }}</CardTitle>
          </div>
        </CardHeader>
        <CardContent class="space-y-3">
          <div v-for="transfer in transfers" :key="transfer.id" class="rounded-lg border p-3 text-sm">
            <div class="flex items-center justify-between gap-2">
              <Badge :variant="transfer.status === 'connected' || transfer.status === 'completed' ? 'default' : 'secondary'">
                {{ formatLabel(transfer.status) }}
              </Badge>
              <span class="text-xs text-muted-foreground">{{ formatDate(transfer.transferred_at) }}</span>
            </div>
            <dl class="mt-2 grid grid-cols-1 gap-1 sm:grid-cols-3">
              <div v-if="transfer.team">
                <dt class="text-xs text-muted-foreground">{{ t('calling.team') }}</dt>
                <dd class="font-medium">{{ transfer.team.name }}</dd>
              </div>
              <div v-if="transfer.initiating_agent">
                <dt class="text-xs text-muted-foreground">{{ t('calling.transferredBy') }}</dt>
                <dd class="font-medium">{{ transfer.initiating_agent.full_name }}</dd>
              </div>
              <div v-if="transfer.agent">
                <dt class="text-xs text-muted-foreground">{{ t('calling.pickedBy') }}</dt>
                <dd class="font-medium">{{ transfer.agent.full_name }}</dd>
              </div>
            </dl>
          </div>
        </CardContent>
      </Card>
    </template>

    <template #sidebar>
      <template v-if="log">
        <Card>
          <CardHeader class="pb-3">
            <div class="flex items-center gap-2">
              <Info class="h-4 w-4 text-muted-foreground" />
              <CardTitle class="text-sm font-medium">{{ t('calling.details') }}</CardTitle>
            </div>
          </CardHeader>
          <CardContent class="space-y-3 text-sm">
            <div>
              <span class="text-xs text-muted-foreground">{{ t('calling.account') }}</span>
              <p class="font-medium">{{ log.whatsapp_account }}</p>
            </div>
            <div>
              <span class="text-xs text-muted-foreground">{{ t('calling.ivrFlow') }}</span>
              <RouterLink
                v-if="log.ivr_flow"
                :to="{ name: 'ivr-flow-editor', params: { id: log.ivr_flow.id } }"
                class="flex items-center gap-1.5 font-medium hover:underline"
              >
                <Workflow class="h-3.5 w-3.5 text-muted-foreground" />
                {{ log.ivr_flow.name }}
              </RouterLink>
              <p v-else class="text-muted-foreground">-</p>
            </div>
            <div>
              <span class="text-xs text-muted-foreground">{{ t('calling.agent') }}</span>
              <p v-if="log.agent" class="flex items-center gap-1.5 font-medium">
                <Headphones class="h-3.5 w-3.5 text-muted-foreground" />
                {{ log.agent.full_name }}
              </p>
              <p v-else class="text-muted-foreground">-</p>
            </div>
            <div v-if="log.whatsapp_call_id">
              <span class="text-xs text-muted-foreground">{{ t('calling.callId') }}</span>
              <div class="flex items-center gap-1">
                <code class="min-w-0 flex-1 truncate rounded bg-muted px-1.5 py-0.5 text-xs" :title="log.whatsapp_call_id">
                  {{ log.whatsapp_call_id }}
                </code>
                <Button variant="ghost" size="icon" class="h-7 w-7 shrink-0" :title="t('common.copy')" @click="copyCallId">
                  <Copy class="h-3.5 w-3.5" />
                </Button>
              </div>
            </div>
            <RouterLink
              v-if="log.contact_id"
              :to="{ name: 'chat-conversation', params: { contactId: log.contact_id } }"
            >
              <Button variant="outline" size="sm" class="mt-2 w-full">
                <MessageSquare class="mr-1.5 h-3.5 w-3.5" />
                {{ t('calling.openChat') }}
              </Button>
            </RouterLink>
          </CardContent>
        </Card>

        <Card v-if="timeline.length">
          <CardHeader class="pb-3">
            <div class="flex items-center gap-2">
              <CalendarClock class="h-4 w-4 text-muted-foreground" />
              <CardTitle class="text-sm font-medium">{{ t('calling.timeline') }}</CardTitle>
            </div>
          </CardHeader>
          <CardContent>
            <ol class="relative ml-1.5 space-y-4 border-l pl-5">
              <li v-for="item in timeline" :key="item.key" class="relative">
                <span class="absolute -left-[26px] top-1 h-2.5 w-2.5 rounded-full ring-4 ring-background" :class="item.tone" />
                <p class="text-sm font-medium">{{ item.label }}</p>
                <p class="text-xs text-muted-foreground">{{ formatDate(item.at) }}</p>
              </li>
            </ol>
          </CardContent>
        </Card>
      </template>
    </template>
  </DetailPageLayout>
</template>
