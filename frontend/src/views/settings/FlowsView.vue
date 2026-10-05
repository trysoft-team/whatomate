<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Label } from '@/components/ui/label'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from '@/components/ui/dialog'
import { Textarea } from '@/components/ui/textarea'
import { PageHeader, DeleteConfirmDialog, ConfirmDialog, DataTable, SearchInput, IconButton, ErrorState, type Column } from '@/components/shared'
import { flowsService, accountsService } from '@/services/api'
import { AI_WHATSAPP_FLOW_DRAFT_KEY } from '@/lib/whatsappFlowDraft'
import { toast } from 'vue-sonner'
import { Plus, Pencil, Trash2, Workflow, Play, ExternalLink, Loader2, Archive, RefreshCw, Upload, Copy, Sparkles } from 'lucide-vue-next'
import { getErrorMessage } from '@/lib/api-utils'
import { formatDate } from '@/lib/utils'
import { useSearchPagination } from '@/composables/useSearchPagination'

const { t } = useI18n()
const router = useRouter()

interface WhatsAppFlow {
  id: string; whatsapp_account: string; meta_flow_id: string; name: string; status: 'DRAFT' | 'PUBLISHED' | 'DEPRECATED'
  category: string; json_version: string; flow_json: Record<string, any>; screens: any[]; preview_url?: string
  has_local_changes: boolean; created_at: string; updated_at: string
}
interface Account { id: string; name: string }

const flows = ref<WhatsAppFlow[]>([])
const accounts = ref<Account[]>([])
const isLoading = ref(true)
const fetchError = ref(false)
const selectedAccount = ref<string>(localStorage.getItem('flows_selected_account') || 'all')

const isSyncing = ref(false)
const savingToMetaFlowId = ref<string | null>(null)
const publishingFlowId = ref<string | null>(null)
const duplicatingFlowId = ref<string | null>(null)
const deleteDialogOpen = ref(false)
const publishDialogOpen = ref(false)
const saveToMetaDialogOpen = ref(false)
const flowToDelete = ref<WhatsAppFlow | null>(null)
const flowToPublish = ref<WhatsAppFlow | null>(null)
const flowToSaveToMeta = ref<WhatsAppFlow | null>(null)

const { searchQuery, currentPage, totalItems, pageSize, handlePageChange } = useSearchPagination({
  fetchFn: () => fetchFlows(),
})

const columns = computed<Column<WhatsAppFlow>[]>(() => [
  { key: 'name', label: t('flows.name'), sortable: true },
  { key: 'status', label: t('flows.status'), sortable: true },
  { key: 'category', label: t('flows.category'), sortable: true },
  { key: 'created_at', label: t('flows.created'), sortable: true },
  { key: 'actions', label: t('common.actions'), align: 'right' },
])

const sortKey = ref('name')
const sortDirection = ref<'asc' | 'desc'>('asc')

onMounted(async () => { await fetchAccounts(); await fetchFlows() })

async function fetchAccounts() {
  try {
    const response = await accountsService.list()
    accounts.value = response.data.data?.accounts || []
    if (selectedAccount.value !== 'all' && !accounts.value.some(a => a.name === selectedAccount.value)) {
      selectedAccount.value = 'all'; localStorage.setItem('flows_selected_account', 'all')
    }
  } catch { /* ignore */ }
}

function onAccountChange(value: string | number | bigint | Record<string, any> | null) {
  if (typeof value !== 'string') return
  localStorage.setItem('flows_selected_account', value)
  currentPage.value = 1
  fetchFlows()
}

async function fetchFlows() {
  isLoading.value = true
  fetchError.value = false
  try {
    const response = await flowsService.list({
      account: selectedAccount.value !== 'all' ? selectedAccount.value : undefined,
      search: searchQuery.value || undefined,
      page: currentPage.value,
      limit: pageSize
    })
    const data = (response.data as any).data || response.data
    flows.value = data.flows || []
    totalItems.value = data.total ?? flows.value.length
  } catch { flows.value = []; fetchError.value = true }
  finally { isLoading.value = false }
}

