<script setup lang="ts">
import { ref, computed, onMounted, watch, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { api, chatbotService, accountsService, type FlowEncryptionStatus } from '@/services/api'
import { toast } from 'vue-sonner'
import { getErrorMessage } from '@/lib/api-utils'
import { getQualityBadgeClass, getQualityRatingLabel, getVerificationBadgeClass, getVerificationStatusLabel, formatLimitTier } from '@/lib/utils'
import { useUnsavedChangesGuard } from '@/composables/useUnsavedChangesGuard'
import DetailPageLayout from '@/components/shared/DetailPageLayout.vue'
import MetadataPanel from '@/components/shared/MetadataPanel.vue'
import AuditLogPanel from '@/components/shared/AuditLogPanel.vue'
import UnsavedChangesDialog from '@/components/shared/UnsavedChangesDialog.vue'
import BusinessProfileDialog from './BusinessProfileDialog.vue'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Separator } from '@/components/ui/separator'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { IconButton } from '@/components/shared'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import {
  Phone,
  Save,
  Trash2,
  Copy,
  RefreshCw,
  Loader2,
  AlertCircle,
  CheckCircle2,
  Bell,
  Store,
  TestTube2,
  Check,
  X,
  KeyRound,
  Eye,
  EyeOff,
} from 'lucide-vue-next'

interface WhatsAppAccount {
  id: string
  name: string
  app_id: string
  phone_id: string
  business_id: string
  webhook_verify_token: string
  api_version: string
  is_default_incoming: boolean
  is_default_outgoing: boolean
  auto_read_receipt: boolean
  business_calling_enabled?: boolean
  ai_responses_enabled?: boolean
  typing_indicator_enabled?: boolean
  default_chatbot_flow_id?: string
  status: string
  has_access_token: boolean
  has_app_secret: boolean
  phone_number?: string
  display_name?: string
  created_by_id?: string
  created_by_name?: string
  updated_by_id?: string
  updated_by_name?: string
  created_at: string
  updated_at: string
}

interface ChatbotFlowOption {
  id: string
  name: string
  enabled: boolean
}

interface TestResult {
  success: boolean
  error?: string
  display_phone_number?: string
  verified_name?: string
  quality_rating?: string
  messaging_limit_tier?: string
  code_verification_status?: string
  account_mode?: string
  is_test_number?: boolean
  warning?: string
}

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const authStore = useAuthStore()

const accountId = computed(() => route.params.id as string)
const isNew = computed(() => accountId.value === 'new')
const account = ref<WhatsAppAccount | null>(null)
const isLoading = ref(true)
const isNotFound = ref(false)
const isSaving = ref(false)
const hasChanges = ref(false)
const deleteDialogOpen = ref(false)
const testResult = ref<TestResult | null>(null)
const testingConnection = ref(false)
const subscribing = ref(false)
const isProfileDialogOpen = ref(false)
const flowEncryption = ref<FlowEncryptionStatus | null>(null)
const flowEncryptionLoading = ref(false)
const flowEncryptionBusy = ref(false)
const rotateKeyDialogOpen = ref(false)
const revealKeyDialogOpen = ref(false)
const flowPrivateKey = ref('')
const revealingPrivateKey = ref(false)

const flowKeyOnMeta = computed(() =>
  !!flowEncryption.value?.configured &&
  flowEncryption.value.meta_key_matches &&
  flowEncryption.value.meta_status !== 'MISMATCH',
)

const { showLeaveDialog, confirmLeave, cancelLeave } = useUnsavedChangesGuard(hasChanges)

const canWrite = computed(() => authStore.hasPermission('accounts', 'write'))
const canDelete = computed(() => authStore.hasPermission('accounts', 'delete'))

const form = ref({
  name: '',
  app_id: '',
  phone_id: '',
  business_id: '',
  access_token: '',
  app_secret: '',
  webhook_verify_token: '',
  api_version: 'v21.0',
  is_default_incoming: false,
  is_default_outgoing: false,
  auto_read_receipt: false,
  business_calling_enabled: false,
  ai_responses_enabled: false,
  typing_indicator_enabled: false,
  default_chatbot_flow_id: '',
})

const chatbotFlows = ref<ChatbotFlowOption[]>([])
const NONE_CHAT_FLOW = 'none'

const breadcrumbs = computed(() => [
  { label: t('nav.settings'), href: '/settings' },
  { label: t('settings.accounts'), href: '/settings/accounts' },
  { label: isNew.value ? t('accounts.newAccount', 'New Account') : (account.value?.name || '') },
])

