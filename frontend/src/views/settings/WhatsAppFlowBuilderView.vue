<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import FlowBuilder from '@/components/flow-builder/FlowBuilder.vue'
import ErrorState from '@/components/shared/ErrorState.vue'
import { flowsService, accountsService } from '@/services/api'
import { takeAIWhatsAppFlowDraft } from '@/lib/whatsappFlowDraft'
import { toast } from 'vue-sonner'
import { ArrowLeft, Copy, Loader2, Plug, Save, Workflow } from 'lucide-vue-next'
import { getErrorMessage } from '@/lib/api-utils'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

interface Account { id: string; name: string; display_phone_number?: string }

const flowCategories = [
  { value: 'SIGN_UP', label: 'Sign Up' },
  { value: 'SIGN_IN', label: 'Sign In' },
  { value: 'APPOINTMENT_BOOKING', label: 'Appointment Booking' },
  { value: 'LEAD_GENERATION', label: 'Lead Generation' },
  { value: 'CONTACT_US', label: 'Contact Us' },
  { value: 'CUSTOMER_SUPPORT', label: 'Customer Support' },
  { value: 'SURVEY', label: 'Survey' },
  { value: 'OTHER', label: 'Other' },
]

const componentsWithoutId = [
  'TextHeading', 'TextSubheading', 'TextBody', 'TextCaption', 'RichText',
  'TextInput', 'TextArea', 'Dropdown', 'RadioButtonsGroup', 'CheckboxGroup',
  'OptIn', 'DatePicker', 'CalendarPicker', 'PhotoPicker', 'DocumentPicker',
  'Image', 'EmbeddedLink', 'If', 'Switch', 'Footer',
]

const isCreate = computed(() => route.name === 'flow-new' || !route.params.id)
const flowId = computed(() => (typeof route.params.id === 'string' ? route.params.id : ''))

const accounts = ref<Account[]>([])
const loading = ref(!isCreate.value)
const loadError = ref(false)
const saving = ref(false)
const flowBuilderRef = ref<InstanceType<typeof FlowBuilder> | null>(null)

const form = ref({
  whatsapp_account: '',
  name: '',
  category: '',
  json_version: '6.1',
  endpoint_enabled: false,
  endpoint_mode: 'builtin' as 'builtin' | 'proxy',
  endpoint_proxy_url: '',
})
const metaFlowId = ref('')
const accountLabel = ref('')
/** URL Meta calls for this flow, and the one last registered with Meta. */
const endpointUrl = ref('')
const endpointUri = ref('')

const currentAccount = computed(() => accounts.value.find(a => a.name === form.value.whatsapp_account))

function applyEndpointFields(flow: any) {
  form.value.endpoint_enabled = !!flow.endpoint_enabled
  form.value.endpoint_mode = flow.endpoint_mode === 'proxy' ? 'proxy' : 'builtin'
  form.value.endpoint_proxy_url = flow.endpoint_proxy_url || ''
  endpointUrl.value = flow.endpoint_url || ''
  endpointUri.value = flow.endpoint_uri || ''
}

function endpointPayload() {
  return {
    endpoint_enabled: form.value.endpoint_enabled,
    endpoint_mode: form.value.endpoint_mode,
    endpoint_proxy_url: form.value.endpoint_mode === 'proxy' ? form.value.endpoint_proxy_url.trim() : '',
  }
}

async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast.success(t('common.copiedToClipboard', 'Copied'))
  } catch {
    toast.error(t('common.clipboardFailed', 'Failed to copy'))
  }
}
const builderData = ref<{ screens: any[] }>({ screens: [] })

onMounted(async () => {
  await fetchAccounts()
  if (isCreate.value) {
    const qAccount = typeof route.query.account === 'string' ? route.query.account : ''
    form.value.whatsapp_account =
      qAccount ||
      (localStorage.getItem('flows_selected_account') !== 'all'
        ? localStorage.getItem('flows_selected_account') || ''
        : '') ||
      accounts.value[0]?.name ||
      ''
    if (route.query.ai === '1') {
      const draft = takeAIWhatsAppFlowDraft()
      if (draft) {
        form.value.name = draft.name
        if (flowCategories.some(c => c.value === draft.category)) form.value.category = draft.category
        builderData.value = { screens: draft.screens }
        toast.success(t('flows.generateWithAIReady'))
      }
    }
    loading.value = false
    return
  }
  await loadFlow()
})

watch(flowId, async (id, prev) => {
  if (!id || id === prev || isCreate.value) return
  accountLabel.value = form.value.whatsapp_account || accountLabel.value
  // Fresh navigation to another flow (not create→edit handoff with same in-memory screens)
  if (prev && prev !== id) {
    await loadFlow()
  }
})