function goCreate() {
  const query =
    selectedAccount.value && selectedAccount.value !== 'all'
      ? { account: selectedAccount.value }
      : undefined
  router.push({ path: '/flows/new', query })
}

const generateAIDialogOpen = ref(false)
const generateAIPrompt = ref('')
const isGeneratingAI = ref(false)

const generateAISuggestions = computed(() => [
  t('flows.generateWithAISuggestion1'),
  t('flows.generateWithAISuggestion2'),
  t('flows.generateWithAISuggestion3'),
])

function openGenerateAI() {
  generateAIPrompt.value = ''
  generateAIDialogOpen.value = true
}

async function confirmGenerateAI() {
  const prompt = generateAIPrompt.value.trim()
  if (!prompt) {
    toast.error(t('flows.generateWithAIPromptRequired'))
    return
  }
  const account = selectedAccount.value !== 'all' ? selectedAccount.value : undefined
  isGeneratingAI.value = true
  try {
    const response = await flowsService.generate({ prompt, whatsapp_account: account })
    const data = (response.data as any)?.data || response.data
    if (!Array.isArray(data?.screens) || data.screens.length === 0) {
      throw new Error('Invalid flow returned')
    }
    sessionStorage.setItem(
      AI_WHATSAPP_FLOW_DRAFT_KEY,
      JSON.stringify({ name: data.name || '', category: data.category || '', screens: data.screens }),
    )
    generateAIDialogOpen.value = false
    router.push({ path: '/flows/new', query: { ai: '1', ...(account ? { account } : {}) } })
  } catch (e) {
    toast.error(getErrorMessage(e, t('flows.generateWithAIFailed')))
  } finally {
    isGeneratingAI.value = false
  }
}

function goEdit(flow: WhatsAppFlow) {
  router.push(`/flows/${flow.id}/edit`)
}

function openSaveToMetaDialog(flow: WhatsAppFlow) {
  flowToSaveToMeta.value = flow
  saveToMetaDialogOpen.value = true
}

async function confirmSaveToMeta() {
  if (!flowToSaveToMeta.value) return
  savingToMetaFlowId.value = flowToSaveToMeta.value.id
  try { await flowsService.saveToMeta(flowToSaveToMeta.value.id); toast.success(t('flows.flowSavedToMeta')); saveToMetaDialogOpen.value = false; flowToSaveToMeta.value = null; await fetchFlows() }
  catch (e) { toast.error(getErrorMessage(e, t('flows.saveToMetaFailed'))) }
  finally { savingToMetaFlowId.value = null }
}

function openPublishDialog(flow: WhatsAppFlow) {
  flowToPublish.value = flow
  publishDialogOpen.value = true
}

async function confirmPublishFlow() {
  if (!flowToPublish.value) return
  publishingFlowId.value = flowToPublish.value.id
  try { await flowsService.publish(flowToPublish.value.id); toast.success(t('flows.flowPublished')); publishDialogOpen.value = false; flowToPublish.value = null; await fetchFlows() }
  catch (e) { toast.error(getErrorMessage(e, t('flows.publishFailed'))) }
  finally { publishingFlowId.value = null }
}

async function confirmDeleteFlow() {
  if (!flowToDelete.value) return
  try { await flowsService.delete(flowToDelete.value.id); toast.success(t('common.deletedSuccess', { resource: t('resources.Flow') })); deleteDialogOpen.value = false; flowToDelete.value = null; await fetchFlows() }
  catch (e) { toast.error(getErrorMessage(e, t('common.failedDelete', { resource: t('resources.flow') }))) }
}

