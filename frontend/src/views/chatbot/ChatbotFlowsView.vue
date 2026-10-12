<script setup lang="ts">
import { ref, onMounted, watch, computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Switch } from '@/components/ui/switch'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { chatbotService } from '@/services/api'
import { toast } from 'vue-sonner'
import { PageHeader, DataTable, DeleteConfirmDialog, SearchInput, IconButton, ErrorState, ConfirmDialog, type Column } from '@/components/shared'
import { getErrorMessage } from '@/lib/api-utils'
import { Plus, Pencil, Trash2, Workflow, Sparkles } from 'lucide-vue-next'
import { useDebounceFn } from '@vueuse/core'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from '@/components/ui/dialog'
import { Textarea } from '@/components/ui/textarea'
import { FLOW_TEMPLATES, type FlowTemplateId } from '@/chatbot/flowTemplates'

const { t } = useI18n()

interface ChatbotFlow {
  id: string
  name: string
  description: string
  whatsapp_account: string
  is_default: boolean
  trigger_keywords: string[]
  steps_count: number
  enabled: boolean
  created_at: string
}

const router = useRouter()
const flows = ref<ChatbotFlow[]>([])
const isLoading = ref(true)
const error = ref<string | null>(null)
const searchQuery = ref('')
const deleteDialogOpen = ref(false)
const isDeleting = ref(false)
const flowToDelete = ref<ChatbotFlow | null>(null)

const defaultConfirmOpen = ref(false)
const defaultConfirmFlow = ref<ChatbotFlow | null>(null)
const defaultConfirmMakeDefault = ref(false)
const defaultConfirmSessionCount = ref(0)
const isUpdatingDefault = ref(false)

const templateDialogOpen = ref(false)
const selectedTemplate = ref<FlowTemplateId>('blank')
const flowTemplates = FLOW_TEMPLATES

const generateAIDialogOpen = ref(false)
const generateAIPrompt = ref('')
const isGeneratingAI = ref(false)

const generateAISuggestions = computed(() => [
  t('chatbotFlows.generateWithAISuggestion1', 'Welcome new contacts with a WhatsApp message, then tag them as onboarded'),
  t('chatbotFlows.generateWithAISuggestion2', 'Recover abandoned carts: wait 1 hour, send a reminder with the recovery link'),
  t('chatbotFlows.generateWithAISuggestion3', "When someone messages 'price', reply with our pricing and ask if they want a demo"),
])

// Pagination state
const currentPage = ref(1)
const totalItems = ref(0)
const pageSize = 20

const columns = computed<Column<ChatbotFlow>[]>(() => [
  { key: 'name', label: t('chatbotFlows.name'), sortable: true },
  { key: 'whatsapp_account', label: t('chatbotFlows.account'), sortable: true },
  { key: 'description', label: t('chatbotFlows.description') },
  { key: 'trigger_keywords', label: t('chatbotFlows.keywords') },
  { key: 'steps_count', label: t('chatbotFlows.steps'), sortable: true },
  { key: 'status', label: t('chatbotFlows.status'), sortable: true, sortKey: 'enabled' },
  { key: 'is_default', label: t('chatbotFlows.defaultForAccount'), sortable: true },
  { key: 'actions', label: t('chatbotFlows.actions'), align: 'right' },
])

const sortKey = ref('name')
const sortDirection = ref<'asc' | 'desc'>('asc')

onMounted(async () => {
  await fetchFlows()
})

async function fetchFlows() {
  isLoading.value = true
  error.value = null
  try {
    const response = await chatbotService.listFlows({
      search: searchQuery.value || undefined,
      page: currentPage.value,
      limit: pageSize
    })
    const data = (response.data as any).data || response.data
    flows.value = data.flows || []
    totalItems.value = data.total ?? flows.value.length
  } catch (err) {
    console.error('Failed to load flows:', err)
    error.value = t('chatbotFlows.fetchError')
    flows.value = []
  } finally {
    isLoading.value = false
  }
}

// Debounced search
const debouncedSearch = useDebounceFn(() => {
  currentPage.value = 1
  fetchFlows()
}, 300)