const basePath = ((window as any).__BASE_PATH__ ?? '').replace(/\/$/, '')
const webhookUrl = window.location.origin + basePath + '/api/webhook'

// Track form changes
watch(form, () => { hasChanges.value = true }, { deep: true })

async function loadAccount() {
  isLoading.value = true
  isNotFound.value = false
  try {
    const response = await api.get(`/accounts/${accountId.value}`)
    const data = response.data.data || response.data
    account.value = data
    syncForm()
    nextTick(() => { hasChanges.value = false })
  } catch {
    isNotFound.value = true
  } finally {
    isLoading.value = false
  }
}

function syncForm() {
  if (!account.value) return
  form.value = {
    name: account.value.name,
    app_id: account.value.app_id || '',
    phone_id: account.value.phone_id,
    business_id: account.value.business_id,
    access_token: '',
    app_secret: '',
    webhook_verify_token: account.value.webhook_verify_token || '',
    api_version: account.value.api_version,
    is_default_incoming: account.value.is_default_incoming,
    is_default_outgoing: account.value.is_default_outgoing,
    auto_read_receipt: account.value.auto_read_receipt,
    business_calling_enabled: account.value.business_calling_enabled ?? false,
    ai_responses_enabled: account.value.ai_responses_enabled ?? false,
    typing_indicator_enabled: account.value.typing_indicator_enabled ?? false,
    default_chatbot_flow_id: account.value.default_chatbot_flow_id || '',
  }
}

async function loadChatbotFlows() {
  try {
    const accountName = account.value?.name || form.value.name
    const response = await chatbotService.listFlows({
      limit: 200,
      whatsapp_account: accountName || undefined,
    })
    const data = (response.data as any).data || response.data
    chatbotFlows.value = data.flows || []
  } catch {
    chatbotFlows.value = []
  }
}

async function save() {
  if (!form.value.name.trim() || !form.value.phone_id.trim() || !form.value.business_id.trim()) {
    toast.error(t('accounts.fillRequired', 'Name, Phone ID, and Business ID are required'))
    return
  }
  if (isNew.value && !form.value.access_token.trim()) {
    toast.error(t('accounts.accessTokenRequired', 'Access token is required'))
    return
  }

  isSaving.value = true
  try {
    const payload: any = { ...form.value }
    if (!isNew.value && !payload.access_token) delete payload.access_token
    if (!isNew.value && !payload.app_secret) delete payload.app_secret

    if (isNew.value) {
      const response = await api.post('/accounts', payload)
      const created = response.data.data || response.data
      hasChanges.value = false
      toast.success(t('common.createdSuccess', { resource: t('resources.Account') }))
      router.replace(`/settings/accounts/${created.id}`)
    } else {
      await api.put(`/accounts/${account.value!.id}`, payload)
      await loadAccount()
      hasChanges.value = false
      toast.success(t('common.updatedSuccess', { resource: t('resources.Account') }))
    }
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedSave', { resource: t('resources.account') })))
  } finally {
    isSaving.value = false
  }
}

async function deleteAccount() {
  if (!account.value) return
  try {
    await api.delete(`/accounts/${account.value.id}`)
    toast.success(t('common.deletedSuccess', { resource: t('resources.Account') }))
    router.push('/settings/accounts')
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedDelete', { resource: t('resources.account') })))
  }
  deleteDialogOpen.value = false
}

async function testConnection() {
  if (!account.value) return
  testingConnection.value = true
  try {
    const response = await api.post(`/accounts/${account.value.id}/test`)
    testResult.value = response.data.data
    if (testResult.value?.success) {
      toast.success(t('accounts.connectionSuccess', 'Connection successful'))
    } else {
      toast.error(t('accounts.connectionFailed', 'Connection failed') + ': ' + (testResult.value?.error || ''))
    }
  } catch (e) {
    testResult.value = { success: false, error: getErrorMessage(e, t('accounts.connectionTestFailed', 'Test failed')) }
    toast.error(testResult.value.error!)
  } finally {
    testingConnection.value = false
  }
}

async function subscribeApp() {
  if (!account.value) return
  subscribing.value = true
  try {
    const response = await api.post(`/accounts/${account.value.id}/subscribe`)
    if (response.data.data?.success) {
      toast.success(t('accounts.subscribeSuccess', 'Subscribed successfully'))
    } else {
      toast.error(t('accounts.subscribeFailed', 'Subscribe failed'))
    }
  } catch (e) {
    toast.error(getErrorMessage(e, t('accounts.subscribeError', 'Subscribe error')))
  } finally {
    subscribing.value = false
  }
}

