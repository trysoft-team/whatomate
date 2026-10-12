<script setup lang="ts">
import { computed, ref, onMounted, watch } from 'vue'
import type { ChatNode, ContactList, GoogleCredential } from '@/services/api'
import { templatesService, flowsService, usersService, contactListsService, googleCredentialsService, chatbotService } from '@/services/api'
import { useTeamsStore } from '@/stores/teams'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { Trash2, Plus, Copy, Check } from 'lucide-vue-next'

const props = defineProps<{
  node: ChatNode
  currentFlowId?: string
  availableFlows?: { id: string; name: string }[]
}>()

const emit = defineEmits<{
  'update:node': [node: ChatNode]
  'delete': []
  'duplicate': []
}>()

const teamsStore = useTeamsStore()
if (teamsStore.teams.length === 0) teamsStore.fetchTeams()

const approvedTemplates = ref<{ id: string; name: string; language: string; display_name?: string; body_content?: string; status?: string }[]>([])
const publishedWaFlows = ref<{ id: string; name: string; meta_flow_id: string; status: string; screens?: any[] }[]>([])
const orgUsers = ref<{ id: string; full_name?: string; email?: string }[]>([])
const contactLists = ref<ContactList[]>([])
const googleCreds = ref<GoogleCredential[]>([])

async function loadTemplates() {
  try {
    const res = await templatesService.list({ status: 'APPROVED', limit: 200 })
    const data = (res.data as any)?.templates || (res.data as any)?.data?.templates || []
    approvedTemplates.value = data
  } catch { /* ignore */ }
}

async function loadPublishedFlows() {
  try {
    const res = await flowsService.list({ limit: 200 })
    const data = (res.data as any)?.flows || (res.data as any)?.data?.flows || []
    publishedWaFlows.value = data.filter((f: any) => String(f.status || '').toUpperCase() === 'PUBLISHED' && f.meta_flow_id)
  } catch { /* ignore */ }
}

async function loadUsers() {
  try {
    const res = await usersService.list({ limit: 200 })
    const data = (res.data as any)?.users || (res.data as any)?.data?.users || []
    orgUsers.value = data
  } catch { /* ignore */ }
}

async function loadContactLists() {
  try {
    const res = await contactListsService.list()
    contactLists.value = (res.data as any)?.data || (res.data as any) || []
  } catch { /* ignore */ }
}

async function loadGoogleCreds() {
  try {
    const res = await googleCredentialsService.list()
    googleCreds.value = (res.data as any)?.data || (res.data as any) || []
  } catch { /* ignore */ }
}

onMounted(() => {
  loadTemplates()
  loadPublishedFlows()
  loadUsers()
  loadContactLists()
  loadGoogleCreds()
})

watch(() => props.node.type, (t) => {
  if (t === 'wa_template') loadTemplates()
  if (t === 'whatsapp_flow') loadPublishedFlows()
  if (t === 'transfer') loadUsers()
  if (t === 'phonebook') loadContactLists()
  if (t === 'google_service') loadGoogleCreds()
})

const config = computed(() => props.node.config || {})

function updateConfig(key: string, value: any) {
  emit('update:node', {
    ...props.node,
    config: { ...props.node.config, [key]: value },
  })
}

function updateLabel(label: string) {
  emit('update:node', { ...props.node, label })
}

// "Text" in the palette covers both v2 `message` (fire-and-forget) and
// v2 `prompt` (blocking + validating). The author chooses by picking an
// expected response type — anything other than "none" flips the node to
// `prompt` under the hood and exposes validation + store_as fields.
const isTextNode = computed(() => props.node.type === 'message' || props.node.type === 'prompt')

/** Palette "Message" group — optional send_to recipients apply to all of these. */
const isMessageGroupNode = computed(() =>
  ['message', 'prompt', 'buttons', 'list', 'wa_template', 'whatsapp_flow', 'ai_response'].includes(props.node.type),
)

const expectedResponse = computed<string>(() => {
  if (props.node.type === 'message') return 'none'
  return (props.node.config?.input_type as string) || 'text'
})

function setExpectedResponse(value: string) {
  if (value === 'none') {
    // Drop prompt-only fields, switch type back to message.
    const { input_type: _unused, validation_regex: _r, validation_error: _e, store_as: _s, max_retries: _m, body, ...rest } = props.node.config || {}
    void _unused; void _r; void _e; void _s; void _m
    emit('update:node', {
      ...props.node,
      type: 'message',
      config: {
        ...rest,
        // The text in the message body lived under either `body` (prompt
        // shape) or `message` (message shape) depending on history —
        // collapse to `message` for fire-and-forget.
        message: body || props.node.config?.message || '',
      },
    })
    return
  }
  // Switch to prompt and remember the response variant.
  const { message, ...rest } = props.node.config || {}
  emit('update:node', {
    ...props.node,
    type: 'prompt',
    config: {
      ...rest,
      body: rest.body || message || '',
      input_type: value,
    },
  })
}

const textBodyValue = computed(() => {
  if (props.node.type === 'prompt') return (props.node.config?.body as string) || ''
  return (props.node.config?.message as string) || ''
})

function updateTextBody(value: string) {
  const key = props.node.type === 'prompt' ? 'body' : 'message'
  updateConfig(key, value)
}

// Buttons helpers
function addReplyButton() {
  const buttons = [...(config.value.buttons || [])]
  const id = `btn_${Date.now()}_${buttons.length}`
  buttons.push({ id, title: '', type: 'reply' })
  updateConfig('buttons', buttons)
}

function addUrlButton() {
  const buttons = [...(config.value.buttons || [])]
  const id = `btn_${Date.now()}_${buttons.length}`
  buttons.push({ id, title: '', type: 'url', url: '' })
  updateConfig('buttons', buttons)
}

function addPhoneButton() {
  const buttons = [...(config.value.buttons || [])]
  const id = `btn_${Date.now()}_${buttons.length}`
  buttons.push({ id, title: '', type: 'phone', phone_number: '' })
  updateConfig('buttons', buttons)
}

function addListSection() {
  const sections = [...(config.value.sections || [])]
  sections.push({ title: '', rows: [{ id: `row_${Date.now()}`, title: '', description: '' }] })
  updateConfig('sections', sections)
}

function removeListSection(idx: number) {
  const sections = [...(config.value.sections || [])]
  sections.splice(idx, 1)
  updateConfig('sections', sections)
}

function updateListSection(idx: number, key: string, value: string) {
  const sections = (config.value.sections || []).map((s: any, i: number) =>
    i === idx ? { ...s, [key]: value } : s,
  )
  updateConfig('sections', sections)
}

function addListRow(sectionIdx: number) {
  const sections = (config.value.sections || []).map((s: any, i: number) => {
    if (i !== sectionIdx) return s
    const rows = [...(s.rows || [])]
    rows.push({ id: `row_${Date.now()}_${rows.length}`, title: '', description: '' })
    return { ...s, rows }
  })
  updateConfig('sections', sections)
}

function removeListRow(sectionIdx: number, rowIdx: number) {
  const sections = (config.value.sections || []).map((s: any, i: number) => {
    if (i !== sectionIdx) return s
    const rows = [...(s.rows || [])]
    rows.splice(rowIdx, 1)
    return { ...s, rows }
  })
  updateConfig('sections', sections)
}

function updateListRow(sectionIdx: number, rowIdx: number, key: string, value: string) {
  const sections = (config.value.sections || []).map((s: any, i: number) => {
    if (i !== sectionIdx) return s
    const rows = (s.rows || []).map((r: any, j: number) =>
      j === rowIdx ? { ...r, [key]: value } : r,
    )
    return { ...s, rows }
  })
  updateConfig('sections', sections)
}

// Condition rules helpers
function slugHandle(name: string) {
  return String(name || '')
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_|_$/g, '')
}

function addConditionRule() {
  const rules = [...(config.value.rules || [])]
  const n = rules.length + 1
  rules.push({
    name: `Condition ${n}`,
    operator: 'text_exact',
    value: '',
    value_to: '',
    case_sensitive: false,
    handle: `condition_${n}`,
  })
  updateConfig('rules', rules)
}

function removeConditionRule(idx: number) {
  const rules = [...(config.value.rules || [])]
  rules.splice(idx, 1)
  updateConfig('rules', rules)
}

function updateConditionRule(idx: number, key: string, value: any) {
  const rules = (config.value.rules || []).map((r: any, i: number) => {
    if (i !== idx) return r
    const next = { ...r, [key]: value }
    if (key === 'name' && (!r.handle || r.handle === slugHandle(r.name))) {
      next.handle = slugHandle(String(value)) || `rule_${idx}`
    }
    return next
  })
  updateConfig('rules', rules)
}

// Set variable helpers
function addSetAssignment() {
  const set = { ...(config.value.set || {}) }
  set[''] = ''
  updateConfig('set', set)
}

function removeSetAssignment(key: string) {
  const set = { ...(config.value.set || {}) }
  delete set[key]
  updateConfig('set', set)
}

function updateSetKey(oldKey: string, newKey: string) {
  if (oldKey === newKey) return
  const set = { ...(config.value.set || {}) }
  set[newKey] = set[oldKey]
  delete set[oldKey]
  updateConfig('set', set)
}

function updateSetValue(key: string, value: string) {
  const set = { ...(config.value.set || {}) }
  set[key] = value
  updateConfig('set', set)
}