watch(searchQuery, () => debouncedSearch())

function handlePageChange(page: number) {
  currentPage.value = page
  fetchFlows()
}

function createFlow() {
  selectedTemplate.value = 'blank'
  templateDialogOpen.value = true
}

function openGenerateAI() {
  generateAIPrompt.value = ''
  generateAIDialogOpen.value = true
}

function applyGenerateAISuggestion(suggestion: string) {
  generateAIPrompt.value = suggestion
}

async function confirmGenerateAI() {
  const prompt = generateAIPrompt.value.trim()
  if (!prompt) {
    toast.error(t('chatbotFlows.generateWithAIPromptRequired', 'Describe the flow you want to generate'))
    return
  }
  isGeneratingAI.value = true
  try {
    const response = await chatbotService.generateFlow({ prompt })
    const data = (response.data as any)?.data || response.data
    if (!data?.graph || data.graph.version !== 2) {
      throw new Error('Invalid graph returned')
    }
    sessionStorage.setItem(
      'whatomate:ai-flow-draft',
      JSON.stringify({
        name: data.name || '',
        description: data.description || '',
        trigger_keywords: data.trigger_keywords || [],
        graph: data.graph,
      }),
    )
    generateAIDialogOpen.value = false
    router.push({ path: '/chatbot/flows/new', query: { ai: '1' } })
  } catch (err: any) {
    toast.error(getErrorMessage(err, t('chatbotFlows.generateWithAIFailed', 'Failed to generate flow. Check AI settings and try again.')))
  } finally {
    isGeneratingAI.value = false
  }
}

function confirmCreateFlow() {
  templateDialogOpen.value = false
  const tpl = selectedTemplate.value
  if (tpl && tpl !== 'blank') {
    router.push({ path: '/chatbot/flows/new', query: { template: tpl } })
  } else {
    router.push('/chatbot/flows/new')
  }
}

function editFlow(flow: ChatbotFlow) {
  router.push(`/chatbot/flows/${flow.id}/edit`)
}

async function toggleFlow(flow: ChatbotFlow) {
  try {
    await chatbotService.updateFlow(flow.id, { enabled: !flow.enabled })
    flow.enabled = !flow.enabled
    toast.success(flow.enabled ? t('common.enabledSuccess', { resource: t('resources.Flow') }) : t('common.disabledSuccess', { resource: t('resources.Flow') }))
  } catch (error: any) {
    toast.error(getErrorMessage(error, t('common.failedToggle', { resource: t('resources.flow') })))
  }
}

async function requestToggleDefault(flow: ChatbotFlow) {
  if (!flow.whatsapp_account) {
    toast.error(t('chatbotFlows.accountRequiredForDefault', 'Assign a WhatsApp account before setting a default flow'))
    return
  }
  const makeDefault = !flow.is_default
  let count = 0
  try {
    const res = await chatbotService.countSessions({
      whatsapp_account: flow.whatsapp_account,
      status: 'active',
    })
    const data = (res.data as any)?.data || res.data
    count = data?.count ?? 0
  } catch {
    count = 0
  }
  defaultConfirmFlow.value = flow
  defaultConfirmMakeDefault.value = makeDefault
  defaultConfirmSessionCount.value = count
  defaultConfirmOpen.value = true
}

async function confirmToggleDefault() {
  const flow = defaultConfirmFlow.value
  if (!flow) return
  const makeDefault = defaultConfirmMakeDefault.value
  isUpdatingDefault.value = true
  try {
    await chatbotService.updateFlow(flow.id, { is_default: makeDefault })
    for (const other of flows.value) {
      if (other.whatsapp_account === flow.whatsapp_account) {
        other.is_default = makeDefault && other.id === flow.id
      }
    }
    toast.success(
      makeDefault
        ? t('chatbotFlows.defaultSetSuccess', 'Default flow updated; active sessions cleared')
        : t('chatbotFlows.defaultClearedSuccess', 'Default flow cleared; active sessions cleared'),
    )
    defaultConfirmOpen.value = false
    defaultConfirmFlow.value = null
  } catch (error: any) {
    toast.error(getErrorMessage(error, t('chatbotFlows.defaultToggleFailed', 'Failed to update default flow')))
  } finally {
    isUpdatingDefault.value = false
  }
}