async function copyToClipboard(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast.success(t('common.copiedToClipboard', 'Copied'))
  } catch {
    toast.error(t('common.clipboardFailed', 'Failed to copy'))
  }
}

async function loadFlowEncryption() {
  if (!account.value) return
  flowEncryptionLoading.value = true
  try {
    const response = await accountsService.getFlowEncryption(account.value.id)
    flowEncryption.value = response.data.data
  } catch {
    flowEncryption.value = null
  } finally {
    flowEncryptionLoading.value = false
  }
}

async function setupFlowEncryption(rotate: boolean) {
  if (!account.value) return
  rotateKeyDialogOpen.value = false
  flowEncryptionBusy.value = true
  try {
    const response = await accountsService.setupFlowEncryption(account.value.id, rotate)
    flowEncryption.value = response.data.data
    if (rotate) flowPrivateKey.value = ''
    toast.success(rotate
      ? t('accounts.flowKeyRotated', 'New key generated and uploaded to Meta')
      : t('accounts.flowKeyUploaded', 'Public key uploaded to Meta'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('accounts.flowKeyFailed', 'Failed to set up Flow encryption')))
    await loadFlowEncryption()
  } finally {
    flowEncryptionBusy.value = false
  }
}

async function revealFlowPrivateKey() {
  if (!account.value) return
  revealKeyDialogOpen.value = false
  revealingPrivateKey.value = true
  try {
    const response = await accountsService.revealFlowPrivateKey(account.value.id)
    flowPrivateKey.value = response.data.data.private_key
  } catch (e) {
    toast.error(getErrorMessage(e, t('accounts.flowPrivateKeyFailed', 'Failed to load the private key')))
  } finally {
    revealingPrivateKey.value = false
  }
}

function setDefaultChatbotFlow(value: unknown) {
  const selected = String(value ?? '')
  form.value.default_chatbot_flow_id = selected === NONE_CHAT_FLOW ? '' : selected
}

onMounted(async () => {
  if (isNew.value) {
    await loadChatbotFlows()
    isLoading.value = false
    hasChanges.value = false
  } else {
    await loadAccount()
    await Promise.all([loadChatbotFlows(), loadFlowEncryption()])
  }
})
</script>

