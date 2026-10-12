<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { PageHeader, DataTable, IconButton, ErrorState, type Column } from '@/components/shared'
import { chatbotService } from '@/services/api'
import { MessageSquare, RefreshCw, ExternalLink, History } from 'lucide-vue-next'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

interface SessionContact {
  id?: string
  profile_name?: string
  phone_number?: string
}

interface SessionFlow {
  id?: string
  name?: string
}

interface ChatbotSessionRow {
  id: string
  contact_id?: string
  phone_number: string
  whatsapp_account: string
  status: string
  current_step?: string
  current_flow_id?: string
  started_at: string
  last_activity_at: string
  completed_at?: string
  contact?: SessionContact
  current_flow?: SessionFlow
}

interface SessionMessage {
  id: string
  direction: string
  message: string
  step_name?: string
  created_at?: string
}

const sessions = ref<ChatbotSessionRow[]>([])
const isLoading = ref(true)
const error = ref<string | null>(null)
const statusFilter = ref<string>((route.query.status as string) || 'all')

const detailOpen = ref(false)
const detailLoading = ref(false)
const selected = ref<ChatbotSessionRow | null>(null)
const detailMessages = ref<SessionMessage[]>([])

const columns = computed<Column<ChatbotSessionRow>[]>(() => [
  { key: 'contact', label: t('chatbotSessions.contact') },
  { key: 'status', label: t('chatbotSessions.status'), sortable: true },
  { key: 'flow', label: t('chatbotSessions.flow') },
  { key: 'step', label: t('chatbotSessions.step') },
  { key: 'account', label: t('chatbotSessions.account') },
  { key: 'last_activity', label: t('chatbotSessions.lastActivity'), sortable: true, sortKey: 'last_activity_at' },
  { key: 'actions', label: '', align: 'right' },
])

const sortKey = ref('last_activity_at')
const sortDirection = ref<'asc' | 'desc'>('desc')

const filteredSessions = computed(() => {
  const list = [...sessions.value]
  const key = sortKey.value
  const dir = sortDirection.value === 'asc' ? 1 : -1
  list.sort((a: any, b: any) => {
    const av = a[key] ?? ''
    const bv = b[key] ?? ''
    if (av < bv) return -1 * dir
    if (av > bv) return 1 * dir
    return 0
  })
  return list
})

onMounted(fetchSessions)
watch(statusFilter, () => {
  router.replace({ query: statusFilter.value === 'all' ? {} : { status: statusFilter.value } })
  fetchSessions()
})

async function fetchSessions() {
  isLoading.value = true
  error.value = null
  try {
    const params: { status?: string } = {}
    if (statusFilter.value && statusFilter.value !== 'all') {
      params.status = statusFilter.value
    }
    const response = await chatbotService.listSessions(params)
    const data = (response.data as any).data || response.data
    sessions.value = data.sessions || []
  } catch (err) {
    console.error('Failed to load chatbot sessions:', err)
    error.value = t('chatbotSessions.fetchError')
    sessions.value = []
  } finally {
    isLoading.value = false
  }
}

function contactLabel(s: ChatbotSessionRow) {
  return s.contact?.profile_name || s.phone_number || s.contact?.phone_number || s.id
}

function statusTone(status: string): 'default' | 'secondary' | 'destructive' | 'outline' {
  switch (status) {
    case 'active':
      return 'default'
    case 'completed':
      return 'secondary'
    case 'cancelled':
    case 'timeout':
      return 'outline'
    default:
      return 'outline'
  }
}

function formatTime(iso?: string) {
  if (!iso) return '—'
  try {
    return new Date(iso).toLocaleString()
  } catch {
    return iso
  }
}

async function openSession(s: ChatbotSessionRow) {
  selected.value = s
  detailOpen.value = true
  detailLoading.value = true
  detailMessages.value = []
  try {
    const response = await chatbotService.getSession(s.id)
    const data = (response.data as any).data || response.data
    detailMessages.value = data.messages || []
    if (data.contact) selected.value = { ...s, contact: data.contact }
  } catch (err) {
    console.error('Failed to load session detail:', err)
    detailMessages.value = []
  } finally {
    detailLoading.value = false
  }
}

function openInChat(s: ChatbotSessionRow) {
  const id = s.contact?.id || s.contact_id
  if (!id) return
  router.push(`/chat?contact=${id}`)
}