function cancelToggleDefault() {
  defaultConfirmOpen.value = false
  defaultConfirmFlow.value = null
}

const defaultConfirmDescription = computed(() => {
  const flow = defaultConfirmFlow.value
  if (!flow) return ''
  const count = defaultConfirmSessionCount.value
  const action = defaultConfirmMakeDefault.value
    ? t('chatbotFlows.defaultConfirmSet', 'Set “{name}” as the default flow for {account}?', {
        name: flow.name,
        account: flow.whatsapp_account,
      })
    : t('chatbotFlows.defaultConfirmClear', 'Clear “{name}” as the default flow for {account}?', {
        name: flow.name,
        account: flow.whatsapp_account,
      })
  if (count > 0) {
    return `${action} ${t('chatbotFlows.defaultConfirmSessions', '{count} active session(s) will be cancelled.', { count })}`
  }
  return action
})

function openDeleteDialog(flow: ChatbotFlow) {
  flowToDelete.value = flow
  deleteDialogOpen.value = true
}

async function confirmDeleteFlow() {
  if (!flowToDelete.value) return

  isDeleting.value = true
  try {
    await chatbotService.deleteFlow(flowToDelete.value.id)
    toast.success(t('common.deletedSuccess', { resource: t('resources.Flow') }))
    deleteDialogOpen.value = false
    flowToDelete.value = null
    await fetchFlows()
  } catch (error: any) {
    toast.error(getErrorMessage(error, t('common.failedDelete', { resource: t('resources.flow') })))
  } finally {
    isDeleting.value = false
  }
}
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader
      :title="$t('chatbotFlows.title')"
      :icon="Workflow"
      icon-gradient="bg-gradient-to-br from-purple-500 to-pink-600 shadow-purple-500/20"
      back-link="/chatbot"
      :breadcrumbs="[{ label: $t('chatbotFlows.backToChatbot'), href: '/chatbot' }, { label: $t('nav.flows') }]"
    >
      <template #actions>
        <div class="flex items-center gap-2">
          <button
            type="button"
            class="btn-press relative inline-flex h-8 items-center overflow-hidden rounded-full bg-gradient-to-r from-cyan-400 to-fuchsia-500 p-px text-xs font-medium shadow-[0_0_16px_rgba(34,211,238,0.35),0_0_28px_rgba(168,85,247,0.35)] transition-shadow duration-200 hover:shadow-[0_0_20px_rgba(34,211,238,0.45),0_0_34px_rgba(168,85,247,0.45)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-violet-500/50 focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:pointer-events-none disabled:opacity-50"
            @click="openGenerateAI"
          >
            <span class="inline-flex h-full items-center gap-2 rounded-full bg-[#12121a] px-3 text-xs text-violet-300 light:bg-violet-50 light:text-violet-600">
              <Sparkles class="h-3.5 w-3.5 shrink-0" />
              {{ $t('chatbotFlows.generateWithAI', 'Generate with AI') }}
            </span>
          </button>
          <Button size="sm" @click="createFlow">
            <Plus class="h-4 w-4 mr-2" />
            {{ $t('chatbotFlows.createFlow') }}
          </Button>
        </div>
      </template>
    </PageHeader>

    <ScrollArea class="flex-1">
      <div class="p-6">
        <div>
          <Card>
            <CardHeader>
              <div class="flex items-center justify-between flex-wrap gap-4">
                <div>
                  <CardTitle>{{ $t('chatbotFlows.yourFlows') }}</CardTitle>
                  <CardDescription>{{ $t('chatbotFlows.yourFlowsDesc') }}</CardDescription>
                </div>
                <SearchInput v-model="searchQuery" :placeholder="$t('chatbotFlows.searchFlows') + '...'" class="w-64" />
              </div>
            </CardHeader>
            <CardContent>
              <ErrorState
                v-if="error"
                :title="$t('common.loadErrorTitle')"
                :description="error"
                :retry-label="$t('common.retry')"
                @retry="fetchFlows"
              />
              <DataTable
                v-else
                :items="flows"
                :columns="columns"
                :is-loading="isLoading"
                :empty-icon="Workflow"
                :empty-title="searchQuery ? $t('chatbotFlows.noMatchingFlows') : $t('chatbotFlows.noFlowsYet')"
                :empty-description="searchQuery ? $t('chatbotFlows.noMatchingFlowsDesc') : $t('chatbotFlows.noFlowsYetDesc')"
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
                  <span class="font-medium">{{ flow.name }}</span>
                </template>
                <template #cell-whatsapp_account="{ item: flow }">
                  <span class="text-sm text-muted-foreground">{{ flow.whatsapp_account || '—' }}</span>
                </template>
                <template #cell-description="{ item: flow }">
                  <span class="text-muted-foreground max-w-[200px] truncate block">{{ flow.description || $t('chatbotFlows.noDescription') }}</span>
                </template>
                <template #cell-trigger_keywords="{ item: flow }">
                  <div class="flex flex-wrap gap-1">
                    <Badge v-for="keyword in flow.trigger_keywords?.slice(0, 2)" :key="keyword" variant="secondary" class="text-xs">
                      {{ keyword }}
                    </Badge>
                    <Badge v-if="flow.trigger_keywords?.length > 2" variant="outline" class="text-xs">
                      +{{ flow.trigger_keywords.length - 2 }}
                    </Badge>
                    <span v-if="!flow.trigger_keywords?.length" class="text-muted-foreground text-sm">—</span>
                  </div>
                </template>
                <template #cell-steps_count="{ item: flow }">
                  <span class="text-muted-foreground">{{ flow.steps_count }}</span>
                </template>
                <template #cell-status="{ item: flow }">
                  <div class="flex items-center gap-2">
                    <Switch :checked="flow.enabled" @update:checked="toggleFlow(flow)" />
                    <span class="text-sm text-muted-foreground">{{ flow.enabled ? $t('chatbotFlows.active') : $t('chatbotFlows.inactive') }}</span>
                  </div>
                </template>
                <template #cell-is_default="{ item: flow }">
                  <div class="flex items-center gap-2">
                    <Switch
                      :checked="flow.is_default"
                      :disabled="!flow.whatsapp_account"
                      @update:checked="requestToggleDefault(flow)"
                    />
                    <span class="text-sm text-muted-foreground">
                      {{ flow.is_default ? $t('chatbotFlows.defaultYes') : $t('chatbotFlows.defaultNo') }}
                    </span>
                  </div>
                </template>
                <template #cell-actions="{ item: flow }">
                  <div class="flex items-center justify-end gap-1">
                    <IconButton :icon="Pencil" :label="$t('chatbotFlows.editFlowLabel')" class="h-8 w-8" @click="editFlow(flow)" />
                    <IconButton :icon="Trash2" :label="$t('chatbotFlows.deleteFlowLabel')" class="h-8 w-8 text-destructive" @click="openDeleteDialog(flow)" />
                  </div>
                </template>
                <template #empty-action>
                  <Button v-if="!searchQuery" variant="outline" size="sm" @click="createFlow">
                    <Plus class="h-4 w-4 mr-2" />
                    {{ $t('chatbotFlows.createFlow') }}
                  </Button>
                </template>
              </DataTable>
            </CardContent>
          </Card>
        </div>
      </div>
    </ScrollArea>

    <DeleteConfirmDialog
      v-model:open="deleteDialogOpen"
      :title="$t('chatbotFlows.deleteFlow')"
      :item-name="flowToDelete?.name"
      :is-submitting="isDeleting"
      @confirm="confirmDeleteFlow"
    />

    <ConfirmDialog
      v-model:open="defaultConfirmOpen"
      :title="$t('chatbotFlows.defaultConfirmTitle', 'Change default flow')"
      :description="defaultConfirmDescription"
      :confirm-label="$t('common.confirm', 'Confirm')"
      :is-submitting="isUpdatingDefault"
      @confirm="confirmToggleDefault"
      @cancel="cancelToggleDefault"
    />

    <Dialog v-model:open="templateDialogOpen">
      <DialogContent class="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{{ $t('chatbotFlows.chooseTemplate', 'Choose a starting template') }}</DialogTitle>
          <DialogDescription>
            {{ $t('chatbotFlows.chooseTemplateDesc', 'You can edit every node after creating the flow.') }}
          </DialogDescription>
        </DialogHeader>
        <div class="grid gap-2 py-2">
          <button
            v-for="tpl in flowTemplates"
            :key="tpl.id"
            type="button"
            class="text-left rounded-lg border px-3 py-2 transition-colors"
            :class="selectedTemplate === tpl.id ? 'border-primary bg-primary/5' : 'border-border hover:bg-muted/40'"
            @click="selectedTemplate = tpl.id"
          >
            <div class="font-medium text-sm">{{ tpl.name }}</div>
            <div class="text-xs text-muted-foreground">{{ tpl.description }}</div>
          </button>
        </div>
        <DialogFooter>
          <Button variant="outline" @click="templateDialogOpen = false">{{ $t('common.cancel', 'Cancel') }}</Button>
          <Button @click="confirmCreateFlow">{{ $t('chatbotFlows.createFlow') }}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <Dialog v-model:open="generateAIDialogOpen">
      <DialogContent class="sm:max-w-lg">
        <DialogHeader>
          <div class="flex items-start gap-3">
            <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 light:bg-violet-100">
              <Sparkles class="h-5 w-5 text-primary light:text-violet-600" />
            </div>
            <div class="min-w-0 space-y-1">
              <DialogTitle>{{ $t('chatbotFlows.generateWithAI', 'Generate with AI') }}</DialogTitle>
              <DialogDescription>
                {{ $t('chatbotFlows.generateWithAISubtitle', 'Describe your workflow in plain language') }}
              </DialogDescription>
            </div>
          </div>
        </DialogHeader>

        <div class="space-y-3">
          <Textarea
            v-model="generateAIPrompt"
            rows="5"
            class="min-h-[120px] resize-y"
            :placeholder="$t('chatbotFlows.generateWithAIPlaceholder', 'e.g. When a new contact is added, send a WhatsApp welcome message, wait 1 day, then ask how their experience is going.')"
          />

          <div class="flex flex-col gap-2">
            <button
              v-for="(suggestion, index) in generateAISuggestions"
              :key="index"
              type="button"
              class="rounded-full border border-border bg-background px-3 py-2 text-left text-xs text-muted-foreground transition-colors hover:border-primary/40 hover:bg-muted/40 hover:text-foreground"
              @click="applyGenerateAISuggestion(suggestion)"
            >
              {{ suggestion }}
            </button>
          </div>

          <p class="flex items-start gap-1.5 text-[11px] leading-relaxed text-muted-foreground">
            <Sparkles class="mt-0.5 h-3 w-3 shrink-0 text-primary/70" />
            <span>{{ $t('chatbotFlows.generateWithAIHint', 'AI builds a draft on the canvas. Review each step and Save before activating.') }}</span>
          </p>
        </div>

        <DialogFooter>
          <Button variant="outline" :disabled="isGeneratingAI" @click="generateAIDialogOpen = false">
            {{ $t('common.cancel', 'Cancel') }}
          </Button>
          <Button :disabled="isGeneratingAI || !generateAIPrompt.trim()" @click="confirmGenerateAI">
            <Sparkles v-if="!isGeneratingAI" class="h-4 w-4 mr-2" />
            <span v-if="isGeneratingAI" class="mr-2 inline-block h-4 w-4 animate-spin rounded-full border-2 border-current border-t-transparent" />
            {{ isGeneratingAI
              ? $t('chatbotFlows.generateWithAIGenerating', 'Generating…')
              : $t('chatbotFlows.generateWithAI', 'Generate with AI') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