function addPathAssignment() {
  const paths = [...(config.value.paths || [])]
  paths.push({ name: '', path: 'msgContext.text.body' })
  updateConfig('paths', paths)
}

function removePathAssignment(idx: number) {
  const paths = [...(config.value.paths || [])]
  paths.splice(idx, 1)
  updateConfig('paths', paths)
}

function updatePathAssignment(idx: number, key: string, value: string) {
  const paths = (config.value.paths || []).map((p: any, i: number) =>
    i === idx ? { ...p, [key]: value } : p,
  )
  updateConfig('paths', paths)
}

// WA template helpers
function selectTemplate(templateId: string) {
  const t = approvedTemplates.value.find((x) => x.id === templateId)
  if (!t) return
  emit('update:node', {
    ...props.node,
    config: {
      ...props.node.config,
      template_id: t.id,
      template_name: t.name,
      language: t.language,
      body_params: props.node.config?.body_params || {},
    },
  })
}

function updateBodyParam(key: string, value: string) {
  const body_params = { ...(config.value.body_params || {}) }
  body_params[key] = value
  updateConfig('body_params', body_params)
}

function addBodyParam() {
  const body_params = { ...(config.value.body_params || {}) }
  const keys = Object.keys(body_params)
  const next = String(keys.length + 1)
  body_params[next] = ''
  updateConfig('body_params', body_params)
}

function removeBodyParam(key: string) {
  const body_params = { ...(config.value.body_params || {}) }
  delete body_params[key]
  updateConfig('body_params', body_params)
}

function selectWaFlow(metaOrLocalId: string) {
  const f = publishedWaFlows.value.find((x) => x.meta_flow_id === metaOrLocalId || x.id === metaOrLocalId)
  if (!f) {
    updateConfig('flow_id', metaOrLocalId)
    return
  }
  emit('update:node', {
    ...props.node,
    config: {
      ...props.node.config,
      flow_id: f.meta_flow_id,
      flow_name: f.name,
    },
  })
}

function updateButton(idx: number, field: string, value: any) {
  const buttons = [...(config.value.buttons || [])]
  buttons[idx] = { ...buttons[idx], [field]: value }
  updateConfig('buttons', buttons)
}

function removeButton(idx: number) {
  const buttons = [...(config.value.buttons || [])]
  buttons.splice(idx, 1)
  updateConfig('buttons', buttons)
}

const hasReplyButtons = computed(() =>
  (config.value.buttons || []).some((b: any) => !b.type || b.type === 'reply'),
)
const hasCtaButtons = computed(() =>
  (config.value.buttons || []).some((b: any) => b.type === 'url' || b.type === 'phone'),
)
const replyCount = computed(() =>
  (config.value.buttons || []).filter((b: any) => !b.type || b.type === 'reply').length,
)
const ctaCount = computed(() =>
  (config.value.buttons || []).filter((b: any) => b.type === 'url' || b.type === 'phone').length,
)

// HTTP headers helpers (api_call / webhook)
function addHeader() {
  const headers = { ...(config.value.headers || {}) }
  headers[''] = ''
  updateConfig('headers', headers)
}

function removeHeader(key: string) {
  const headers = { ...(config.value.headers || {}) }
  delete headers[key]
  updateConfig('headers', headers)
}

function updateHeaderKey(oldKey: string, newKey: string) {
  if (oldKey === newKey) return
  const headers = { ...(config.value.headers || {}) }
  headers[newKey] = headers[oldKey]
  delete headers[oldKey]
  updateConfig('headers', headers)
}

function updateHeaderValue(key: string, value: string) {
  const headers = { ...(config.value.headers || {}) }
  headers[key] = value
  updateConfig('headers', headers)
}

// Response mapping helpers (api_call)
function addResponseMapping() {
  const m = { ...(config.value.response_mapping || {}) }
  m[''] = ''
  updateConfig('response_mapping', m)
}

function removeResponseMapping(key: string) {
  const m = { ...(config.value.response_mapping || {}) }
  delete m[key]
  updateConfig('response_mapping', m)
}

function updateResponseMappingKey(oldKey: string, newKey: string) {
  if (oldKey === newKey) return
  const m = { ...(config.value.response_mapping || {}) }
  m[newKey] = m[oldKey]
  delete m[oldKey]
  updateConfig('response_mapping', m)
}

function updateResponseMappingValue(key: string, value: string) {
  const m = { ...(config.value.response_mapping || {}) }
  m[key] = value
  updateConfig('response_mapping', m)
}

// Timing schedule
const defaultSchedule = [
  { day: 'monday', enabled: true, start_time: '09:00', end_time: '17:00' },
  { day: 'tuesday', enabled: true, start_time: '09:00', end_time: '17:00' },
  { day: 'wednesday', enabled: true, start_time: '09:00', end_time: '17:00' },
  { day: 'thursday', enabled: true, start_time: '09:00', end_time: '17:00' },
  { day: 'friday', enabled: true, start_time: '09:00', end_time: '17:00' },
  { day: 'saturday', enabled: false, start_time: '09:00', end_time: '17:00' },
  { day: 'sunday', enabled: false, start_time: '09:00', end_time: '17:00' },
]
const schedule = computed(() => config.value.schedule || defaultSchedule)

function updateScheduleEntry(idx: number, field: string, value: any) {
  const sched = [...schedule.value]
  sched[idx] = { ...sched[idx], [field]: value }
  updateConfig('schedule', sched)
}

const gotoFlowTargets = computed(() =>
  (props.availableFlows || []).filter((f) => f.id !== props.currentFlowId),
)

const typeLabel: Record<string, string> = {
  start: 'Start',
  message: 'Message',
  prompt: 'Prompt',
  buttons: 'Buttons',
  list: 'List',
  set_variable: 'Set Variable',
  wa_template: 'WA Template',
  delay: 'Delay',
  reset_session: 'Reset Session',
  ai_response: 'AI Response',
  set_labels: 'Labels',
  disable_bot: 'Disable Bot',
  send_email: 'Email',
  phonebook: 'Phonebook',
  google_service: 'Google',
  api_call: 'API Call',
  condition: 'Condition',
  timing: 'Timing',
  transfer: 'Transfer',
  end: 'End',
  goto_flow: 'Go to Flow',
  whatsapp_flow: 'WhatsApp Flow',
  webhook: 'Webhook',
}

const typeDescription: Record<string, string> = {
  start: 'Entry point and fallback hub when input does not match.',
  message: 'Send text, media, or a location to the contact.',
  prompt: 'Ask a question, optionally validate the reply, then continue.',
  buttons: 'Send quick-reply buttons and branch on the choice.',
  list: 'Send a list picker and branch on the selected row.',
  set_variable: 'Write or map values into session variables.',
  wa_template: 'Send an approved WhatsApp template message.',
  delay: 'Pause the flow for a set time before continuing.',
  reset_session: 'Clear session variables and continue on the default edge.',
  ai_response: 'Generate a reply with the configured AI provider.',
  set_labels: 'Add or remove contact labels.',
  disable_bot: 'Mute the chatbot for this contact for a period.',
  send_email: 'Send an email via the organization SMTP settings.',
  phonebook: 'Add or update the contact on a contact list.',
  google_service: 'Call Google Sheets, Calendar, Drive, or Docs.',
  api_call: 'Call an HTTP API and optionally store the response.',
  condition: 'Branch on expression rules; unmatched uses the else path.',
  timing: 'Branch based on business hours or a custom schedule.',
  transfer: 'Hand the conversation to an agent or team queue.',
  end: 'Stop the flow and complete the session.',
  goto_flow: 'Jump into another chatbot flow.',
  whatsapp_flow: 'Open a Meta WhatsApp Flow form and wait for submission.',
  webhook: 'POST to a URL (use {{{inbound}}} for the full Meta webhook JSON), then continue.',
}

const nodeTypeDescription = computed(() => typeDescription[props.node.type] || '')

// Built-in session variables seeded every chatbot turn. Shown on the Start
// node so authors know what they can interpolate ({{x}} or {{{x}}}).
const builtinVariables: { name: string; description: string }[] = [
  { name: 'contact_name', description: 'Contact profile name (alias: senderName)' },
  { name: 'phone_number', description: 'Contact phone number (alias: senderMobile)' },
  { name: 'senderName', description: 'Same as contact_name (whatsCRM alias)' },
  { name: 'senderMobile', description: 'Same as phone_number (whatsCRM alias)' },
  { name: 'senderMessage', description: 'Latest inbound message text/caption this turn' },
  { name: 'inbound', description: 'Full Meta webhook body (use {{{inbound}}}); text via {{senderMessage}} or inbound.entry[0].changes[0].value.messages[0].text.body' },
  { name: 'inbound.type', description: 'Inbound WhatsApp type (text, image, interactive, …)' },
  { name: 'inbound.text.body', description: 'Text body when type is text' },
  { name: 'inbound_message_type', description: 'Inbound type alias (button_reply, image, …)' },
  { name: 'senderMessageType', description: 'Alias of inbound_message_type' },
  { name: 'inbound_media_url', description: 'Local path of inbound media (when present)' },
  { name: 'inbound_media_mime', description: 'MIME type of inbound media' },
  { name: 'inbound_media_filename', description: 'Filename for inbound documents' },
  { name: 'inbound_latitude', description: 'Latitude when inbound is location' },
  { name: 'inbound_longitude', description: 'Longitude when inbound is location' },
]