function hasChatLink(s: ChatbotSessionRow) {
  return !!(s.contact?.id || s.contact_id)
}
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader
      :title="$t('chatbotSessions.title')"
      :icon="History"
      icon-gradient="bg-gradient-to-br from-blue-500 to-indigo-600 shadow-blue-500/20"
      back-link="/chatbot"
      :breadcrumbs="[
        { label: $t('chatbotSessions.backToChatbot'), href: '/chatbot' },
        { label: $t('nav.sessions') },
      ]"
    >
      <template #actions>
        <Button variant="outline" size="sm" :disabled="isLoading" @click="fetchSessions">
          <RefreshCw class="h-4 w-4 mr-2" :class="{ 'animate-spin': isLoading }" />
          {{ $t('common.refresh') }}
        </Button>
      </template>
    </PageHeader>

    <ScrollArea class="flex-1">
      <div class="p-6">
        <Card>
          <CardHeader>
            <div class="flex flex-wrap items-center justify-between gap-3">
              <div>
                <CardTitle>{{ $t('chatbotSessions.listTitle') }}</CardTitle>
                <CardDescription>{{ $t('chatbotSessions.listDesc') }}</CardDescription>
              </div>
              <Select v-model="statusFilter">
                <SelectTrigger class="w-44 h-9">
                  <SelectValue :placeholder="$t('chatbotSessions.filterStatus')" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">{{ $t('chatbotSessions.statusAll') }}</SelectItem>
                  <SelectItem value="active">{{ $t('chatbotSessions.statusActive') }}</SelectItem>
                  <SelectItem value="completed">{{ $t('chatbotSessions.statusCompleted') }}</SelectItem>
                  <SelectItem value="cancelled">{{ $t('chatbotSessions.statusCancelled') }}</SelectItem>
                  <SelectItem value="timeout">{{ $t('chatbotSessions.statusTimeout') }}</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </CardHeader>
          <CardContent>
            <ErrorState
              v-if="error"
              :title="$t('common.loadErrorTitle')"
              :description="error"
              :retry-label="$t('common.retry')"
              @retry="fetchSessions"
            />
            <DataTable
              v-else
              :items="filteredSessions"
              :columns="columns"
              :is-loading="isLoading"
              :empty-icon="History"
              :empty-title="$t('chatbotSessions.emptyTitle')"
              :empty-description="$t('chatbotSessions.emptyDesc')"
              v-model:sort-key="sortKey"
              v-model:sort-direction="sortDirection"
              item-name="sessions"
            >
              <template #cell-contact="{ item: s }">
                <button type="button" class="text-left hover:opacity-80" @click.stop="openSession(s)">
                  <div class="font-medium">{{ contactLabel(s) }}</div>
                  <div class="text-xs text-muted-foreground font-mono">{{ s.phone_number }}</div>
                </button>
              </template>
              <template #cell-status="{ item: s }">
                <Badge :variant="statusTone(s.status)" class="capitalize">{{ s.status }}</Badge>
              </template>
              <template #cell-flow="{ item: s }">
                <span class="text-sm">{{ s.current_flow?.name || '—' }}</span>
              </template>
              <template #cell-step="{ item: s }">
                <code class="text-xs text-muted-foreground">{{ s.current_step || '—' }}</code>
              </template>
              <template #cell-account="{ item: s }">
                <span class="text-sm text-muted-foreground">{{ s.whatsapp_account }}</span>
              </template>
              <template #cell-last_activity="{ item: s }">
                <span class="text-sm text-muted-foreground">{{ formatTime(s.last_activity_at) }}</span>
              </template>
              <template #cell-actions="{ item: s }">
                <div class="flex justify-end gap-1">
                  <IconButton
                    :icon="MessageSquare"
                    :label="$t('chatbotSessions.viewTranscript')"
                    class="h-8 w-8"
                    @click.stop="openSession(s)"
                  />
                  <IconButton
                    v-if="hasChatLink(s)"
                    :icon="ExternalLink"
                    :label="$t('chatbotSessions.openChat')"
                    class="h-8 w-8"
                    @click.stop="openInChat(s)"
                  />
                </div>
              </template>
            </DataTable>
          </CardContent>
        </Card>
      </div>
    </ScrollArea>

    <Dialog v-model:open="detailOpen">
      <DialogContent class="max-w-lg max-h-[85vh] flex flex-col">
        <DialogHeader>
          <DialogTitle>{{ selected ? contactLabel(selected) : $t('chatbotSessions.sessionDetail') }}</DialogTitle>
          <DialogDescription v-if="selected" class="flex flex-wrap gap-2 items-center">
            <Badge :variant="statusTone(selected.status)" class="capitalize">{{ selected.status }}</Badge>
            <span class="font-mono text-xs">{{ selected.phone_number }}</span>
            <span v-if="selected.current_flow?.name" class="text-xs">· {{ selected.current_flow.name }}</span>
          </DialogDescription>
        </DialogHeader>

        <div v-if="selected" class="text-xs text-muted-foreground space-y-0.5 pb-2 border-b">
          <div>{{ $t('chatbotSessions.started') }}: {{ formatTime(selected.started_at) }}</div>
          <div>{{ $t('chatbotSessions.lastActivity') }}: {{ formatTime(selected.last_activity_at) }}</div>
          <div v-if="selected.current_step">{{ $t('chatbotSessions.step') }}: <code>{{ selected.current_step }}</code></div>
        </div>

        <ScrollArea class="flex-1 min-h-[240px] max-h-[50vh] pr-2">
          <div v-if="detailLoading" class="py-8 text-center text-sm text-muted-foreground">
            {{ $t('common.loading') }}…
          </div>
          <div v-else-if="detailMessages.length === 0" class="py-8 text-center text-sm text-muted-foreground">
            {{ $t('chatbotSessions.noMessages') }}
          </div>
          <div v-else class="space-y-2 py-2">
            <div
              v-for="m in detailMessages"
              :key="m.id"
              class="rounded-lg px-3 py-2 text-sm max-w-[90%]"
              :class="m.direction === 'outgoing'
                ? 'ml-auto bg-emerald-500/15 border border-emerald-500/20'
                : 'mr-auto bg-muted/60 border border-border'"
            >
              <div class="whitespace-pre-wrap break-words">{{ m.message }}</div>
              <div class="text-[10px] text-muted-foreground mt-1 flex gap-2">
                <span class="capitalize">{{ m.direction }}</span>
                <span v-if="m.step_name">· {{ m.step_name }}</span>
              </div>
            </div>
          </div>
        </ScrollArea>

        <div v-if="selected && hasChatLink(selected)" class="pt-2 border-t flex justify-end">
          <Button variant="outline" size="sm" @click="openInChat(selected!)">
            <ExternalLink class="h-4 w-4 mr-2" />
            {{ $t('chatbotSessions.openChat') }}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  </div>
</template>