async function duplicateFlow(flow: WhatsAppFlow) {
  duplicatingFlowId.value = flow.id
  try { await flowsService.duplicate(flow.id); toast.success(t('flows.flowDuplicated')); await fetchFlows() }
  catch (e) { toast.error(getErrorMessage(e, t('flows.duplicateFailed'))) }
  finally { duplicatingFlowId.value = null }
}

async function syncFlows() {
  isSyncing.value = true
  try {
    const account = selectedAccount.value && selectedAccount.value !== 'all' ? selectedAccount.value : undefined
    const response = await flowsService.sync(account)
    const data = response.data.data
    toast.success(t('flows.syncSuccess', { synced: data.synced, created: data.created, updated: data.updated }))
    if (data.failed_accounts?.length) {
      toast.warning(t('flows.syncPartialFailed', { accounts: data.failed_accounts.join(', ') }))
    }
    await fetchFlows()
  }
  catch (e) { toast.error(getErrorMessage(e, t('flows.syncFailed'))) }
  finally { isSyncing.value = false }
}

function openPreviewUrl(url: string) { window.open(url, '_blank') }
function getStatusClass(status: string): string { return { PUBLISHED: 'border-green-600 text-green-600', DEPRECATED: 'border-destructive text-destructive' }[status] || '' }
function isFlowDraft(flow: WhatsAppFlow): boolean { return flow.status?.toUpperCase() === 'DRAFT' }
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader :title="$t('flows.title')" :subtitle="$t('flows.subtitle')" :icon="Workflow" icon-gradient="bg-gradient-to-br from-violet-500 to-purple-600 shadow-violet-500/20">
      <template #actions>
        <div class="flex items-center gap-2">
          <Button variant="outline" size="sm" @click="syncFlows" :disabled="isSyncing || accounts.length === 0"><RefreshCw :class="['h-4 w-4 mr-2', isSyncing && 'animate-spin']" />{{ $t('flows.syncFromMeta') }}</Button>
          <button
            type="button"
            class="btn-press relative inline-flex h-8 items-center overflow-hidden rounded-full bg-gradient-to-r from-cyan-400 to-fuchsia-500 p-px text-xs font-medium shadow-[0_0_16px_rgba(34,211,238,0.35),0_0_28px_rgba(168,85,247,0.35)] transition-shadow duration-200 hover:shadow-[0_0_20px_rgba(34,211,238,0.45),0_0_34px_rgba(168,85,247,0.45)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-violet-500/50 focus-visible:ring-offset-2 focus-visible:ring-offset-background"
            @click="openGenerateAI"
          >
            <span class="inline-flex h-full items-center gap-2 rounded-full bg-[#12121a] px-3 text-xs text-violet-300 light:bg-violet-50 light:text-violet-600">
              <Sparkles class="h-3.5 w-3.5 shrink-0" />
              {{ $t('flows.generateWithAI') }}
            </span>
          </button>
          <Button variant="outline" size="sm" @click="goCreate"><Plus class="h-4 w-4 mr-2" />{{ $t('flows.createFlow') }}</Button>
        </div>
      </template>
    </PageHeader>

    <ErrorState
      v-if="fetchError && !isLoading"
      :title="$t('flows.fetchErrorTitle')"
      :description="$t('flows.fetchErrorDescription')"
      :retry-label="$t('common.retry')"
      class="flex-1"
      @retry="fetchFlows"
    />

    <ScrollArea v-else class="flex-1">
      <div class="p-6">
        <div class="max-w-6xl mx-auto">
          <Card>
            <CardHeader>
              <div class="flex items-center justify-between flex-wrap gap-4">
                <div>
                  <CardTitle>{{ $t('flows.yourFlows') }}</CardTitle>
                  <CardDescription>{{ $t('flows.yourFlowsDesc') }}</CardDescription>
                </div>
                <div class="flex items-center gap-2">
                  <Label class="text-sm text-muted-foreground">{{ $t('flows.account') }}:</Label>
                  <Select v-model="selectedAccount" @update:model-value="onAccountChange">
                    <SelectTrigger class="w-[180px]"><SelectValue :placeholder="$t('flows.allAccounts')" /></SelectTrigger>
                    <SelectContent><SelectItem value="all">{{ $t('flows.allAccounts') }}</SelectItem><SelectItem v-for="account in accounts" :key="account.id" :value="account.name">{{ account.name }}</SelectItem></SelectContent>
                  </Select>
                  <SearchInput v-model="searchQuery" :placeholder="$t('flows.searchFlows') + '...'" class="w-64" />
                </div>
              </div>
            </CardHeader>
            <CardContent>
              <DataTable
                :items="flows"
                :columns="columns"
                :is-loading="isLoading"
                :empty-icon="Workflow"
                :empty-title="searchQuery ? $t('flows.noMatchingFlows') : $t('flows.noFlowsYet')"
                :empty-description="searchQuery ? $t('flows.noMatchingFlowsDesc') : $t('flows.noFlowsYetDesc')"
                server-pagination
                :current-page="currentPage"
                :total-items="totalItems"
                :page-size="pageSize"
                item-name="flows"
                @page-change="handlePageChange"
                v-model:sort-key="sortKey"
                v-model:sort-direction="sortDirection"
              >
                <template #cell-name="{ item: flow }">
                  <div>
                    <span class="font-medium">{{ flow.name }}</span>
                    <p class="text-xs text-muted-foreground">{{ flow.whatsapp_account }}</p>
                  </div>
                </template>
                <template #cell-status="{ item: flow }">
                  <Badge v-if="flow.status?.toUpperCase() === 'DEPRECATED'" variant="destructive" class="text-xs">
                    <Archive class="h-3 w-3 mr-1" />{{ $t('flows.deprecated') }}
                  </Badge>
                  <Badge v-else variant="outline" :class="[getStatusClass(flow.status), 'text-xs']">{{ flow.status }}</Badge>
                </template>
                <template #cell-category="{ item: flow }">
                  <Badge v-if="flow.category" variant="outline" class="text-xs">{{ flow.category }}</Badge>
                  <span v-else class="text-muted-foreground">—</span>
                </template>
                <template #cell-created_at="{ item: flow }">
                  <span class="text-muted-foreground text-sm">{{ formatDate(flow.created_at) }}</span>
                </template>
                <template #cell-actions="{ item: flow }">
                  <div class="flex items-center justify-end gap-1">
                    <IconButton :icon="Pencil" :label="$t('flows.editTooltip')" class="h-8 w-8" @click="goEdit(flow)" />
                    <IconButton :icon="duplicatingFlowId === flow.id ? Loader2 : Copy" :label="$t('flows.duplicateTooltip')" class="h-8 w-8" :disabled="duplicatingFlowId === flow.id" @click="duplicateFlow(flow)" />
                    <IconButton v-if="flow.preview_url" :icon="ExternalLink" :label="$t('flows.previewTooltip')" class="h-8 w-8" @click="openPreviewUrl(flow.preview_url!)" />
                    <IconButton
                      v-if="flow.status?.toUpperCase() !== 'DEPRECATED' && (flow.has_local_changes || !flow.meta_flow_id)"
                      :icon="savingToMetaFlowId === flow.id ? Loader2 : Upload"
                      :label="flow.meta_flow_id ? $t('flows.updateOnMeta') : $t('flows.saveToMeta')"
                      class="h-8 w-8"
                      :disabled="savingToMetaFlowId === flow.id || publishingFlowId === flow.id"
                      @click="openSaveToMetaDialog(flow)"
                    />
                    <IconButton
                      v-if="isFlowDraft(flow) && flow.meta_flow_id"
                      :icon="publishingFlowId === flow.id ? Loader2 : Play"
                      :label="$t('flows.publishTooltip')"
                      class="h-8 w-8 text-green-600"
                      :disabled="savingToMetaFlowId === flow.id || publishingFlowId === flow.id"
                      @click="openPublishDialog(flow)"
                    />
                    <IconButton
                      :icon="Trash2"
                      :label="$t('flows.deleteTooltip')"
                      class="h-8 w-8 text-destructive"
                      :disabled="flow.status?.toUpperCase() === 'PUBLISHED'"
                      @click="flowToDelete = flow; deleteDialogOpen = true"
                    />
                  </div>
                </template>
                <template #empty-action>
                  <Button v-if="!searchQuery" variant="outline" size="sm" @click="goCreate">
                    <Plus class="h-4 w-4 mr-2" />{{ $t('flows.createFlow') }}
                  </Button>
                </template>
              </DataTable>
            </CardContent>
          </Card>
        </div>
      </div>
    </ScrollArea>

    <DeleteConfirmDialog v-model:open="deleteDialogOpen" :title="$t('flows.deleteFlow')" :item-name="flowToDelete?.name" @confirm="confirmDeleteFlow" />

    <ConfirmDialog
      v-model:open="publishDialogOpen"
      :title="$t('flows.publishConfirmTitle')"
      :description="$t('flows.publishConfirmDescription', { name: flowToPublish?.name })"
      :confirm-label="$t('flows.publishTooltip')"
      variant="destructive"
      :is-submitting="!!publishingFlowId"
      @confirm="confirmPublishFlow"
    />

    <ConfirmDialog
      v-model:open="saveToMetaDialogOpen"
      :title="$t('flows.saveToMetaConfirmTitle')"
      :description="$t('flows.saveToMetaConfirmDescription', { name: flowToSaveToMeta?.name })"
      :confirm-label="$t('flows.saveToMeta')"
      :is-submitting="!!savingToMetaFlowId"
      @confirm="confirmSaveToMeta"
    />

    <Dialog v-model:open="generateAIDialogOpen">
      <DialogContent class="sm:max-w-lg">
        <DialogHeader>
          <div class="flex items-start gap-3">
            <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 light:bg-violet-100">
              <Sparkles class="h-5 w-5 text-primary light:text-violet-600" />
            </div>
            <div class="min-w-0 space-y-1">
              <DialogTitle>{{ $t('flows.generateWithAI') }}</DialogTitle>
              <DialogDescription>{{ $t('flows.generateWithAISubtitle') }}</DialogDescription>
            </div>
          </div>
        </DialogHeader>

        <div class="space-y-3">
          <Textarea
            v-model="generateAIPrompt"
            :rows="5"
            class="min-h-[120px] resize-y"
            :placeholder="$t('flows.generateWithAIPlaceholder')"
          />

          <div class="flex flex-col gap-2">
            <button
              v-for="(suggestion, index) in generateAISuggestions"
              :key="index"
              type="button"
              class="rounded-full border border-border bg-background px-3 py-2 text-left text-xs text-muted-foreground transition-colors hover:border-primary/40 hover:bg-muted/40 hover:text-foreground"
              @click="generateAIPrompt = suggestion"
            >
              {{ suggestion }}
            </button>
          </div>

          <p class="flex items-start gap-1.5 text-[11px] leading-relaxed text-muted-foreground">
            <Sparkles class="mt-0.5 h-3 w-3 shrink-0 text-primary/70" />
            <span>{{ $t('flows.generateWithAIHint') }}</span>
          </p>
        </div>

        <DialogFooter>
          <Button variant="outline" :disabled="isGeneratingAI" @click="generateAIDialogOpen = false">
            {{ $t('common.cancel') }}
          </Button>
          <Button :disabled="isGeneratingAI || !generateAIPrompt.trim()" @click="confirmGenerateAI">
            <Loader2 v-if="isGeneratingAI" class="h-4 w-4 mr-2 animate-spin" />
            <Sparkles v-else class="h-4 w-4 mr-2" />
            {{ isGeneratingAI ? $t('flows.generateWithAIGenerating') : $t('flows.generateWithAI') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