const mediaUploading = ref(false)
const mediaFileInput = ref<HTMLInputElement | null>(null)

const mediaAccept = computed(() => {
  switch (config.value.media_type) {
    case 'image': return 'image/jpeg,image/png,image/webp,image/gif'
    case 'audio': return 'audio/mpeg,audio/mp4,audio/aac,audio/ogg,audio/opus'
    case 'video': return 'video/mp4,video/3gpp'
    case 'document': return '.pdf,.doc,.docx,.xls,.xlsx,.txt,application/pdf'
    default: return '*/*'
  }
})

async function onMediaFileSelected(ev: Event) {
  const input = ev.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  mediaUploading.value = true
  try {
    const res = await chatbotService.uploadMedia(file)
    const data = (res.data as any)?.data || res.data
    const url = data?.url
    if (!url) throw new Error('No URL returned')
    updateConfig('media_url', url)
    if (config.value.media_type === 'document' && file.name) {
      updateConfig('filename', file.name)
    }
  } catch (e: any) {
    console.error('Media upload failed', e)
    alert(e?.response?.data?.message || e?.message || 'Upload failed')
  } finally {
    mediaUploading.value = false
    if (input) input.value = ''
  }
}

const escapeKeywordsStr = computed({
  get: () => {
    const raw = config.value.escape_keywords
    if (Array.isArray(raw)) return raw.join(', ')
    return typeof raw === 'string' ? raw : ''
  },
  set: (v: string) => {
    const list = v.split(',').map((s) => s.trim()).filter(Boolean)
    updateConfig('escape_keywords', list)
  },
})

const copiedVar = ref<string | null>(null)

async function copyVariable(name: string) {
  const token = `{{${name}}}`
  try {
    await navigator.clipboard.writeText(token)
    copiedVar.value = name
    window.setTimeout(() => {
      if (copiedVar.value === name) copiedVar.value = null
    }, 1500)
  } catch {
    // Clipboard may be unavailable (insecure context / denied).
  }
}

function variableChip(name: string) {
  return `{{${name}}}`
}

const varExampleHint = '{{contact_name}}, {{{senderMessage}}}'
</script>