async function fetchAccounts() {
  try {
    const response = await accountsService.list()
    accounts.value = response.data.data?.accounts || []
  } catch {
    accounts.value = []
  }
}

async function loadFlow() {
  loading.value = true
  loadError.value = false
  try {
    const response = await flowsService.get(flowId.value)
    const flow = (response.data as any).data?.flow || (response.data as any).flow
    if (!flow) throw new Error('Flow not found')
    form.value = {
      ...form.value,
      whatsapp_account: flow.whatsapp_account || '',
      name: flow.name || '',
      category: flow.category || '',
      json_version: flow.json_version || '6.0',
    }
    applyEndpointFields(flow)
    metaFlowId.value = flow.meta_flow_id || ''
    accountLabel.value = flow.whatsapp_account || ''
    builderData.value = { screens: Array.isArray(flow.screens) ? flow.screens : [] }
  } catch {
    loadError.value = true
  } finally {
    loading.value = false
  }
}

function sanitizeScreensForMeta(screens: any[]): any[] {
  if (flowBuilderRef.value?.sanitizeFlowForMeta) {
    return flowBuilderRef.value.sanitizeFlowForMeta({ screens }).screens
  }
  const stripImage = (src: string) => {
    const match = src.match(/^data:image\/[^;]+;base64,(.+)$/s)
    return match ? match[1] : src
  }
  const sanitizeComp = (comp: any): any => {
    const { id, ...rest } = comp
    const cleaned: Record<string, any> = {}
    for (const [k, v] of Object.entries(rest)) {
      if (v !== undefined && v !== '') cleaned[k] = v
    }
    if (comp.type === 'Image' && typeof cleaned.src === 'string') {
      cleaned.src = stripImage(cleaned.src)
    }
    if (comp.type === 'If') {
      cleaned.then = (comp.then || []).map(sanitizeComp)
      if (comp.else?.length) cleaned.else = comp.else.map(sanitizeComp)
      else delete cleaned.else
    }
    if (comp.type === 'Switch') {
      const cases: Record<string, any[]> = {}
      for (const [key, children] of Object.entries(comp.cases || {})) {
        cases[key] = (Array.isArray(children) ? children : []).map(sanitizeComp)
      }
      cleaned.cases = cases
    }
    return componentsWithoutId.includes(comp.type) ? cleaned : { id, ...cleaned }
  }
  return screens.map(screen => ({
    id: screen.id,
    title: screen.title,
    data: screen.data || {},
    layout: {
      type: screen.layout?.type || 'SingleColumnLayout',
      children: (screen.layout?.children || []).map(sanitizeComp),
    },
  }))
}

function goBack() {
  router.push('/flows')
}

async function save() {
  if (!form.value.name.trim()) {
    toast.error(t('flows.enterFlowName'))
    return
  }
  if (isCreate.value && !form.value.whatsapp_account) {
    toast.error(t('flows.selectAccountRequired'))
    return
  }
  if (form.value.endpoint_enabled && form.value.endpoint_mode === 'proxy' && !form.value.endpoint_proxy_url.trim()) {
    toast.error(t('flows.endpointProxyUrlRequired', 'Enter the URL to forward endpoint requests to'))
    return
  }

  saving.value = true
  try {
    const sanitizedScreens = builderData.value.screens.length > 0
      ? sanitizeScreensForMeta(builderData.value.screens)
      : []

    if (isCreate.value) {
      const payload: any = {
        whatsapp_account: form.value.whatsapp_account,
        name: form.value.name.trim(),
        category: form.value.category || undefined,
        json_version: form.value.json_version,
        ...endpointPayload(),
      }
      if (sanitizedScreens.length > 0) {
        payload.flow_json = { version: form.value.json_version, screens: sanitizedScreens }
        payload.screens = sanitizedScreens
      }
      const response = await flowsService.create(payload)
      const created = (response.data as any).data?.flow || (response.data as any).flow
      toast.success(t('common.createdSuccess', { resource: t('resources.Flow') }))
      if (created?.id) {
        accountLabel.value = form.value.whatsapp_account
        metaFlowId.value = created.meta_flow_id || ''
        await router.replace(`/flows/${created.id}/edit`)
      } else {
        goBack()
      }
    } else {
      const payload: any = {
        name: form.value.name.trim(),
        category: form.value.category || undefined,
        json_version: form.value.json_version,
        ...endpointPayload(),
      }
      if (sanitizedScreens.length > 0) {
        payload.flow_json = { version: form.value.json_version, screens: sanitizedScreens }
        payload.screens = sanitizedScreens
      }
      const response = await flowsService.update(flowId.value, payload)
      const updated = (response.data as any).data?.flow || (response.data as any).flow
      if (updated) applyEndpointFields(updated)
      toast.success(t('common.updatedSuccess', { resource: t('resources.Flow') }))
    }
  } catch (e) {
    toast.error(getErrorMessage(
      e,
      isCreate.value
        ? t('common.failedCreate', { resource: t('resources.flow') })
        : t('common.failedUpdate', { resource: t('resources.flow') }),
    ))
  } finally {
    saving.value = false
  }
}