<template>
  <div class="h-full">
  <DetailPageLayout
    :title="isNew ? $t('accounts.newAccount', 'New Account') : (account?.name || '')"
    :icon="Phone"
    icon-gradient="bg-gradient-to-br from-emerald-500 to-green-600 shadow-emerald-500/20"
    back-link="/settings/accounts"
    :breadcrumbs="breadcrumbs"
    :is-loading="isLoading"
    :is-not-found="isNotFound"
  >
    <template #actions>
      <div class="flex items-center gap-2">
        <Button v-if="!isNew && account" variant="outline" size="sm" :disabled="testingConnection" @click="testConnection">
          <Loader2 v-if="testingConnection" class="h-4 w-4 animate-spin mr-1" />
          <RefreshCw v-else class="h-4 w-4 mr-1" />
          {{ $t('accounts.test', 'Test') }}
        </Button>
        <Button v-if="!isNew && account" variant="outline" size="sm" :disabled="subscribing" @click="subscribeApp">
          <Loader2 v-if="subscribing" class="h-4 w-4 animate-spin mr-1" />
          <Bell v-else class="h-4 w-4 mr-1" />
          {{ $t('accounts.subscribe', 'Subscribe') }}
        </Button>
        <Button v-if="!isNew && account" variant="outline" size="sm" @click="isProfileDialogOpen = true">
          <Store class="h-4 w-4 mr-1" />
          {{ $t('accounts.businessProfile', 'Profile') }}
        </Button>
        <Button v-if="canWrite && (hasChanges || isNew)" size="sm" @click="save" :disabled="isSaving">
          <Save class="h-4 w-4 mr-1" /> {{ isSaving ? $t('common.saving', 'Saving...') : isNew ? $t('common.create') : $t('common.save') }}
        </Button>
        <Button v-if="canDelete && !isNew" variant="destructive" size="sm" @click="deleteDialogOpen = true">
          <Trash2 class="h-4 w-4 mr-1" /> {{ $t('common.delete') }}
        </Button>
      </div>
    </template>

    <!-- Test Result -->
    <Card v-if="testResult">
      <CardContent class="p-4">
        <div v-if="testResult.success" class="space-y-2">
          <div class="flex items-center gap-2 text-green-400 light:text-green-600">
            <CheckCircle2 class="h-4 w-4" />
            <span class="text-sm font-medium">{{ $t('accounts.connected', 'Connected') }}</span>
            <span v-if="testResult.display_phone_number" class="text-sm text-muted-foreground">— {{ testResult.display_phone_number }}</span>
            <Badge v-if="testResult.is_test_number" variant="outline" class="border-amber-600 text-amber-600">
              <TestTube2 class="h-3 w-3 mr-1" /> {{ $t('accounts.testNumber', 'Test Number') }}
            </Badge>
          </div>
          <div v-if="testResult.warning" class="flex items-start gap-2 p-2 rounded-lg bg-amber-950/50 light:bg-amber-50 border border-amber-800 light:border-amber-200">
            <AlertCircle class="h-4 w-4 text-amber-400 light:text-amber-600 mt-0.5 shrink-0" />
            <span class="text-sm text-amber-300 light:text-amber-700">{{ testResult.warning }}</span>
          </div>
          <!-- Meta Connection Details -->
          <div class="mt-4 border-t pt-4 border-border/40">
            <h4 class="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-3">
              {{ $t('accounts.metaConnectionDetails', 'Details') }}
            </h4>
            <div class="grid grid-cols-2 md:grid-cols-4 gap-4 bg-muted/20 p-3 rounded-lg border border-border/30">
              <!-- Verified Name -->
              <div v-if="testResult.verified_name" class="space-y-1">
                <span class="text-[10px] text-muted-foreground block font-medium">{{ $t('accounts.verifiedName', 'Verified Name') }}</span>
                <span class="text-sm font-semibold block text-foreground truncate" :title="testResult.verified_name">{{ testResult.verified_name }}</span>
              </div>
               <!-- Quality Rating -->
              <div class="space-y-1">
                <span class="text-[10px] text-muted-foreground block font-medium">{{ $t('accounts.qualityRating', 'Quality Rating') }}</span>
                <Badge :class="getQualityBadgeClass(testResult.quality_rating || '')">
                  {{ getQualityRatingLabel(testResult.quality_rating || '', t) }}
                </Badge>
              </div>
              <!-- Messaging Limit Tier -->
              <div class="space-y-1">
                <span class="text-[10px] text-muted-foreground block font-medium">{{ $t('accounts.messagingLimitTier', 'Messaging Limit') }}</span>
                <span class="text-sm font-semibold block text-foreground">
                  {{ formatLimitTier(testResult.messaging_limit_tier, testResult.is_test_number || testResult.account_mode === 'SANDBOX', t) }}
                </span>
              </div>
              <!-- Verification Status -->
              <div v-if="testResult.code_verification_status" class="space-y-1">
                <span class="text-[10px] text-muted-foreground block font-medium">{{ $t('accounts.codeVerificationStatus', 'Verification Status') }}</span>
                <Badge :class="getVerificationBadgeClass(testResult.code_verification_status)">
                  {{ getVerificationStatusLabel(testResult.code_verification_status, t) }}
                </Badge>
              </div>
            </div>
          </div>
        </div>
        <div v-else class="flex items-center gap-2 text-red-400 light:text-red-600">
          <X class="h-4 w-4" />
          <span class="text-sm">{{ testResult.error }}</span>
        </div>
      </CardContent>
    </Card>

    <!-- Account Details Card -->
    <Card>
      <CardHeader class="pb-3">
        <div class="flex items-center justify-between">
          <CardTitle class="text-sm font-medium">{{ $t('accounts.accountDetails', 'Account Details') }}</CardTitle>
          <Badge v-if="account" :variant="account.status === 'active' ? 'default' : 'secondary'">
            {{ account.status }}
          </Badge>
        </div>
      </CardHeader>
      <CardContent class="space-y-4">
        <div class="space-y-1.5">
          <Label class="text-xs">{{ $t('accounts.accountName', 'Account Name') }} *</Label>
          <Input v-model="form.name" :disabled="!canWrite" />
        </div>

        <Separator />

        <div class="grid grid-cols-2 gap-4">
          <div class="space-y-1.5">
            <Label class="text-xs">{{ $t('accounts.metaAppId', 'Meta App ID') }}</Label>
            <Input v-model="form.app_id" :disabled="!canWrite" />
          </div>
          <div class="space-y-1.5">
            <Label class="text-xs">{{ $t('accounts.phoneNumberId', 'Phone Number ID') }} *</Label>
            <Input v-model="form.phone_id" :disabled="!canWrite" />
          </div>
          <div class="space-y-1.5">
            <Label class="text-xs">{{ $t('accounts.businessAccountId', 'Business Account ID') }} *</Label>
            <Input v-model="form.business_id" :disabled="!canWrite" />
          </div>
          <div class="space-y-1.5">
            <Label class="text-xs">{{ $t('accounts.apiVersion', 'API Version') }}</Label>
            <Input v-model="form.api_version" :disabled="!canWrite" />
          </div>
        </div>

        <Separator />

        <div class="grid grid-cols-2 gap-4">
          <div class="space-y-1.5">
            <Label class="text-xs">
              {{ $t('accounts.accessToken', 'Access Token') }}
              <span v-if="isNew" class="text-destructive">*</span>
              <span v-else class="text-muted-foreground text-[10px]">{{ $t('accounts.accessTokenKeepExisting', '(leave empty to keep existing)') }}</span>
            </Label>
            <Input v-model="form.access_token" type="password" :disabled="!canWrite" />
            <Badge v-if="account?.has_access_token" variant="outline" class="border-green-600 text-green-600">
              <Check class="h-3 w-3 mr-1" /> {{ $t('accounts.configured', 'Configured') }}
            </Badge>
          </div>
          <div class="space-y-1.5">
            <Label class="text-xs">
              {{ $t('accounts.appSecret', 'App Secret') }}
              <span v-if="!isNew" class="text-muted-foreground text-[10px]">{{ $t('accounts.accessTokenKeepExisting', '(leave empty to keep existing)') }}</span>
            </Label>
            <Input v-model="form.app_secret" type="password" :disabled="!canWrite" />
            <Badge v-if="account?.has_app_secret" variant="outline" class="border-green-600 text-green-600">
              <Check class="h-3 w-3 mr-1" /> {{ $t('accounts.configured', 'Configured') }}
            </Badge>
          </div>
        </div>

        <template v-if="chatbotFlows.length > 0">
          <Separator />

          <div class="space-y-1.5">
            <Label class="text-xs">{{ $t('accounts.defaultChatbotFlow', 'Default Chat Flow') }}</Label>
            <Select
              :model-value="form.default_chatbot_flow_id || NONE_CHAT_FLOW"
              :disabled="!canWrite"
              @update:model-value="setDefaultChatbotFlow"
            >
              <SelectTrigger>
                <SelectValue :placeholder="$t('accounts.selectChatFlow', 'Select a chat flow')" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem :value="NONE_CHAT_FLOW">{{ $t('accounts.noDefaultChatFlow', 'None') }}</SelectItem>
                <SelectItem v-for="flow in chatbotFlows" :key="flow.id" :value="flow.id">
                  {{ flow.name }}<span v-if="!flow.enabled" class="text-muted-foreground"> ({{ $t('common.disabled', 'disabled') }})</span>
                </SelectItem>
              </SelectContent>
            </Select>
            <p class="text-[11px] text-muted-foreground">
              {{ $t('accounts.defaultChatbotFlowDesc', 'Inbound messages that do not match another flow’s trigger keywords start this flow.') }}
            </p>
          </div>
        </template>

        <Separator />

        <div class="space-y-3">
          <div class="flex items-center justify-between">
            <Label class="text-xs">{{ $t('accounts.defaultIncoming', 'Default for Incoming') }}</Label>
            <Switch :checked="form.is_default_incoming" @update:checked="form.is_default_incoming = $event" :disabled="!canWrite" />
          </div>
          <div class="flex items-center justify-between">
            <Label class="text-xs">{{ $t('accounts.defaultOutgoing', 'Default for Outgoing') }}</Label>
            <Switch :checked="form.is_default_outgoing" @update:checked="form.is_default_outgoing = $event" :disabled="!canWrite" />
          </div>
          <div class="flex items-center justify-between">
            <Label class="text-xs">{{ $t('accounts.autoReadReceipt', 'Auto Read Receipt') }}</Label>
            <Switch :checked="form.auto_read_receipt" @update:checked="form.auto_read_receipt = $event" :disabled="!canWrite" />
          </div>
          <div class="flex items-start justify-between gap-3">
            <div class="space-y-0.5">
              <Label class="text-xs">{{ $t('accounts.aiResponsesEnabled', 'AI Responses') }}</Label>
              <p class="text-[11px] text-muted-foreground">
                {{ $t('accounts.aiResponsesEnabledDesc', 'Use AI to reply when no keyword or flow matches. Requires AI to be configured under Settings → AI.') }}
              </p>
            </div>
            <Switch :checked="form.ai_responses_enabled" @update:checked="form.ai_responses_enabled = $event" :disabled="!canWrite" />
          </div>
          <div class="flex items-start justify-between gap-3">
            <div class="space-y-0.5">
              <Label class="text-xs">{{ $t('accounts.typingIndicatorEnabled', 'Typing indicator') }}</Label>
              <p class="text-[11px] text-muted-foreground">
                {{ $t('accounts.typingIndicatorEnabledDesc', 'Show typing in WhatsApp before chatbot replies.') }}
              </p>
            </div>
            <Switch :checked="form.typing_indicator_enabled" @update:checked="form.typing_indicator_enabled = $event" :disabled="!canWrite" />
          </div>
          <div class="flex items-start justify-between gap-3">
            <div class="space-y-0.5">
              <Label class="text-xs">{{ $t('accounts.businessCallingEnabled', 'Business Calling enabled') }}</Label>
              <p class="text-[11px] text-muted-foreground">
                {{ $t('accounts.businessCallingEnabledDesc', 'Enable only after Meta enrolls this number in the WhatsApp Business Calling API. Required for click-to-call buttons in canned responses.') }}
              </p>
            </div>
            <Switch :checked="form.business_calling_enabled" @update:checked="form.business_calling_enabled = $event" :disabled="!canWrite" />
          </div>
        </div>
      </CardContent>
    </Card>

    <!-- Webhook Config Card -->
    <Card v-if="!isNew">
      <CardHeader class="pb-3">
        <CardTitle class="text-sm font-medium">{{ $t('accounts.webhookConfig', 'Webhook Configuration') }}</CardTitle>
      </CardHeader>
      <CardContent class="space-y-3">
        <div>
          <Label class="text-xs text-muted-foreground">{{ $t('accounts.webhookUrl', 'Webhook URL') }}</Label>
          <div class="flex items-center gap-2 mt-1">
            <code class="px-2 py-1 bg-muted rounded text-xs font-mono flex-1 truncate">{{ webhookUrl }}</code>
            <IconButton :icon="Copy" label="Copy" @click="copyToClipboard(webhookUrl)" />
          </div>
        </div>
        <div>
          <Label class="text-xs text-muted-foreground">{{ $t('accounts.verifyToken', 'Verify Token') }}</Label>
          <div class="flex items-center gap-2 mt-1">
            <code class="px-2 py-1 bg-muted rounded text-xs font-mono flex-1 truncate">{{ account?.webhook_verify_token }}</code>
            <IconButton :icon="Copy" label="Copy" @click="copyToClipboard(account?.webhook_verify_token || '')" />
          </div>
        </div>
      </CardContent>
    </Card>

    <!-- Flow Endpoint Encryption Card -->
    <Card v-if="!isNew && account">
      <CardHeader class="pb-3">
        <div class="flex items-center justify-between gap-2">
          <CardTitle class="text-sm font-medium flex items-center gap-2">
            <KeyRound class="h-4 w-4" />
            {{ $t('accounts.flowEncryption', 'Flow endpoint encryption') }}
          </CardTitle>
          <Loader2 v-if="flowEncryptionLoading" class="h-4 w-4 animate-spin text-muted-foreground" />
          <template v-else-if="flowEncryption">
            <Badge v-if="!flowEncryption.configured" variant="secondary">
              {{ $t('accounts.flowKeyNotSetUp', 'Not set up') }}
            </Badge>
            <Badge v-else-if="flowKeyOnMeta" variant="outline" class="border-green-600 text-green-600">
              <Check class="h-3 w-3 mr-1" /> {{ $t('accounts.flowKeyOnMeta', 'Uploaded to Meta') }}
            </Badge>
            <Badge v-else variant="outline" class="border-amber-600 text-amber-600">
              <AlertCircle class="h-3 w-3 mr-1" /> {{ $t('accounts.flowKeyNotOnMeta', 'Not on Meta') }}
            </Badge>
          </template>
        </div>
      </CardHeader>
      <CardContent class="space-y-3">
        <p class="text-[11px] text-muted-foreground">
          {{ $t('accounts.flowEncryptionDesc', 'WhatsApp Flows with a data endpoint encrypt their traffic with this key pair. The private key stays on this server; the public key is uploaded to Meta for this phone number.') }}
        </p>

        <div
          v-if="flowEncryption?.configured && !flowKeyOnMeta"
          class="flex items-start gap-2 p-2 rounded-lg bg-amber-950/50 light:bg-amber-50 border border-amber-800 light:border-amber-200"
        >
          <AlertCircle class="h-4 w-4 text-amber-400 light:text-amber-600 mt-0.5 shrink-0" />
          <span class="text-xs text-amber-300 light:text-amber-700">
            {{ flowEncryption.meta_status_error
              ? $t('accounts.flowKeyMetaCheckFailed', { error: flowEncryption.meta_status_error })
              : $t('accounts.flowKeyMismatch', 'Meta has a different public key for this number, so endpoint requests will fail. Upload the key again.') }}
          </span>
        </div>

        <details v-if="flowEncryption?.public_key" class="text-xs">
          <summary class="cursor-pointer text-muted-foreground">{{ $t('accounts.flowPublicKey', 'Public key') }}</summary>
          <div class="mt-2 flex items-start gap-2">
            <pre class="px-2 py-1 bg-muted rounded text-[10px] font-mono flex-1 overflow-x-auto whitespace-pre">{{ flowEncryption.public_key }}</pre>
            <IconButton :icon="Copy" label="Copy" @click="copyToClipboard(flowEncryption.public_key || '')" />
          </div>
        </details>

        <div v-if="canWrite && flowEncryption?.configured" class="text-xs space-y-2">
          <div class="flex items-center justify-between gap-2">
            <span class="text-muted-foreground">{{ $t('accounts.flowPrivateKey', 'Private key') }}</span>
            <Button
              v-if="!flowPrivateKey"
              variant="ghost"
              size="sm"
              class="h-7 text-xs"
              :disabled="revealingPrivateKey"
              @click="revealKeyDialogOpen = true"
            >
              <Loader2 v-if="revealingPrivateKey" class="h-3.5 w-3.5 animate-spin mr-1" />
              <Eye v-else class="h-3.5 w-3.5 mr-1" />
              {{ $t('accounts.flowPrivateKeyShow', 'Show private key') }}
            </Button>
            <Button v-else variant="ghost" size="sm" class="h-7 text-xs" @click="flowPrivateKey = ''">
              <EyeOff class="h-3.5 w-3.5 mr-1" />
              {{ $t('accounts.flowPrivateKeyHide', 'Hide') }}
            </Button>
          </div>
          <div v-if="flowPrivateKey" class="flex items-start gap-2">
            <pre class="px-2 py-1 bg-muted rounded text-[10px] font-mono flex-1 overflow-x-auto whitespace-pre">{{ flowPrivateKey }}</pre>
            <IconButton :icon="Copy" label="Copy" @click="copyToClipboard(flowPrivateKey)" />
          </div>
        </div>

        <div v-if="canWrite" class="flex flex-wrap gap-2">
          <Button
            v-if="!flowEncryption?.configured"
            size="sm"
            :disabled="flowEncryptionBusy || flowEncryptionLoading"
            @click="setupFlowEncryption(false)"
          >
            <Loader2 v-if="flowEncryptionBusy" class="h-4 w-4 animate-spin mr-1" />
            <KeyRound v-else class="h-4 w-4 mr-1" />
            {{ $t('accounts.flowKeyGenerate', 'Generate & upload to Meta') }}
          </Button>
          <template v-else>
            <Button
              variant="outline"
              size="sm"
              :disabled="flowEncryptionBusy"
              @click="setupFlowEncryption(false)"
            >
              <Loader2 v-if="flowEncryptionBusy" class="h-4 w-4 animate-spin mr-1" />
              <RefreshCw v-else class="h-4 w-4 mr-1" />
              {{ $t('accounts.flowKeyReupload', 'Upload to Meta again') }}
            </Button>
            <Button
              variant="outline"
              size="sm"
              :disabled="flowEncryptionBusy"
              @click="rotateKeyDialogOpen = true"
            >
              <KeyRound class="h-4 w-4 mr-1" />
              {{ $t('accounts.flowKeyRotate', 'Rotate key') }}
            </Button>
          </template>
        </div>
      </CardContent>
    </Card>

    <!-- Activity Log -->
    <AuditLogPanel
      v-if="account && !isNew"
      resource-type="account"
      :resource-id="account.id"
    />

    <!-- Sidebar -->
    <template #sidebar>
      <MetadataPanel
        v-if="!isNew"
        :created-at="account?.created_at"
        :updated-at="account?.updated_at"
        :created-by-name="account?.created_by_name"
        :updated-by-name="account?.updated_by_name"
      />

      <!-- Setup Guide -->
      <Card>
        <CardHeader class="pb-3">
          <CardTitle class="text-sm font-medium">{{ $t('accounts.setupGuide', 'Setup Guide') }}</CardTitle>
        </CardHeader>
        <CardContent>
          <ol class="list-decimal list-inside space-y-2.5 text-sm text-muted-foreground">
            <li>{{ $t('accounts.setupStep1', 'Go to') }} <a href="https://developers.facebook.com" target="_blank" class="text-primary hover:underline">Meta Developer Console</a> {{ $t('accounts.setupStep1End', 'and create an app') }}</li>
            <li>{{ $t('accounts.setupStep2', 'Add WhatsApp product to your app') }}</li>
            <li>{{ $t('accounts.setupStep3', 'Copy') }} <strong>{{ $t('accounts.setupStep3Bold1', 'Phone Number ID') }}</strong> {{ $t('accounts.setupStep3And', 'and') }} <strong>{{ $t('accounts.setupStep3Bold2', 'Business Account ID') }}</strong></li>
            <li>{{ $t('accounts.setupStep4', 'Generate a permanent token from') }} <a href="https://business.facebook.com/settings/system-users" target="_blank" class="text-primary hover:underline">Business Settings</a></li>
            <li>{{ $t('accounts.setupStep5', 'Configure the webhook URL and verify token in Meta dashboard') }}</li>
            <li>{{ $t('accounts.setupStep6', 'Click Test Connection to verify') }}</li>
          </ol>
        </CardContent>
      </Card>
    </template>
  </DetailPageLayout>

  <!-- Delete Confirmation -->
  <AlertDialog v-model:open="deleteDialogOpen">
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>{{ $t('accounts.deleteAccount', 'Delete Account') }}</AlertDialogTitle>
        <AlertDialogDescription>
          {{ $t('accounts.deleteAccountConfirm', 'Are you sure? This cannot be undone.') }}
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <AlertDialogCancel>{{ $t('common.cancel') }}</AlertDialogCancel>
        <AlertDialogAction @click="deleteAccount">{{ $t('common.delete') }}</AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>

  <!-- Rotate Flow Key Confirmation -->
  <AlertDialog v-model:open="rotateKeyDialogOpen">
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>{{ $t('accounts.flowKeyRotateTitle', 'Rotate Flow encryption key?') }}</AlertDialogTitle>
        <AlertDialogDescription>
          {{ $t('accounts.flowKeyRotateConfirm', 'A new key pair replaces the current one and is uploaded to Meta. Flows already open on customers’ phones may fail until they restart.') }}
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <AlertDialogCancel>{{ $t('common.cancel') }}</AlertDialogCancel>
        <AlertDialogAction @click="setupFlowEncryption(true)">{{ $t('accounts.flowKeyRotate', 'Rotate key') }}</AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>

  <!-- Reveal Flow Private Key Confirmation -->
  <AlertDialog v-model:open="revealKeyDialogOpen">
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>{{ $t('accounts.flowPrivateKeyRevealTitle', 'Show the private key?') }}</AlertDialogTitle>
        <AlertDialogDescription>
          {{ $t('accounts.flowPrivateKeyRevealConfirm', 'Anyone with this key can decrypt Flow traffic for this number. Only copy it somewhere safe, such as your own Flow endpoint server. This is recorded in the activity log.') }}
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <AlertDialogCancel>{{ $t('common.cancel') }}</AlertDialogCancel>
        <AlertDialogAction @click="revealFlowPrivateKey">{{ $t('accounts.flowPrivateKeyShow', 'Show private key') }}</AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>

  <!-- Business Profile -->
  <BusinessProfileDialog
    v-if="account"
    v-model:open="isProfileDialogOpen"
    :account-id="account.id"
    :account-name="account.name"
  />

  <UnsavedChangesDialog :open="showLeaveDialog" @stay="cancelLeave" @leave="confirmLeave" />
  </div>
</template>