<template>
  <div class="space-y-4 p-4">
    <div class="flex items-center justify-between">
      <h3 class="font-semibold text-sm">{{ typeLabel[node.type] || node.type }}</h3>
      <div class="flex items-center gap-1">
        <Button
          v-if="node.type !== 'start'"
          variant="ghost"
          size="icon"
          class="h-7 w-7"
          :title="'Duplicate'"
          @click="emit('duplicate')"
        >
          <Copy class="h-3.5 w-3.5" />
        </Button>
        <Button v-if="node.type !== 'start'" variant="ghost" size="icon" class="h-7 w-7" @click="emit('delete')">
          <Trash2 class="h-3.5 w-3.5 text-destructive" />
        </Button>
      </div>
    </div>
    <p v-if="nodeTypeDescription" class="text-xs text-muted-foreground -mt-2">
      {{ nodeTypeDescription }}
    </p>

    <!-- Optional override recipients (Message palette group).
         Empty → {{senderMobile}}; otherwise use the entered value(s). -->
    <div v-if="isMessageGroupNode" class="space-y-1.5">
      <Label class="text-xs">Send to <span class="text-muted-foreground font-normal">(optional)</span></Label>
      <Input
        :model-value="(config.send_to as string) || ''"
        @update:model-value="(v: string) => updateConfig('send_to', v)"
        :placeholder="'{{senderMobile}}'"
        class="h-8 text-xs font-mono"
      />
      <p class="text-[10px] text-muted-foreground">
        Leave empty to send to <code v-pre>{{senderMobile}}</code>.
        Or enter one or more numbers (comma-separated). Supports session variables.
      </p>
    </div>

    <!-- Start: entry point + available builtin variables. -->
    <template v-if="node.type === 'start'">
      <div class="space-y-1.5 pt-1">
        <Label class="text-xs">Fallback message</Label>
        <Textarea
          :model-value="(config.fallback_message as string) || ''"
          @update:model-value="(v: string) => updateConfig('fallback_message', v)"
          placeholder="Optional — sent when returning to entry (e.g. Please choose from the menu)"
          class="min-h-[60px] text-xs"
        />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Escape keywords</Label>
        <Input
          :model-value="escapeKeywordsStr"
          @update:model-value="(v: string) => (escapeKeywordsStr = String(v ?? ''))"
          placeholder="menu, hi, 0"
          class="h-8 text-xs"
        />
        <p class="text-[10px] text-muted-foreground">Comma-separated. Exact match jumps back to this entry.</p>
      </div>
      <div class="space-y-1.5 pt-1">
        <Label class="text-xs">Available variables</Label>
        <p class="text-[10px] text-muted-foreground">
          Click to copy. Use <code v-pre>{{name}}</code> or <code v-pre>{{{name}}}</code> in any message field.
        </p>
        <div class="flex flex-wrap gap-1.5">
          <button
            v-for="v in builtinVariables"
            :key="v.name"
            type="button"
            class="inline-flex items-center gap-1 rounded-md border bg-muted/40 px-2 py-1 font-mono text-[11px] hover:bg-muted transition-colors"
            :title="v.description"
            @click="copyVariable(v.name)"
          >
            <span>{{ variableChip(v.name) }}</span>
            <Check v-if="copiedVar === v.name" class="h-3 w-3 text-green-600" />
            <Copy v-else class="h-3 w-3 text-muted-foreground" />
          </button>
        </div>
      </div>
    </template>

    <!-- Label -->
    <div v-if="node.type !== 'start'" class="space-y-1.5">
      <Label class="text-xs">Label</Label>
      <Input :model-value="node.label" @update:model-value="(v) => updateLabel(String(v ?? ''))" class="h-8 text-sm" />
    </div>

    <!-- text (message OR prompt) -->
    <template v-if="isTextNode">
      <div v-if="node.type === 'message'" class="space-y-1.5">
        <Label class="text-xs">Content type</Label>
        <Select
          :model-value="(config.media_type as string) || 'text'"
          @update:model-value="(v: any) => updateConfig('media_type', v)"
        >
          <SelectTrigger class="h-8 text-sm"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="text">Simple Text</SelectItem>
            <SelectItem value="image">Image</SelectItem>
            <SelectItem value="audio">Audio</SelectItem>
            <SelectItem value="video">Video</SelectItem>
            <SelectItem value="document">Document</SelectItem>
            <SelectItem value="location">Location</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <template v-if="node.type === 'prompt' || !config.media_type || config.media_type === 'text'">
        <div class="space-y-1.5">
          <Label class="text-xs">Message</Label>
          <Textarea
            :model-value="textBodyValue"
            @update:model-value="(v: string) => updateTextBody(String(v ?? ''))"
            placeholder="Enter your message"
            class="min-h-[80px] text-xs"
          />
          <p class="text-[10px] text-muted-foreground">
            Use <code v-pre>{{name}}</code> / <code v-pre>{{{name}}}</code> placeholders
            (<span class="font-mono" v-text="varExampleHint" />) to interpolate session variables.
          </p>
        </div>
        <div v-if="node.type === 'message'" class="flex items-center justify-between gap-2">
          <div>
            <Label class="text-xs">URL preview</Label>
            <p class="text-[10px] text-muted-foreground">Show link previews in WhatsApp</p>
          </div>
          <Switch
            :checked="!!config.preview_url"
            @update:checked="(v: boolean) => updateConfig('preview_url', v)"
          />
        </div>
      </template>

      <template v-else-if="['image','audio','video','document'].includes(String(config.media_type))">
        <div class="space-y-1.5">
          <Label class="text-xs">Media URL</Label>
          <Input
            :model-value="config.media_url || ''"
            @update:model-value="(v: string) => updateConfig('media_url', v)"
            placeholder="https://example.com/file.jpg"
            class="h-8 text-xs font-mono"
          />
          <div class="flex items-center gap-2">
            <input
              ref="mediaFileInput"
              type="file"
              class="hidden"
              :accept="mediaAccept"
              @change="onMediaFileSelected"
            />
            <Button
              type="button"
              variant="outline"
              size="sm"
              class="h-7 text-xs"
              :disabled="mediaUploading"
              @click="mediaFileInput?.click()"
            >
              {{ mediaUploading ? 'Uploading…' : 'Upload file' }}
            </Button>
            <p class="text-[10px] text-muted-foreground flex-1">
              Paste a public HTTPS link, or upload — served from this app’s public URL (same origin as the webhook).
            </p>
          </div>
        </div>
        <div v-if="config.media_type !== 'audio'" class="space-y-1.5">
          <Label class="text-xs">Caption</Label>
          <Input
            :model-value="config.caption || ''"
            @update:model-value="(v: string) => updateConfig('caption', v)"
            placeholder="Optional caption"
            class="h-8 text-xs"
          />
        </div>
        <div v-if="config.media_type === 'document'" class="space-y-1.5">
          <Label class="text-xs">Filename</Label>
          <Input
            :model-value="config.filename || ''"
            @update:model-value="(v: string) => updateConfig('filename', v)"
            placeholder="report.pdf"
            class="h-8 text-xs"
          />
        </div>
      </template>

      <template v-else-if="config.media_type === 'location'">
        <div class="grid grid-cols-2 gap-2">
          <div class="space-y-1.5">
            <Label class="text-xs">Latitude</Label>
            <Input
              :model-value="String(config.latitude ?? '')"
              @update:model-value="(v: string) => updateConfig('latitude', parseFloat(v) || 0)"
              placeholder="-1.2921"
              class="h-8 text-xs font-mono"
            />
          </div>
          <div class="space-y-1.5">
            <Label class="text-xs">Longitude</Label>
            <Input
              :model-value="String(config.longitude ?? '')"
              @update:model-value="(v: string) => updateConfig('longitude', parseFloat(v) || 0)"
              placeholder="36.8219"
              class="h-8 text-xs font-mono"
            />
          </div>
        </div>
        <div class="space-y-1.5">
          <Label class="text-xs">Name</Label>
          <Input
            :model-value="config.name || ''"
            @update:model-value="(v: string) => updateConfig('name', v)"
            placeholder="Office"
            class="h-8 text-xs"
          />
        </div>
        <div class="space-y-1.5">
          <Label class="text-xs">Address</Label>
          <Input
            :model-value="config.address || ''"
            @update:model-value="(v: string) => updateConfig('address', v)"
            placeholder="Street, city"
            class="h-8 text-xs"
          />
        </div>
      </template>

      <div v-if="node.type === 'message'" class="flex items-center justify-between gap-2">
        <div>
          <Label class="text-xs">Wait for reply</Label>
          <p class="text-[10px] text-muted-foreground">Pause the flow until the next inbound message</p>
        </div>
        <Switch
          :checked="!!config.wait_for_reply"
          @update:checked="(v: boolean) => updateConfig('wait_for_reply', v)"
        />
      </div>
      <div v-if="node.type === 'message' && config.wait_for_reply" class="space-y-1.5">
        <Label class="text-xs">Store reply as</Label>
        <Input
          :model-value="config.store_as || 'senderMessage'"
          @update:model-value="(v: string) => updateConfig('store_as', v)"
          placeholder="senderMessage"
          class="h-8 text-sm font-mono"
        />
      </div>

      <div v-if="node.type === 'prompt' || ((!config.media_type || config.media_type === 'text') && !config.wait_for_reply)" class="space-y-1.5">
        <Label class="text-xs">Expected response</Label>
        <Select :model-value="expectedResponse" @update:model-value="(v: any) => setExpectedResponse(v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="none">None (fire-and-forget)</SelectItem>
            <SelectItem value="text">Text</SelectItem>
            <SelectItem value="number">Number</SelectItem>
            <SelectItem value="email">Email</SelectItem>
            <SelectItem value="phone">Phone</SelectItem>
            <SelectItem value="date">Date</SelectItem>
            <SelectItem value="select">Selection</SelectItem>
          </SelectContent>
        </Select>
        <p class="text-[10px] text-muted-foreground">When set, the flow waits for the user's reply before continuing.</p>
      </div>
      <template v-if="node.type === 'prompt'">
        <div class="space-y-1.5">
          <Label class="text-xs">Store response as</Label>
          <Input
            :model-value="config.store_as || ''"
            @update:model-value="(v: string) => updateConfig('store_as', v)"
            placeholder="variable_name"
            class="h-8 text-sm font-mono"
          />
        </div>
        <div class="space-y-1.5">
          <Label class="text-xs">Validation regex (optional)</Label>
          <Input
            :model-value="config.validation_regex || ''"
            @update:model-value="(v: string) => updateConfig('validation_regex', v)"
            placeholder="^[0-9]+$"
            class="h-8 text-xs font-mono"
          />
        </div>
        <div class="space-y-1.5">
          <Label class="text-xs">Validation error message</Label>
          <Input
            :model-value="config.validation_error || ''"
            @update:model-value="(v: string) => updateConfig('validation_error', v)"
            placeholder="Invalid input. Please try again."
            class="h-8 text-xs"
          />
        </div>
        <div class="space-y-1.5">
          <Label class="text-xs">Max retries</Label>
          <Input
            type="number"
            :model-value="String(config.max_retries ?? 3)"
            @update:model-value="(v: string) => updateConfig('max_retries', parseInt(v) || 3)"
            class="h-8 text-sm"
            min="1"
            max="10"
          />
        </div>
      </template>
    </template>

    <!-- list -->
    <template v-if="node.type === 'list'">
      <div class="space-y-1.5">
        <Label class="text-xs">Body</Label>
        <Textarea
          :model-value="config.body || ''"
          @update:model-value="(v: string) => updateConfig('body', v)"
          placeholder="Message shown above the list"
          class="min-h-[60px] text-xs"
        />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Header (optional)</Label>
        <Input
          :model-value="config.header || ''"
          @update:model-value="(v: string) => updateConfig('header', v)"
          class="h-8 text-xs"
        />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Footer (optional)</Label>
        <Input
          :model-value="config.footer || ''"
          @update:model-value="(v: string) => updateConfig('footer', v)"
          class="h-8 text-xs"
        />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Menu button label</Label>
        <Input
          :model-value="config.button || ''"
          @update:model-value="(v: string) => updateConfig('button', v)"
          placeholder="Select an option"
          class="h-8 text-xs"
        />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Store selection as</Label>
        <Input
          :model-value="config.store_as || ''"
          @update:model-value="(v: string) => updateConfig('store_as', v)"
          placeholder="choice"
          class="h-8 text-sm font-mono"
        />
      </div>
      <div class="space-y-2">
        <div class="flex items-center justify-between">
          <Label class="text-xs">Sections</Label>
          <Button variant="outline" size="sm" class="h-7 text-xs" @click="addListSection">
            <Plus class="h-3 w-3 mr-0.5" /> Section
          </Button>
        </div>
        <div v-for="(sec, sIdx) in (config.sections || [])" :key="sIdx" class="p-2 border rounded-md space-y-2 bg-muted/30">
          <div class="flex items-center gap-1">
            <Input
              :model-value="sec.title || ''"
              @update:model-value="(v: string) => updateListSection(Number(sIdx), 'title', v)"
              placeholder="Section title"
              class="h-7 text-xs flex-1"
            />
            <Button variant="ghost" size="icon" class="h-6 w-6" @click="removeListSection(Number(sIdx))">
              <Trash2 class="h-3 w-3 text-destructive" />
            </Button>
          </div>
          <div v-for="(row, rIdx) in (sec.rows || [])" :key="row.id || rIdx" class="pl-2 border-l space-y-1">
            <div class="flex items-center gap-1">
              <Input
                :model-value="row.title || ''"
                @update:model-value="(v: string) => updateListRow(Number(sIdx), Number(rIdx), 'title', v)"
                placeholder="Row title"
                class="h-7 text-xs flex-1"
              />
              <Button variant="ghost" size="icon" class="h-6 w-6" @click="removeListRow(Number(sIdx), Number(rIdx))">
                <Trash2 class="h-3 w-3 text-destructive" />
              </Button>
            </div>
            <Input
              :model-value="row.id || ''"
              @update:model-value="(v: string) => updateListRow(Number(sIdx), Number(rIdx), 'id', v)"
              placeholder="row_id"
              class="h-7 text-xs font-mono"
            />
            <Input
              :model-value="row.description || ''"
              @update:model-value="(v: string) => updateListRow(Number(sIdx), Number(rIdx), 'description', v)"
              placeholder="Description (optional)"
              class="h-7 text-xs"
            />
          </div>
          <Button variant="outline" size="sm" class="h-7 text-xs w-full" @click="addListRow(Number(sIdx))">
            <Plus class="h-3 w-3 mr-0.5" /> Row
          </Button>
        </div>
      </div>
    </template>

    <!-- buttons -->
    <template v-if="node.type === 'buttons'">
      <div class="space-y-1.5">
        <Label class="text-xs">Body</Label>
        <Textarea
          :model-value="config.body || ''"
          @update:model-value="(v: string) => updateConfig('body', v)"
          placeholder="Message shown above the buttons"
          class="min-h-[60px] text-xs"
        />
      </div>
      <div class="space-y-1.5">
        <div class="flex items-center justify-between">
          <Label class="text-xs">Button Options ({{ (config.buttons || []).length }}/{{ hasCtaButtons ? 2 : 10 }})</Label>
        </div>
        <div class="flex gap-1">
          <Button variant="outline" size="sm" class="h-7 text-xs" :disabled="hasCtaButtons || replyCount >= 10" @click="addReplyButton">
            <Plus class="h-3 w-3 mr-0.5" /> Reply
          </Button>
          <Button variant="outline" size="sm" class="h-7 text-xs" :disabled="hasReplyButtons || ctaCount >= 2" @click="addUrlButton">
            <Plus class="h-3 w-3 mr-0.5" /> URL
          </Button>
          <Button variant="outline" size="sm" class="h-7 text-xs" :disabled="hasReplyButtons || ctaCount >= 2" @click="addPhoneButton">
            <Plus class="h-3 w-3 mr-0.5" /> Phone
          </Button>
        </div>
        <div v-for="(btn, idx) in (config.buttons || [])" :key="btn.id || idx" class="p-2 border rounded-md space-y-2 bg-muted/30">
          <div class="flex items-center gap-1">
            <span class="text-[10px] uppercase text-muted-foreground w-12">{{ btn.type || 'reply' }}</span>
            <Input
              :model-value="btn.title || ''"
              @update:model-value="(v: string) => updateButton(Number(idx), 'title', v)"
              placeholder="Button Title"
              class="h-7 text-xs flex-1"
            />
            <Button variant="ghost" size="icon" class="h-6 w-6" @click="removeButton(Number(idx))">
              <Trash2 class="h-3 w-3 text-destructive" />
            </Button>
          </div>
          <Input
            :model-value="btn.id || ''"
            @update:model-value="(v: string) => updateButton(Number(idx), 'id', v)"
            placeholder="button_id"
            class="h-7 text-xs font-mono"
          />
          <Input
            v-if="btn.type === 'url'"
            :model-value="btn.url || ''"
            @update:model-value="(v: string) => updateButton(Number(idx), 'url', v)"
            placeholder="https://example.com"
            class="h-7 text-xs font-mono"
          />
          <Input
            v-if="btn.type === 'phone'"
            :model-value="btn.phone_number || ''"
            @update:model-value="(v: string) => updateButton(Number(idx), 'phone_number', v)"
            placeholder="+1234567890"
            class="h-7 text-xs font-mono"
          />
        </div>
        <p class="text-[10px] text-muted-foreground">Reply buttons (max 10) send the user's choice back. URL / Phone buttons (max 2 per node, mutually exclusive with Reply) open a link or call. Wire reply buttons to next nodes by dragging from the button handle on the canvas.</p>
      </div>

      <!-- Input — buttons always expect a button selection; surface this
           for visual consistency with text nodes. -->
      <div class="pt-2 border-t space-y-1.5">
        <Label class="text-xs">Expected response</Label>
        <Select model-value="button" disabled>
          <SelectTrigger class="h-8 text-sm"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="button">Selection (buttons)</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div class="space-y-1.5">
        <Label class="text-xs">Store response as (optional)</Label>
        <Input
          :model-value="config.store_as || ''"
          @update:model-value="(v: string) => updateConfig('store_as', v)"
          placeholder="variable_name"
          class="h-8 text-sm font-mono"
        />
        <p class="text-[10px] text-muted-foreground">Saves the tapped button's title into this variable so later nodes can reference it.</p>
      </div>

    </template>

    <!-- api_call -->
    <template v-if="node.type === 'api_call'">
      <div class="space-y-1.5">
        <Label class="text-xs">URL</Label>
        <Input
          :model-value="config.url || ''"
          @update:model-value="(v: string) => updateConfig('url', v)"
          placeholder="https://api.example.com/endpoint"
          class="h-8 text-xs font-mono"
        />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Method</Label>
        <Select :model-value="config.method || 'GET'" @update:model-value="(v: any) => updateConfig('method', v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="GET">GET</SelectItem>
            <SelectItem value="POST">POST</SelectItem>
            <SelectItem value="PUT">PUT</SelectItem>
            <SelectItem value="PATCH">PATCH</SelectItem>
            <SelectItem value="DELETE">DELETE</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div class="space-y-1.5">
        <div class="flex items-center justify-between">
          <Label class="text-xs">Headers</Label>
          <Button variant="outline" size="sm" class="h-6 text-xs" @click="addHeader">
            <Plus class="h-3 w-3 mr-1" /> Add
          </Button>
        </div>
        <div v-for="(val, key) in (config.headers || {})" :key="String(key)" class="flex items-center gap-1">
          <Input :model-value="String(key)" @update:model-value="(v: string) => updateHeaderKey(String(key), v)" placeholder="Key" class="h-7 text-xs flex-1" />
          <Input :model-value="String(val)" @update:model-value="(v: string) => updateHeaderValue(String(key), v)" placeholder="Value" class="h-7 text-xs flex-1" />
          <Button variant="ghost" size="icon" class="h-6 w-6" @click="removeHeader(String(key))">
            <Trash2 class="h-3 w-3 text-destructive" />
          </Button>
        </div>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Body</Label>
        <Textarea
          :model-value="config.body || ''"
          @update:model-value="(v: string) => updateConfig('body', v)"
          placeholder='{"phone": "{{phone_number}}"}'
          class="min-h-[60px] text-xs font-mono"
        />
      </div>
      <div class="space-y-1.5">
        <div class="flex items-center justify-between">
          <Label class="text-xs">Save response to variables</Label>
          <Button variant="outline" size="sm" class="h-6 text-xs" @click="addResponseMapping">
            <Plus class="h-3 w-3 mr-1" /> Add
          </Button>
        </div>
        <p class="text-[10px] text-muted-foreground">Map JSON paths into session variables (e.g. <code>data.user.name</code>).</p>
        <div v-for="(val, key) in (config.response_mapping || {})" :key="String(key)" class="flex items-center gap-1">
          <Input :model-value="String(key)" @update:model-value="(v: string) => updateResponseMappingKey(String(key), v)" placeholder="var_name" class="h-7 text-xs flex-1 font-mono" />
          <Input :model-value="String(val)" @update:model-value="(v: string) => updateResponseMappingValue(String(key), v)" placeholder="path.to.field" class="h-7 text-xs flex-1 font-mono" />
          <Button variant="ghost" size="icon" class="h-6 w-6" @click="removeResponseMapping(String(key))">
            <Trash2 class="h-3 w-3 text-destructive" />
          </Button>
        </div>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Message template (optional)</Label>
        <Textarea
          :model-value="config.message_template || ''"
          @update:model-value="(v: string) => updateConfig('message_template', v)"
          placeholder="Hello {{user_name}}!"
          class="min-h-[50px] text-xs"
        />
        <p class="text-[10px] text-muted-foreground">Sent on 2xx response after mappings are applied.</p>
      </div>
    </template>

    <!-- condition -->
    <template v-if="node.type === 'condition'">
      <div class="space-y-1.5">
        <Label class="text-xs">Mode</Label>
        <Select
          :model-value="(config.mode as string) || (config.rules?.length ? 'rules' : 'expression')"
          @update:model-value="(v: any) => updateConfig('mode', v)"
        >
          <SelectTrigger class="h-8 text-sm"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="rules">Rules (named branches)</SelectItem>
            <SelectItem value="expression">Expression (expr-lang)</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <template v-if="(config.mode || (config.rules?.length ? 'rules' : 'expression')) === 'rules'">
        <div class="space-y-1.5">
          <Label class="text-xs">Compare variable</Label>
          <Input
            :model-value="config.variable || 'senderMessage'"
            @update:model-value="(v: string) => updateConfig('variable', v)"
            placeholder="senderMessage"
            class="h-8 text-xs font-mono"
          />
          <p class="text-[10px] text-muted-foreground">Defaults to last inbound text (<code>senderMessage</code>).</p>
        </div>
        <div class="flex items-center justify-between">
          <Label class="text-xs">Rules</Label>
          <Button variant="outline" size="sm" class="h-7 text-xs" @click="addConditionRule">
            <Plus class="h-3 w-3 mr-0.5" /> Rule
          </Button>
        </div>
        <div v-for="(rule, idx) in (config.rules || [])" :key="idx" class="p-2 border rounded-md space-y-2 bg-muted/30">
          <div class="flex items-center gap-1">
            <Input
              :model-value="rule.name || ''"
              @update:model-value="(v: string) => updateConditionRule(Number(idx), 'name', v)"
              placeholder="Branch name"
              class="h-7 text-xs flex-1"
            />
            <Button variant="ghost" size="icon" class="h-6 w-6" @click="removeConditionRule(Number(idx))">
              <Trash2 class="h-3 w-3 text-destructive" />
            </Button>
          </div>
          <Select :model-value="rule.operator || 'text_exact'" @update:model-value="(v: any) => updateConditionRule(Number(idx), 'operator', v)">
            <SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="text_exact">Text equals</SelectItem>
              <SelectItem value="text_contains">Text contains</SelectItem>
              <SelectItem value="text_starts_with">Text starts with</SelectItem>
              <SelectItem value="text_ends_with">Text ends with</SelectItem>
              <SelectItem value="number_equals">Number equals</SelectItem>
              <SelectItem value="number_greater">Number greater</SelectItem>
              <SelectItem value="number_less">Number less</SelectItem>
              <SelectItem value="number_between">Number between</SelectItem>
            </SelectContent>
          </Select>
          <div class="grid grid-cols-2 gap-1">
            <Input
              :model-value="rule.value || ''"
              @update:model-value="(v: string) => updateConditionRule(Number(idx), 'value', v)"
              :placeholder="rule.operator === 'number_between' ? 'From' : 'Value'"
              class="h-7 text-xs"
            />
            <Input
              v-if="rule.operator === 'number_between'"
              :model-value="rule.value_to || ''"
              @update:model-value="(v: string) => updateConditionRule(Number(idx), 'value_to', v)"
              placeholder="To"
              class="h-7 text-xs"
            />
          </div>
          <div class="flex items-center justify-between">
            <Label class="text-[10px]">Case sensitive</Label>
            <Switch
              :checked="!!rule.case_sensitive"
              @update:checked="(v: boolean) => updateConditionRule(Number(idx), 'case_sensitive', v)"
            />
          </div>
          <Input
            :model-value="rule.handle || ''"
            @update:model-value="(v: string) => updateConditionRule(Number(idx), 'handle', v)"
            placeholder="handle id"
            class="h-7 text-xs font-mono"
          />
        </div>
        <p class="text-[10px] text-muted-foreground">First match wins. Unmatched routes via the <code>else</code> handle.</p>
      </template>

      <template v-else>
        <div class="space-y-1.5">
          <Label class="text-xs">Expression</Label>
          <Textarea
            :model-value="config.expression || ''"
            @update:model-value="(v: string) => updateConfig('expression', v)"
            placeholder='tier == "premium" and amount > 100'
            class="min-h-[60px] text-xs font-mono"
          />
          <p class="text-[10px] text-muted-foreground">Routes via the <code>true</code> / <code>false</code> handle. Uses expr-lang syntax.</p>
        </div>
      </template>
    </template>

    <!-- set_variable -->
    <template v-if="node.type === 'set_variable'">
      <div class="space-y-1.5">
        <div class="flex items-center justify-between">
          <Label class="text-xs">Templated values</Label>
          <Button variant="outline" size="sm" class="h-7 text-xs" @click="addSetAssignment">
            <Plus class="h-3 w-3 mr-0.5" /> Variable
          </Button>
        </div>
        <div v-for="(val, key) in (config.set || {})" :key="'set-' + key" class="flex gap-1">
          <Input
            :model-value="String(key)"
            @update:model-value="(v: string) => updateSetKey(String(key), v)"
            placeholder="name"
            class="h-7 text-xs font-mono w-28"
          />
          <Input
            :model-value="String(val ?? '')"
            @update:model-value="(v: string) => updateSetValue(String(key), v)"
            placeholder="Hello {{contact_name}}"
            class="h-7 text-xs flex-1"
          />
          <Button variant="ghost" size="icon" class="h-6 w-6" @click="removeSetAssignment(String(key))">
            <Trash2 class="h-3 w-3 text-destructive" />
          </Button>
        </div>
      </div>
      <div class="space-y-1.5">
        <div class="flex items-center justify-between">
          <Label class="text-xs">From inbound path</Label>
          <Button variant="outline" size="sm" class="h-7 text-xs" @click="addPathAssignment">
            <Plus class="h-3 w-3 mr-0.5" /> Path
          </Button>
        </div>
        <p class="text-[10px] text-muted-foreground">
          Paths walk <code>__inbound__</code> (e.g. <code>msgContext.text.body</code> or <code>senderName</code>).
        </p>
        <div v-for="(p, idx) in (config.paths || [])" :key="'path-' + idx" class="flex gap-1">
          <Input
            :model-value="p.name || ''"
            @update:model-value="(v: string) => updatePathAssignment(Number(idx), 'name', v)"
            placeholder="var"
            class="h-7 text-xs font-mono w-24"
          />
          <Input
            :model-value="p.path || ''"
            @update:model-value="(v: string) => updatePathAssignment(Number(idx), 'path', v)"
            placeholder="msgContext.text.body"
            class="h-7 text-xs font-mono flex-1"
          />
          <Button variant="ghost" size="icon" class="h-6 w-6" @click="removePathAssignment(Number(idx))">
            <Trash2 class="h-3 w-3 text-destructive" />
          </Button>
        </div>
      </div>
    </template>

    <!-- wa_template -->
    <template v-if="node.type === 'wa_template'">
      <div class="space-y-1.5">
        <Label class="text-xs">Template</Label>
        <Select :model-value="config.template_id || ''" @update:model-value="(v: any) => selectTemplate(String(v))">
          <SelectTrigger class="h-8 text-sm"><SelectValue placeholder="Select approved template" /></SelectTrigger>
          <SelectContent>
            <SelectItem v-for="t in approvedTemplates" :key="t.id" :value="t.id">
              {{ t.display_name || t.name }} ({{ t.language }})
            </SelectItem>
          </SelectContent>
        </Select>
        <p v-if="!approvedTemplates.length" class="text-[10px] text-muted-foreground">No approved templates found. Sync templates in Settings.</p>
      </div>
      <div class="space-y-1.5">
        <div class="flex items-center justify-between">
          <Label class="text-xs">Body parameters</Label>
          <Button variant="outline" size="sm" class="h-7 text-xs" @click="addBodyParam">
            <Plus class="h-3 w-3 mr-0.5" /> Param
          </Button>
        </div>
        <div v-for="(val, key) in (config.body_params || {})" :key="'bp-' + key" class="flex gap-1">
          <Input :model-value="String(key)" disabled class="h-7 text-xs font-mono w-16" />
          <Input
            :model-value="String(val ?? '')"
            @update:model-value="(v: string) => updateBodyParam(String(key), v)"
            placeholder="{{order_id}}"
            class="h-7 text-xs flex-1 font-mono"
          />
          <Button variant="ghost" size="icon" class="h-6 w-6" @click="removeBodyParam(String(key))">
            <Trash2 class="h-3 w-3 text-destructive" />
          </Button>
        </div>
      </div>
      <div class="flex items-center justify-between gap-2">
        <div>
          <Label class="text-xs">Wait for reply</Label>
          <p class="text-[10px] text-muted-foreground">Pause until next inbound</p>
        </div>
        <Switch
          :checked="!!config.wait_for_reply"
          @update:checked="(v: boolean) => updateConfig('wait_for_reply', v)"
        />
      </div>
    </template>

    <!-- delay -->
    <template v-if="node.type === 'delay'">
      <div class="space-y-1.5">
        <Label class="text-xs">Seconds</Label>
        <Input
          type="number"
          :model-value="String(config.seconds ?? 5)"
          @update:model-value="(v: string) => updateConfig('seconds', Math.min(3600, Math.max(0, parseInt(v) || 0)))"
          class="h-8 text-sm"
          min="0"
          max="3600"
        />
        <p class="text-[10px] text-muted-foreground">Pause 1–3600s then continue (webhook returns immediately).</p>
      </div>
    </template>

    <!-- reset_session -->
    <template v-if="node.type === 'reset_session'">
      <p class="text-xs text-muted-foreground">
        Clears saved custom variables and step retries, then continues via the default edge.
        Built-ins (<code>contact_name</code>, <code>phone_number</code>, …) are re-seeded.
      </p>
    </template>

    <!-- ai_response -->
    <template v-if="node.type === 'ai_response'">
      <div class="space-y-1.5">
        <Label class="text-xs">Prompt template</Label>
        <Textarea
          :model-value="config.prompt_template || ''"
          @update:model-value="(v: string) => updateConfig('prompt_template', v)"
          placeholder="Leave empty to use the latest inbound message"
          class="min-h-[60px] text-xs"
        />
        <p class="text-[10px] text-muted-foreground">Uses org chatbot AI settings (provider/model/key). No API keys in the graph.</p>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Store answer as</Label>
        <Input
          :model-value="config.store_as || ''"
          @update:model-value="(v: string) => updateConfig('store_as', v)"
          placeholder="ai_answer"
          class="h-8 text-sm font-mono"
        />
      </div>
    </template>

    <!-- timing -->
    <template v-if="node.type === 'timing'">
      <div class="space-y-1.5">
        <Label class="text-xs">Schedule</Label>
        <div v-for="(entry, idx) in schedule" :key="idx" class="flex items-center gap-1.5 text-xs">
          <span class="w-12 capitalize">{{ entry.day.slice(0, 3) }}</span>
          <Switch :checked="entry.enabled" @update:checked="(v: boolean) => updateScheduleEntry(Number(idx), 'enabled', v)" />
          <Input
            v-if="entry.enabled"
            type="time"
            :model-value="entry.start_time"
            @update:model-value="(v: string) => updateScheduleEntry(Number(idx), 'start_time', v)"
            class="h-8 text-xs w-28"
          />
          <span v-if="entry.enabled" class="text-muted-foreground">-</span>
          <Input
            v-if="entry.enabled"
            type="time"
            :model-value="entry.end_time"
            @update:model-value="(v: string) => updateScheduleEntry(Number(idx), 'end_time', v)"
            class="h-8 text-xs w-28"
          />
        </div>
        <p class="text-[10px] text-muted-foreground">Routes via <code>in_hours</code> / <code>out_of_hours</code>.</p>
      </div>
    </template>

    <!-- transfer -->
    <template v-if="node.type === 'transfer'">
      <div class="space-y-1.5">
        <Label class="text-xs">Body (sent before handoff)</Label>
        <Textarea
          :model-value="config.body || ''"
          @update:model-value="(v: string) => updateConfig('body', v)"
          placeholder="Connecting you with a human..."
          class="min-h-[50px] text-xs"
        />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Specific agent (optional)</Label>
        <Select
          :model-value="config.user_id || '_none'"
          @update:model-value="(v: any) => updateConfig('user_id', v === '_none' ? '' : v)"
        >
          <SelectTrigger class="h-8 text-sm"><SelectValue placeholder="None — use team/queue" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="_none">None — use team/queue</SelectItem>
            <SelectItem v-for="u in orgUsers" :key="u.id" :value="u.id">
              {{ u.full_name || u.email || u.id }}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div v-if="!config.user_id" class="space-y-1.5">
        <Label class="text-xs">Team</Label>
        <Select :model-value="config.team_id || '_general'" @update:model-value="(v: any) => updateConfig('team_id', v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue placeholder="General queue" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="_general">General queue</SelectItem>
            <SelectItem v-for="team in teamsStore.teams" :key="team.id" :value="team.id">
              {{ team.name }}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Notes (for agents)</Label>
        <Textarea
          :model-value="config.notes || ''"
          @update:model-value="(v: string) => updateConfig('notes', v)"
          placeholder="Customer asked about: {{topic}}"
          class="min-h-[50px] text-xs"
        />
      </div>
    </template>

    <!-- set_labels -->
    <template v-if="node.type === 'set_labels'">
      <div class="space-y-1.5">
        <Label class="text-xs">Add tags (comma-separated)</Label>
        <Input
          :model-value="(config.add || []).join(', ')"
          @update:model-value="(v: string) => updateConfig('add', String(v).split(',').map(s => s.trim()).filter(Boolean))"
          placeholder="vip, lead"
          class="h-8 text-xs"
        />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Remove tags (comma-separated)</Label>
        <Input
          :model-value="(config.remove || []).join(', ')"
          @update:model-value="(v: string) => updateConfig('remove', String(v).split(',').map(s => s.trim()).filter(Boolean))"
          placeholder="new"
          class="h-8 text-xs"
        />
      </div>
      <p class="text-[10px] text-muted-foreground">Updated tags are also stored in session as <code>tags</code> for conditions.</p>
    </template>

    <!-- disable_bot -->
    <template v-if="node.type === 'disable_bot'">
      <div class="grid grid-cols-2 gap-2">
        <div class="space-y-1.5">
          <Label class="text-xs">Hours</Label>
          <Input
            type="number"
            :model-value="String(config.hours ?? 1)"
            @update:model-value="(v: string) => updateConfig('hours', parseInt(v) || 0)"
            class="h-8 text-sm"
            min="0"
          />
        </div>
        <div class="space-y-1.5">
          <Label class="text-xs">Minutes</Label>
          <Input
            type="number"
            :model-value="String(config.minutes ?? 0)"
            @update:model-value="(v: string) => updateConfig('minutes', parseInt(v) || 0)"
            class="h-8 text-sm"
            min="0"
            max="59"
          />
        </div>
      </div>
      <p class="text-[10px] text-muted-foreground">Inbox still receives messages; chatbot automation is skipped until the mute expires.</p>
    </template>

    <!-- send_email -->
    <template v-if="node.type === 'send_email'">
      <p class="text-[10px] text-muted-foreground">Uses org SMTP from Settings → Email. Passwords are never stored on this node.</p>
      <div class="space-y-1.5">
        <Label class="text-xs">To</Label>
        <Input :model-value="config.to || ''" @update:model-value="(v: string) => updateConfig('to', v)" placeholder="agent@example.com" class="h-8 text-xs" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">From (optional)</Label>
        <Input :model-value="config.from || ''" @update:model-value="(v: string) => updateConfig('from', v)" placeholder="Uses org default" class="h-8 text-xs" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Subject</Label>
        <Input :model-value="config.subject || ''" @update:model-value="(v: string) => updateConfig('subject', v)" placeholder="New chat from {{contact_name}}" class="h-8 text-xs" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">HTML body</Label>
        <Textarea :model-value="config.html || ''" @update:model-value="(v: string) => updateConfig('html', v)" class="min-h-[80px] text-xs" />
      </div>
    </template>

    <!-- phonebook -->
    <template v-if="node.type === 'phonebook'">
      <div class="space-y-1.5">
        <Label class="text-xs">Action</Label>
        <Select :model-value="config.action || 'add'" @update:model-value="(v: any) => updateConfig('action', v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="add">Add</SelectItem>
            <SelectItem value="remove">Remove</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">List</Label>
        <Select :model-value="config.list_id || 'none'" @update:model-value="(v: any) => updateConfig('list_id', v === 'none' ? '' : v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue placeholder="Select list" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="none">Select list…</SelectItem>
            <SelectItem v-for="l in contactLists" :key="l.id" :value="l.id">{{ l.name }}</SelectItem>
          </SelectContent>
        </Select>
        <p class="text-[10px] text-muted-foreground">Manage lists under Settings → Contact lists.</p>
      </div>
      <div v-if="(config.action || 'add') === 'remove'" class="flex items-center justify-between">
        <Label class="text-xs">Remove from all lists</Label>
        <Switch :checked="!!config.remove_from_all" @update:checked="(v: boolean) => updateConfig('remove_from_all', v)" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Mobile</Label>
        <Input :model-value="config.mobile || ''" @update:model-value="(v: string) => updateConfig('mobile', v)" class="h-8 text-xs" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Name</Label>
        <Input :model-value="config.name || ''" @update:model-value="(v: string) => updateConfig('name', v)" class="h-8 text-xs" />
      </div>
    </template>

    <!-- google_service -->
    <template v-if="node.type === 'google_service'">
      <div class="space-y-1.5">
        <Label class="text-xs">Credential</Label>
        <Select :model-value="config.credential_id || 'none'" @update:model-value="(v: any) => updateConfig('credential_id', v === 'none' ? '' : v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue placeholder="Select credential" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="none">Select…</SelectItem>
            <SelectItem v-for="c in googleCreds" :key="c.id" :value="c.id">{{ c.label }}</SelectItem>
          </SelectContent>
        </Select>
        <p class="text-[10px] text-muted-foreground">Add service accounts under Settings → Google. Share calendars/files with the client_email.</p>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Action</Label>
        <Select :model-value="config.action || 'sheets_append'" @update:model-value="(v: any) => updateConfig('action', v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="sheets_append">Sheets append</SelectItem>
            <SelectItem value="sheets_read">Sheets read</SelectItem>
            <SelectItem value="sheets_update">Sheets update</SelectItem>
            <SelectItem value="calendar_create_event">Calendar create event</SelectItem>
            <SelectItem value="calendar_list_events">Calendar list events</SelectItem>
            <SelectItem value="calendar_get_event">Calendar get event</SelectItem>
            <SelectItem value="calendar_delete_event">Calendar delete event</SelectItem>
            <SelectItem value="drive_list_files">Drive list files</SelectItem>
            <SelectItem value="drive_create_folder">Drive create folder</SelectItem>
            <SelectItem value="drive_upload_file">Drive upload text file</SelectItem>
            <SelectItem value="docs_create">Docs create</SelectItem>
            <SelectItem value="docs_append">Docs append</SelectItem>
            <SelectItem value="docs_read">Docs read</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <!-- Sheets fields -->
      <template v-if="(config.action || 'sheets_append').startsWith('sheets_') || !config.action">
        <div class="space-y-1.5">
          <Label class="text-xs">Spreadsheet ID</Label>
          <Input :model-value="config.spreadsheet_id || ''" @update:model-value="(v: string) => updateConfig('spreadsheet_id', v)" class="h-8 text-xs" />
        </div>
        <div v-if="(config.action || 'sheets_append') === 'sheets_append'" class="space-y-1.5">
          <Label class="text-xs">Sheet name</Label>
          <Input :model-value="config.sheet_name || 'Sheet1'" @update:model-value="(v: string) => updateConfig('sheet_name', v)" class="h-8 text-xs" />
        </div>
        <div v-if="config.action === 'sheets_read' || config.action === 'sheets_update'" class="space-y-1.5">
          <Label class="text-xs">Range (A1)</Label>
          <Input :model-value="config.range || ''" @update:model-value="(v: string) => updateConfig('range', v)" placeholder="Sheet1!A1:D10" class="h-8 text-xs" />
        </div>
        <div v-if="config.action === 'sheets_append' || config.action === 'sheets_update' || !config.action" class="space-y-1.5">
          <Label class="text-xs">Values JSON (array of rows)</Label>
          <Textarea
            :model-value="typeof config.values === 'string' ? config.values : JSON.stringify(config.values || [['{{contact_name}}','{{phone_number}}']], null, 2)"
            @update:model-value="(v: string) => { try { updateConfig('values', JSON.parse(v)) } catch { updateConfig('values', v) } }"
            class="min-h-[70px] text-xs font-mono"
          />
        </div>
      </template>

      <!-- Calendar fields -->
      <template v-if="(config.action || '').startsWith('calendar_')">
        <div class="space-y-1.5">
          <Label class="text-xs">Calendar ID</Label>
          <Input :model-value="config.calendar_id || ''" @update:model-value="(v: string) => updateConfig('calendar_id', v)" placeholder="primary or email" class="h-8 text-xs" />
        </div>
        <template v-if="config.action === 'calendar_create_event'">
          <div class="space-y-1.5">
            <Label class="text-xs">Summary</Label>
            <Input :model-value="config.event_summary || ''" @update:model-value="(v: string) => updateConfig('event_summary', v)" class="h-8 text-xs" />
          </div>
          <div class="space-y-1.5">
            <Label class="text-xs">Description</Label>
            <Textarea :model-value="config.event_description || ''" @update:model-value="(v: string) => updateConfig('event_description', v)" class="min-h-[50px] text-xs" />
          </div>
          <div class="grid grid-cols-2 gap-2">
            <div class="space-y-1.5">
              <Label class="text-xs">Start (ISO)</Label>
              <Input :model-value="config.start || ''" @update:model-value="(v: string) => updateConfig('start', v)" placeholder="2026-09-21T10:00:00Z" class="h-8 text-xs" />
            </div>
            <div class="space-y-1.5">
              <Label class="text-xs">End (ISO)</Label>
              <Input :model-value="config.end || ''" @update:model-value="(v: string) => updateConfig('end', v)" class="h-8 text-xs" />
            </div>
          </div>
          <div class="space-y-1.5">
            <Label class="text-xs">Timezone</Label>
            <Input :model-value="config.timezone || 'UTC'" @update:model-value="(v: string) => updateConfig('timezone', v)" class="h-8 text-xs" />
          </div>
          <div class="space-y-1.5">
            <Label class="text-xs">Attendees (comma-separated)</Label>
            <Input :model-value="config.attendees || ''" @update:model-value="(v: string) => updateConfig('attendees', v)" class="h-8 text-xs" />
          </div>
        </template>
        <div v-if="config.action === 'calendar_get_event' || config.action === 'calendar_delete_event'" class="space-y-1.5">
          <Label class="text-xs">Event ID</Label>
          <Input :model-value="config.event_id || ''" @update:model-value="(v: string) => updateConfig('event_id', v)" class="h-8 text-xs" />
        </div>
      </template>

      <!-- Drive fields -->
      <template v-if="(config.action || '').startsWith('drive_')">
        <div v-if="config.action !== 'drive_list_files'" class="space-y-1.5">
          <Label class="text-xs">Name</Label>
          <Input :model-value="config.name || ''" @update:model-value="(v: string) => updateConfig('name', v)" class="h-8 text-xs" />
        </div>
        <div class="space-y-1.5">
          <Label class="text-xs">Parent folder ID (optional)</Label>
          <Input :model-value="config.parent_id || ''" @update:model-value="(v: string) => updateConfig('parent_id', v)" class="h-8 text-xs" />
        </div>
        <div v-if="config.action === 'drive_upload_file'" class="space-y-1.5">
          <Label class="text-xs">File content</Label>
          <Textarea :model-value="config.content || ''" @update:model-value="(v: string) => updateConfig('content', v)" class="min-h-[60px] text-xs" />
        </div>
      </template>

      <!-- Docs fields -->
      <template v-if="(config.action || '').startsWith('docs_')">
        <div v-if="config.action === 'docs_create'" class="space-y-1.5">
          <Label class="text-xs">Title</Label>
          <Input :model-value="config.title || ''" @update:model-value="(v: string) => updateConfig('title', v)" class="h-8 text-xs" />
        </div>
        <div v-if="config.action === 'docs_append' || config.action === 'docs_read'" class="space-y-1.5">
          <Label class="text-xs">Document ID</Label>
          <Input :model-value="config.document_id || ''" @update:model-value="(v: string) => updateConfig('document_id', v)" class="h-8 text-xs" />
        </div>
        <div v-if="config.action === 'docs_append'" class="space-y-1.5">
          <Label class="text-xs">Text</Label>
          <Textarea :model-value="config.text || ''" @update:model-value="(v: string) => updateConfig('text', v)" class="min-h-[60px] text-xs" />
        </div>
      </template>

      <div
        v-if="['sheets_read','calendar_create_event','calendar_list_events','calendar_get_event','drive_list_files','drive_create_folder','drive_upload_file','docs_create','docs_read'].includes(config.action)"
        class="space-y-1.5"
      >
        <Label class="text-xs">Output variable</Label>
        <Input :model-value="config.output_variable || ''" @update:model-value="(v: string) => updateConfig('output_variable', v)" placeholder="auto" class="h-8 text-xs" />
      </div>
    </template>

    <!-- end -->
    <template v-if="node.type === 'end'">
      <div class="space-y-1.5">
        <Label class="text-xs">Final message (optional)</Label>
        <Textarea
          :model-value="config.message || ''"
          @update:model-value="(v: string) => updateConfig('message', v)"
          placeholder="Sent when the flow ends. Leave blank for silent terminal."
          class="min-h-[60px] text-xs"
        />
      </div>
    </template>

    <!-- goto_flow -->
    <template v-if="node.type === 'goto_flow'">
      <div class="space-y-1.5">
        <Label class="text-xs">Target flow</Label>
        <Select :model-value="config.flow_id || 'none'" @update:model-value="(v: any) => updateConfig('flow_id', v === 'none' ? '' : v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue placeholder="Select flow" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="none">Select flow…</SelectItem>
            <SelectItem v-for="flow in gotoFlowTargets" :key="flow.id" :value="flow.id">
              {{ flow.name }}
            </SelectItem>
          </SelectContent>
        </Select>
        <p class="text-[10px] text-muted-foreground">Session variables carry forward into the target flow.</p>
      </div>
    </template>

    <!-- whatsapp_flow -->
    <template v-if="node.type === 'whatsapp_flow'">
      <div class="space-y-1.5">
        <Label class="text-xs">Published form</Label>
        <Select :model-value="config.flow_id || ''" @update:model-value="(v: any) => selectWaFlow(String(v))">
          <SelectTrigger class="h-8 text-sm"><SelectValue placeholder="Select published WhatsApp Flow" /></SelectTrigger>
          <SelectContent>
            <SelectItem v-for="f in publishedWaFlows" :key="f.id" :value="f.meta_flow_id">
              {{ f.name }} · {{ f.meta_flow_id }}
            </SelectItem>
          </SelectContent>
        </Select>
        <p class="text-[10px] text-muted-foreground">Only PUBLISHED flows are listed. Submitted fields land in session variables.</p>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Flow ID (advanced)</Label>
        <Input
          :model-value="config.flow_id || ''"
          @update:model-value="(v: string) => updateConfig('flow_id', v)"
          placeholder="Meta flow id"
          class="h-8 text-xs font-mono"
        />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Header</Label>
        <Input
          :model-value="config.header || ''"
          @update:model-value="(v: string) => updateConfig('header', v)"
          class="h-8 text-xs"
        />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Body</Label>
        <Textarea
          :model-value="config.body || ''"
          @update:model-value="(v: string) => updateConfig('body', v)"
          class="min-h-[50px] text-xs"
        />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Footer</Label>
        <Input
          :model-value="config.footer || ''"
          @update:model-value="(v: string) => updateConfig('footer', v)"
          placeholder="Powered by WhatsApp Flows"
          class="h-8 text-xs"
        />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">CTA label</Label>
        <Input
          :model-value="config.cta || ''"
          @update:model-value="(v: string) => updateConfig('cta', v)"
          placeholder="Open form"
          class="h-8 text-xs"
        />
      </div>
    </template>

    <!-- webhook -->
    <template v-if="node.type === 'webhook'">
      <div class="space-y-1.5">
        <Label class="text-xs">URL</Label>
        <Input
          :model-value="config.url || ''"
          @update:model-value="(v: string) => updateConfig('url', v)"
          placeholder="https://example.com/hook"
          class="h-8 text-xs font-mono"
        />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Method</Label>
        <Select :model-value="config.method || 'POST'" @update:model-value="(v: any) => updateConfig('method', v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="GET">GET</SelectItem>
            <SelectItem value="POST">POST</SelectItem>
            <SelectItem value="PUT">PUT</SelectItem>
            <SelectItem value="PATCH">PATCH</SelectItem>
            <SelectItem value="DELETE">DELETE</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div class="space-y-1.5">
        <div class="flex items-center justify-between">
          <Label class="text-xs">Headers</Label>
          <Button variant="outline" size="sm" class="h-6 text-xs" @click="addHeader">
            <Plus class="h-3 w-3 mr-1" /> Add
          </Button>
        </div>
        <div v-for="(val, key) in (config.headers || {})" :key="String(key)" class="flex items-center gap-1">
          <Input :model-value="String(key)" @update:model-value="(v: string) => updateHeaderKey(String(key), v)" placeholder="Key" class="h-7 text-xs flex-1" />
          <Input :model-value="String(val)" @update:model-value="(v: string) => updateHeaderValue(String(key), v)" placeholder="Value" class="h-7 text-xs flex-1" />
          <Button variant="ghost" size="icon" class="h-6 w-6" @click="removeHeader(String(key))">
            <Trash2 class="h-3 w-3 text-destructive" />
          </Button>
        </div>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Body</Label>
        <Textarea
          :model-value="config.body || ''"
          @update:model-value="(v: string) => updateConfig('body', v)"
          placeholder='{{{inbound}}}'
          class="min-h-[50px] text-xs font-mono"
        />
      </div>
      <p class="text-[10px] text-muted-foreground">
        Fires and continues — response ignored. Use <code v-pre>{{{inbound}}}</code> for the full Meta webhook JSON (<code>object</code>/<code>entry</code>/<code>changes</code>), or <code v-pre>{{senderMessage}}</code> for the text body.
      </p>
    </template>

    <!-- Skip condition. Evaluated by the runner before executing the
         node; truthy → fall through via the default edge without
         sending anything.

         Hidden for nodes whose whole purpose is branching (condition /
         buttons / timing — they have no default edge) and terminal
         nodes (end / transfer / goto_flow) where there's nothing to
         skip past. -->
    <div
      v-if="!['start', 'end', 'transfer', 'goto_flow', 'condition', 'buttons', 'list', 'timing'].includes(node.type)"
      class="pt-2 border-t space-y-1.5"
    >
      <Label class="text-xs">Skip condition (optional)</Label>
      <Input
        :model-value="config.skip_condition || ''"
        @update:model-value="(v: string) => updateConfig('skip_condition', v)"
        placeholder='tier == "premium"'
        class="h-8 text-xs font-mono"
      />
      <p class="text-[10px] text-muted-foreground">Skip this node when the expression evaluates truthy — execution continues via the default edge.</p>
    </div>
  </div>
</template>