function accountDisplay(account: Account) {
  if (account.display_phone_number) {
    return `${account.name} (${account.display_phone_number})`
  }
  return account.name
}
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <div class="flex-shrink-0 border-b border-white/[0.08] light:border-gray-200 px-4 py-3">
      <div class="flex items-center gap-3 flex-wrap">
        <Button variant="ghost" size="icon" class="h-8 w-8" @click="goBack">
          <ArrowLeft class="h-4 w-4" />
        </Button>
        <div class="flex items-center gap-2 min-w-0">
          <div class="h-8 w-8 rounded-lg bg-gradient-to-br from-violet-500 to-purple-600 shadow-violet-500/20 flex items-center justify-center flex-shrink-0">
            <Workflow class="h-4 w-4 text-white" />
          </div>
          <div class="min-w-0">
            <h1 class="text-sm font-semibold truncate">
              {{ isCreate ? $t('flows.createWhatsAppFlow') : $t('flows.editWhatsAppFlow') }}
            </h1>
            <p class="text-xs text-muted-foreground truncate">
              {{ isCreate ? $t('flows.createFlowDesc') : $t('flows.editFlowDesc') }}
            </p>
          </div>
        </div>

        <div class="flex items-center gap-3 flex-wrap flex-1 justify-end ml-auto">
          <div v-if="isCreate" class="flex items-center gap-2">
            <Label class="text-xs whitespace-nowrap text-muted-foreground">{{ $t('flows.account') }}</Label>
            <Select v-model="form.whatsapp_account" :disabled="saving">
              <SelectTrigger class="w-[200px] h-8">
                <SelectValue :placeholder="$t('flows.selectAccount')" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem v-for="account in accounts" :key="account.id" :value="account.name">
                  {{ accountDisplay(account) }}
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div v-else class="flex items-center gap-2">
            <Label class="text-xs whitespace-nowrap text-muted-foreground">{{ $t('flows.account') }}</Label>
            <span class="text-sm text-muted-foreground">{{ accountLabel || form.whatsapp_account }}</span>
            <Badge v-if="metaFlowId" variant="outline" class="text-xs">Meta ID: {{ metaFlowId }}</Badge>
          </div>

          <div class="flex items-center gap-2">
            <Label class="text-xs whitespace-nowrap text-muted-foreground">{{ $t('flows.name') }}</Label>
            <Input
              v-model="form.name"
              :placeholder="$t('flows.flowName')"
              class="w-48 h-8"
              :disabled="saving || loading"
            />
          </div>

          <div class="flex items-center gap-2">
            <Label class="text-xs whitespace-nowrap text-muted-foreground">{{ $t('flows.category') }}</Label>
            <Select v-model="form.category" :disabled="saving || loading">
              <SelectTrigger class="w-[180px] h-8">
                <SelectValue :placeholder="$t('flows.selectCategory')" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem v-for="cat in flowCategories" :key="cat.value" :value="cat.value">
                  {{ cat.label }}
                </SelectItem>
              </SelectContent>
            </Select>
          </div>

          <Popover>
            <PopoverTrigger as-child>
              <Button variant="outline" size="sm" class="h-8" :disabled="loading">
                <Plug class="h-4 w-4 mr-2" />
                {{ $t('flows.endpoint', 'Endpoint') }}
                <Badge
                  :variant="form.endpoint_enabled ? 'default' : 'secondary'"
                  class="ml-2 h-5 px-1.5 text-[10px]"
                >
                  {{ form.endpoint_enabled ? $t('common.on', 'On') : $t('common.off', 'Off') }}
                </Badge>
              </Button>
            </PopoverTrigger>
            <PopoverContent align="end" class="w-96 space-y-4">
              <div class="flex items-start justify-between gap-3">
                <div class="space-y-0.5">
                  <Label class="text-sm">{{ $t('flows.endpointEnabled', 'Data endpoint') }}</Label>
                  <p class="text-[11px] text-muted-foreground">
                    {{ $t('flows.endpointEnabledDesc', 'Needed for Data Exchange buttons, dynamic screens and photo/document uploads.') }}
                  </p>
                </div>
                <Switch
                  :checked="form.endpoint_enabled"
                  :disabled="saving"
                  @update:checked="form.endpoint_enabled = $event"
                />
              </div>

              <template v-if="form.endpoint_enabled">
                <div class="space-y-1.5">
                  <Label class="text-xs">{{ $t('flows.endpointMode', 'Handled by') }}</Label>
                  <Select v-model="form.endpoint_mode" :disabled="saving">
                    <SelectTrigger class="h-8">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="builtin">{{ $t('flows.endpointModeBuiltin', 'Whatomate (built-in)') }}</SelectItem>
                      <SelectItem value="proxy">{{ $t('flows.endpointModeProxy', 'My server (forward requests)') }}</SelectItem>
                    </SelectContent>
                  </Select>
                  <p class="text-[11px] text-muted-foreground">
                    {{ form.endpoint_mode === 'proxy'
                      ? $t('flows.endpointModeProxyDesc', 'Whatomate decrypts each request, posts the JSON to your URL, and encrypts your JSON reply for Meta.')
                      : $t('flows.endpointModeBuiltinDesc', 'Each Data Exchange button goes to the screen you pick, or finishes and hands all answers to the chatbot.') }}
                  </p>
                </div>

                <div v-if="form.endpoint_mode === 'proxy'" class="space-y-1.5">
                  <Label class="text-xs">{{ $t('flows.endpointProxyUrl', 'Forward to URL') }}</Label>
                  <Input
                    v-model="form.endpoint_proxy_url"
                    placeholder="https://example.com/flow-endpoint"
                    class="h-8"
                    :disabled="saving"
                  />
                </div>

                <div class="space-y-1.5">
                  <Label class="text-xs">{{ $t('flows.endpointUrl', 'Endpoint URL') }}</Label>
                  <div v-if="endpointUrl" class="flex items-center gap-2">
                    <code class="px-2 py-1 bg-muted rounded text-[11px] font-mono flex-1 truncate" :title="endpointUrl">{{ endpointUrl }}</code>
                    <Button variant="ghost" size="icon" class="h-7 w-7" @click="copyText(endpointUrl)">
                      <Copy class="h-3.5 w-3.5" />
                    </Button>
                  </div>
                  <p v-else class="text-[11px] text-muted-foreground">
                    {{ $t('flows.endpointUrlAfterSave', 'Shown after you save the flow.') }}
                  </p>
                  <p class="text-[11px] text-muted-foreground">
                    <template v-if="endpointUri">
                      {{ $t('flows.endpointRegistered', 'Registered with Meta:') }} <span class="font-mono break-all">{{ endpointUri }}</span>
                    </template>
                    <template v-else>
                      {{ $t('flows.endpointRegisteredOnMetaSave', 'Registered with Meta when you save the flow to Meta.') }}
                    </template>
                  </p>
                </div>

                <p class="text-[11px] text-muted-foreground border-t pt-3">
                  {{ $t('flows.endpointKeysHint', 'Meta encrypts endpoint traffic with a key pair per WhatsApp account. Set it up under') }}
                  <RouterLink
                    v-if="currentAccount"
                    :to="`/settings/accounts/${currentAccount.id}`"
                    class="text-primary hover:underline"
                  >{{ $t('flows.endpointKeysLink', 'account settings → Flow endpoint encryption') }}</RouterLink>
                  <span v-else>{{ $t('flows.endpointKeysLink', 'account settings → Flow endpoint encryption') }}</span>.
                </p>
              </template>
            </PopoverContent>
          </Popover>

          <Button size="sm" class="h-8" :disabled="saving || loading" @click="save">
            <Loader2 v-if="saving" class="h-4 w-4 mr-2 animate-spin" />
            <Save v-else class="h-4 w-4 mr-2" />
            {{ isCreate ? $t('flows.createFlow') : $t('flows.saveChanges') }}
          </Button>
        </div>
      </div>
    </div>

    <ErrorState
      v-if="loadError && !loading"
      :title="$t('flows.fetchErrorTitle')"
      :description="$t('flows.fetchErrorDescription')"
      :retry-label="$t('common.retry')"
      class="flex-1"
      @retry="loadFlow"
    />

    <div v-else-if="loading" class="flex-1 flex items-center justify-center text-muted-foreground text-sm">
      <Loader2 class="h-5 w-5 mr-2 animate-spin" />
      {{ $t('common.loading') }}
    </div>

    <div v-else class="flex-1 min-h-0 p-4">
      <FlowBuilder ref="flowBuilderRef" v-model="builderData" :endpoint-enabled="form.endpoint_enabled" />
    </div>
  </div>
</template>
