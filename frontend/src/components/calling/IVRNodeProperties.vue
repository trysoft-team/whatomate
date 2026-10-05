<script setup lang="ts">
import { computed, ref, watch, onMounted } from 'vue'
import type { IVRNode, IVRNodeType } from '@/services/api'
import { ivrFlowsService, chatbotService } from '@/services/api'
import { useCallingStore } from '@/stores/calling'
import { useTeamsStore } from '@/stores/teams'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Checkbox } from '@/components/ui/checkbox'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Trash2, Plus, Upload, Play, Pause, X, Loader2, Type, Copy, Check } from 'lucide-vue-next'
import { toast } from 'vue-sonner'

const props = defineProps<{
  node: IVRNode
  currentFlowId?: string
}>()

const emit = defineEmits<{
  'update:node': [node: IVRNode]
  'delete': []
}>()

const callingStore = useCallingStore()
const teamsStore = useTeamsStore()

if (teamsStore.teams.length === 0) teamsStore.fetchTeams()

const config = computed(() => props.node.config || {})

const ttsPlaceholder = 'Hi {{caller_name}}, thanks for calling...'
const httpBodyPlaceholder = '{"phone": "{{caller_phone}}", "name": "{{caller_name}}"}'

/** Gemini Live / GeminiTTSService prebuilt voices with genders
 *  (https://docs.cloud.google.com/text-to-speech/docs/gemini-tts). */
const geminiLiveVoices = [
  { name: 'Puck', gender: 'Male' },
  { name: 'Kore', gender: 'Female' },
  { name: 'Aoede', gender: 'Female' },
  { name: 'Charon', gender: 'Male' },
  { name: 'Fenrir', gender: 'Male' },
  { name: 'Leda', gender: 'Female' },
  { name: 'Orus', gender: 'Male' },
  { name: 'Zephyr', gender: 'Female' },
  { name: 'Autonoe', gender: 'Female' },
  { name: 'Enceladus', gender: 'Male' },
  { name: 'Iapetus', gender: 'Male' },
  { name: 'Umbriel', gender: 'Male' },
  { name: 'Algieba', gender: 'Male' },
  { name: 'Despina', gender: 'Female' },
  { name: 'Erinome', gender: 'Female' },
  { name: 'Algenib', gender: 'Male' },
  { name: 'Rasalgethi', gender: 'Male' },
  { name: 'Laomedeia', gender: 'Female' },
  { name: 'Achernar', gender: 'Female' },
  { name: 'Alnilam', gender: 'Male' },
  { name: 'Schedar', gender: 'Male' },
  { name: 'Gacrux', gender: 'Female' },
  { name: 'Pulcherrima', gender: 'Female' },
  { name: 'Achird', gender: 'Male' },
  { name: 'Zubenelgenubi', gender: 'Male' },
  { name: 'Vindemiatrix', gender: 'Female' },
  { name: 'Sadachbia', gender: 'Male' },
  { name: 'Sadaltager', gender: 'Male' },
  { name: 'Sulafat', gender: 'Female' },
  { name: 'Callirrhoe', gender: 'Female' },
] as const

function geminiVoiceLabel(name: string): string {
  const v = geminiLiveVoices.find((x) => x.name === name)
  return v ? `${v.name} · ${v.gender}` : name
}

const previewingGeminiVoice = ref(false)
const geminiPreviewPlaying = ref(false)
const geminiPreviewAudio = ref<HTMLAudioElement | null>(null)
let geminiPreviewObjectUrl: string | null = null

function stopGeminiPreview() {
  if (geminiPreviewAudio.value) {
    geminiPreviewAudio.value.pause()
    geminiPreviewAudio.value = null
  }
  if (geminiPreviewObjectUrl) {
    URL.revokeObjectURL(geminiPreviewObjectUrl)
    geminiPreviewObjectUrl = null
  }
  geminiPreviewPlaying.value = false
}

async function previewGeminiVoice() {
  if (geminiPreviewPlaying.value) {
    stopGeminiPreview()
    return
  }
  const text = String(config.value.welcome_message || '').trim() || 'Hello! How can I help you today?'
  const voice = String(config.value.voice_id || 'Puck')
  previewingGeminiVoice.value = true
  try {
    const res = await ivrFlowsService.previewGeminiVoice({ text, voice })
    const blob = res.data instanceof Blob ? res.data : new Blob([res.data], { type: 'audio/wav' })
    if (blob.type && blob.type.includes('json')) {
      const errText = await blob.text()
      let msg = 'Voice preview failed'
      try {
        const j = JSON.parse(errText)
        msg = j.message || j.error || msg
      } catch { /* ignore */ }
      toast.error(msg)
      return
    }
    stopGeminiPreview()
    geminiPreviewObjectUrl = URL.createObjectURL(blob)
    const audio = new Audio(geminiPreviewObjectUrl)
    audio.onended = () => { geminiPreviewPlaying.value = false }
    audio.onerror = () => {
      geminiPreviewPlaying.value = false
      toast.error('Could not play preview audio')
    }
    geminiPreviewAudio.value = audio
    geminiPreviewPlaying.value = true
    await audio.play()
  } catch (e: any) {
    const msg = e?.response?.data?.message || e?.message || 'Voice preview failed'
    // axios blob errors may need parsing
    if (e?.response?.data instanceof Blob) {
      try {
        const j = JSON.parse(await e.response.data.text())
        toast.error(j.message || j.error || msg)
        return
      } catch { /* fall through */ }
    }
    toast.error(typeof msg === 'string' ? msg : 'Voice preview failed')
  } finally {
    previewingGeminiVoice.value = false
  }
}

function updateConfig(key: string, value: any) {
  emit('update:node', {
    ...props.node,
    config: { ...props.node.config, [key]: value }
  })
}

// Set multiple config keys in a single emit. Calling updateConfig twice in a
// row loses the first change: the prop doesn't update synchronously after
// emit, so the second call rebuilds from a stale props.node and clobbers it.
function updateConfigEntries(entries: Record<string, any>) {
  emit('update:node', {
    ...props.node,
    config: { ...props.node.config, ...entries }
  })
}

function updateLabel(label: string) {
  emit('update:node', { ...props.node, label })
}

// Audio upload state
const audioFileInput = ref<HTMLInputElement | null>(null)
const isUploading = ref(false)
const isPlaying = ref(false)
const audioElement = ref<HTMLAudioElement | null>(null)

function triggerFileUpload() {
  audioFileInput.value?.click()
}

async function handleFileSelect(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  if (file.size > 5 * 1024 * 1024) {
    toast.error('File too large (max 5MB)')
    input.value = ''
    return
  }
  isUploading.value = true
  try {
    const res = await ivrFlowsService.uploadAudio(file)
    const filename = res.data?.data?.filename
    if (filename) {
      // Uploading a clip and TTS text are mutually exclusive; clear greeting_text
      // so the backend doesn't TTS-regenerate and overwrite the uploaded audio_file.
      updateConfigEntries({ audio_file: filename, greeting_text: undefined })
      toast.success('Audio uploaded')
    }
  } catch {
    toast.error('Upload failed')
  } finally {
    isUploading.value = false
    input.value = ''
  }
}

function removeAudio() {
  stopAudio()
  updateConfig('audio_file', '')
}

function togglePlayback() {
  if (isPlaying.value) stopAudio()
  else playAudio()
}

function playAudio() {
  if (!config.value.audio_file) return
  stopAudio()
  const audio = new Audio(ivrFlowsService.getAudioUrl(config.value.audio_file))
  audio.onended = () => { isPlaying.value = false }
  audio.onerror = () => { isPlaying.value = false }
  audio.play()
  audioElement.value = audio
  isPlaying.value = true
}

function stopAudio() {
  if (audioElement.value) {
    audioElement.value.pause()
    audioElement.value = null
  }
  isPlaying.value = false
}

// Menu options helpers
function addMenuOption() {
  const opts = { ...(config.value.options || {}) }
  const used = new Set(Object.keys(opts))
  const digit = ['1','2','3','4','5','6','7','8','9','0','*','#'].find(d => !used.has(d))
  if (!digit) return
  opts[digit] = { label: '' }
  updateConfig('options', opts)
}

function removeMenuOption(digit: string) {
  const opts = { ...(config.value.options || {}) }
  delete opts[digit]
  updateConfig('options', opts)
}

function updateMenuOption(digit: string, field: string, value: string) {
  const opts = { ...(config.value.options || {}) }
  opts[digit] = { ...opts[digit], [field]: value }
  updateConfig('options', opts)
}

// Timing schedule helpers
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

// HTTP headers helpers
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

// Transfer callback helpers
const callbackEvents = ['on_waiting', 'on_connect'] as const
type CallbackEvent = typeof callbackEvents[number]

const callbackLabels: Record<CallbackEvent, string> = {
  on_waiting: 'On Waiting',
  on_connect: 'On Connect',
}

function getCallbackConfig(event: CallbackEvent) {
  return (config.value[event] as Record<string, any>) || {}
}

function updateCallbackField(event: CallbackEvent, field: string, value: any) {
  const cb = { ...getCallbackConfig(event), [field]: value }
  updateConfig(event, cb)
}

function addCallbackHeader(event: CallbackEvent) {
  const cb = getCallbackConfig(event)
  const headers = { ...(cb.headers || {}), '': '' }
  updateCallbackField(event, 'headers', headers)
}

function removeCallbackHeader(event: CallbackEvent, key: string) {
  const cb = getCallbackConfig(event)
  const headers = { ...(cb.headers || {}) }
  delete headers[key]
  updateCallbackField(event, 'headers', headers)
}

function updateCallbackHeaderKey(event: CallbackEvent, oldKey: string, newKey: string) {
  if (oldKey === newKey) return
  const cb = getCallbackConfig(event)
  const headers = { ...(cb.headers || {}) }
  headers[newKey] = headers[oldKey]
  delete headers[oldKey]
  updateCallbackField(event, 'headers', headers)
}

function updateCallbackHeaderValue(event: CallbackEvent, key: string, value: string) {
  const cb = getCallbackConfig(event)
  const headers = { ...(cb.headers || {}) }
  headers[key] = value
  updateCallbackField(event, 'headers', headers)
}

// Goto flow targets
const gotoFlowTargets = computed(() =>
  callingStore.ivrFlows.filter(f => f.id !== props.currentFlowId)
)

// Audio section used by greeting, menu, gather, hangup, confirm, voicemail, speech_input, ai_voice, dial_out
const audioNodeTypes: IVRNodeType[] = ['greeting', 'menu', 'gather', 'hangup', 'confirm', 'voicemail', 'speech_input', 'ai_voice', 'dial_out']
const hasAudio = computed(() => audioNodeTypes.includes(props.node.type))

const greetingTab = computed(() =>
  config.value.greeting_text ? 'text' : 'audio'
)

function setVariableAssignments(): Array<{ name: string; value: string }> {
  const raw = config.value.assignments
  if (Array.isArray(raw) && raw.length > 0) {
    return raw.map((a: any) => ({ name: a?.name || '', value: a?.value || '' }))
  }
  if (config.value.name) {
    return [{ name: String(config.value.name), value: String(config.value.value || '') }]
  }
  return [{ name: '', value: '' }]
}

function updateAssignment(index: number, key: 'name' | 'value', value: string) {
  const next = setVariableAssignments()
  next[index] = { ...next[index], [key]: value }
  updateConfig('assignments', next)
}

function addAssignment() {
  updateConfig('assignments', [...setVariableAssignments(), { name: '', value: '' }])
}

function removeAssignment(index: number) {
  const next = setVariableAssignments().filter((_, i) => i !== index)
  updateConfig('assignments', next.length ? next : [{ name: '', value: '' }])
}

/** Built-ins seeded when a call flow starts (before Start defaults). */
const startBuiltinVariables: { name: string; description: string }[] = [
  { name: 'caller_phone', description: "Caller's WhatsApp phone number" },
  { name: 'caller_name', description: "Caller's WhatsApp profile name" },
  { name: 'call_id', description: 'WhatsApp call ID for this session' },
]

const startAvailableVariables = computed(() => {
  const seen = new Set(startBuiltinVariables.map((v) => v.name))
  const out = [...startBuiltinVariables]
  for (const a of setVariableAssignments()) {
    const name = String(a.name || '').trim()
    if (!name || seen.has(name)) continue
    seen.add(name)
    out.push({ name, description: 'Default variable from Start' })
  }
  return out
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

type AIContextOption = {
  id: string
  name: string
  context_type?: string
  enabled?: boolean
  is_enabled?: boolean
  priority?: number
}

const aiContextOptions = ref<AIContextOption[]>([])
const aiContextsLoading = ref(false)
let aiContextsFetched = false

async function ensureAIContextsLoaded() {
  if (aiContextsFetched || aiContextsLoading.value) return
  aiContextsLoading.value = true
  try {
    const res = await chatbotService.listAIContexts({ limit: 200 })
    const data = (res.data as any)?.data || res.data
    const list = data?.contexts || data || []
    aiContextOptions.value = (Array.isArray(list) ? list : []).map((c: any) => ({
      id: String(c.id),
      name: String(c.name || 'Untitled'),
      context_type: c.context_type,
      enabled: c.enabled ?? c.is_enabled,
      is_enabled: c.is_enabled ?? c.enabled,
      priority: c.priority,
    }))
    aiContextsFetched = true
  } catch (e) {
    console.error('Failed to load AI contexts', e)
  } finally {
    aiContextsLoading.value = false
  }
}

const aiContextMode = computed(() => {
  const mode = String(config.value.ai_context_mode || '').toLowerCase()
  if (mode === 'selected') return 'selected'
  const ids = Array.isArray(config.value.ai_context_ids) ? config.value.ai_context_ids : []
  if (ids.length > 0) return 'selected'
  return 'all'
})

const selectedAIContextIds = computed((): string[] => {
  const raw = config.value.ai_context_ids
  if (!Array.isArray(raw)) return []
  return raw.map((id: any) => String(id)).filter(Boolean)
})

function setAIContextMode(mode: 'all' | 'selected') {
  if (mode === 'all') {
    const next = { ...props.node.config, ai_context_mode: 'all' }
    delete next.ai_context_ids
    emit('update:node', { ...props.node, config: next })
    return
  }
  updateConfigEntries({
    ai_context_mode: 'selected',
    ai_context_ids: Array.isArray(config.value.ai_context_ids) ? config.value.ai_context_ids : [],
  })
}

function toggleAIContextId(id: string, checked: boolean) {
  const set = new Set(selectedAIContextIds.value)
  if (checked) set.add(id)
  else set.delete(id)
  updateConfigEntries({
    ai_context_mode: 'selected',
    ai_context_ids: Array.from(set),
  })
}

function isAIContextSelected(id: string) {
  return selectedAIContextIds.value.includes(id)
}

function extraKnowledgeRows(): Array<{ name: string; value: string }> {
  const raw = config.value.extra_knowledge
  if (Array.isArray(raw) && raw.length > 0) {
    return raw.map((a: any) => ({ name: a?.name || '', value: a?.value || '' }))
  }
  return [{ name: '', value: '' }]
}

function updateExtraKnowledge(index: number, key: 'name' | 'value', value: string) {
  const next = extraKnowledgeRows()
  next[index] = { ...next[index], [key]: value }
  updateConfig('extra_knowledge', next)
}

function addExtraKnowledge() {
  updateConfig('extra_knowledge', [...extraKnowledgeRows(), { name: '', value: '' }])
}

function removeExtraKnowledge(index: number) {
  const next = extraKnowledgeRows().filter((_, i) => i !== index)
  updateConfig('extra_knowledge', next.length ? next : [{ name: '', value: '' }])
}

watch(
  () => props.node.type,
  (type) => {
    if (type === 'ai_call') ensureAIContextsLoaded()
  },
  { immediate: true },
)

onMounted(() => {
  if (props.node.type === 'ai_call') ensureAIContextsLoaded()
})

type AICallTool = {
  name: string
  description: string
  handler: string
  /** UX flag: after the tool runs, AI Call continues. Canvas edges are only for handler=nodes. */
  return_to_call?: boolean
  parameters?: Record<string, any>
  http?: { url?: string; method?: string; body_template?: string; headers?: Record<string, string> }
}

const aiCallTools = computed((): AICallTool[] => {
  const raw = config.value.tools
  if (!Array.isArray(raw)) return []
  return raw.map((t: any) => {
    const handler = t?.handler || 'http'
    return {
      name: t?.name || '',
      description: t?.description || '',
      handler,
      // Default on for nodes; otherwise respect saved flag
      return_to_call: handler === 'nodes' ? (t?.return_to_call !== false) : !!t?.return_to_call,
      parameters: t?.parameters,
      http: t?.http || {},
    }
  })
})

function setAICallTools(next: AICallTool[]) {
  updateConfig('tools', next)
}

function addAICallTool() {
  setAICallTools([
    ...aiCallTools.value,
    {
      name: '',
      description: '',
      handler: 'http',
      return_to_call: false,
      parameters: { type: 'object', properties: {} },
      http: { url: '', method: 'POST', body_template: '', headers: {} },
    },
  ])
}

function removeAICallTool(index: number) {
  const next = [...aiCallTools.value]
  next.splice(index, 1)
  setAICallTools(next)
}

function updateAICallTool(index: number, key: keyof AICallTool, value: string | boolean) {
  const next = aiCallTools.value.map((t, i) => {
    if (i !== index) return t
    const updated = { ...t, [key]: value }
    // Selecting Nodes implies return-to-call (subgraph resumes AI Call)
    if (key === 'handler' && value === 'nodes') {
      updated.return_to_call = true
    }
    return updated
  })
  setAICallTools(next)
}

function updateAICallToolHTTP(index: number, key: string, value: string) {
  const next = aiCallTools.value.map((t, i) => {
    if (i !== index) return t
    return { ...t, http: { ...(t.http || {}), [key]: value } }
  })
  setAICallTools(next)
}
</script>

<template>
  <div class="space-y-4 p-4">
    <div class="flex items-center justify-between">
      <h3 class="font-semibold text-sm capitalize">{{ node.type.replace('_', ' ') }}</h3>
      <Button
        v-if="node.type !== 'start'"
        variant="ghost"
        size="icon"
        class="h-7 w-7"
        @click="emit('delete')"
      >
        <Trash2 class="h-3.5 w-3.5 text-destructive" />
      </Button>
    </div>

    <!-- Label -->
    <div class="space-y-1.5">
      <Label class="text-xs">Label</Label>
      <Input :model-value="node.label" @update:model-value="updateLabel" class="h-8 text-sm" />
    </div>

    <template v-if="node.type === 'start'">
      <p class="text-xs text-muted-foreground">
        Fixed entry point for this call flow. Connect its output to the first action node (greeting, menu, AI Call, …).
      </p>
      <div class="space-y-1.5">
        <div class="flex items-center justify-between">
          <Label class="text-xs">Default variables</Label>
          <Button variant="outline" size="sm" class="h-6 text-xs" @click="addAssignment">
            <Plus class="h-3 w-3 mr-1" /> Add
          </Button>
        </div>
        <p class="text-[10px] text-muted-foreground">
          Seeded after builtins when the call starts. Values may use
          <code v-pre class="bg-muted px-1 py-0.5 rounded">{{caller_name}}</code> etc.
          Cannot overwrite <code class="font-mono">caller_phone</code>,
          <code class="font-mono">caller_name</code>, or <code class="font-mono">call_id</code>.
        </p>
        <div v-for="(a, i) in setVariableAssignments()" :key="i" class="flex items-center gap-1.5">
          <Input :model-value="a.name" @update:model-value="(v: string) => updateAssignment(i, 'name', v)" placeholder="name" class="h-7 text-xs w-28 font-mono" />
          <Input :model-value="a.value" @update:model-value="(v: string) => updateAssignment(i, 'value', v)" placeholder="value or {{var}}" class="h-7 text-xs flex-1 font-mono" />
          <Button variant="ghost" size="icon" class="h-6 w-6" @click="removeAssignment(i)">
            <Trash2 class="h-3 w-3 text-destructive" />
          </Button>
        </div>
      </div>
      <div class="space-y-1.5 pt-1">
        <Label class="text-xs">Available variables</Label>
        <p class="text-[10px] text-muted-foreground">
          Click to copy. Use <code v-pre>{{name}}</code> in TTS, HTTP, AI Call, and other fields.
        </p>
        <div class="flex flex-wrap gap-1.5">
          <button
            v-for="v in startAvailableVariables"
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

    <!-- Audio Section (greeting, menu, gather, hangup) -->
    <div v-if="hasAudio" class="space-y-1.5">
      <Label class="text-xs">Audio</Label>
      <Tabs :default-value="greetingTab">
        <TabsList class="h-8">
          <TabsTrigger value="audio" class="text-xs h-7 px-2">
            <Upload class="h-3 w-3 mr-1" /> Upload
          </TabsTrigger>
          <TabsTrigger value="text" class="text-xs h-7 px-2">
            <Type class="h-3 w-3 mr-1" /> TTS
          </TabsTrigger>
        </TabsList>
        <TabsContent value="audio" class="mt-2">
          <div class="flex items-center gap-2">
            <div v-if="config.audio_file" class="flex items-center gap-1 flex-1 min-w-0 px-2 py-1 border rounded-md bg-muted/50">
              <Button variant="ghost" size="icon" class="h-5 w-5 shrink-0" @click="togglePlayback">
                <Pause v-if="isPlaying" class="h-3 w-3" />
                <Play v-else class="h-3 w-3" />
              </Button>
              <span class="text-xs truncate">{{ config.audio_file }}</span>
              <Button variant="ghost" size="icon" class="h-5 w-5 shrink-0 ml-auto" @click="removeAudio">
                <X class="h-3 w-3 text-destructive" />
              </Button>
            </div>
            <Button v-else variant="outline" size="sm" class="h-7 text-xs w-full" @click="triggerFileUpload" :disabled="isUploading">
              <Loader2 v-if="isUploading" class="h-3 w-3 mr-1 animate-spin" />
              <Upload v-else class="h-3 w-3 mr-1" />
              Upload Audio
            </Button>
            <input ref="audioFileInput" type="file" accept="audio/*" class="hidden" @change="handleFileSelect" />
          </div>
        </TabsContent>
        <TabsContent value="text" class="mt-2">
          <Textarea
            :model-value="config.greeting_text || ''"
            @update:model-value="(v: string) => updateConfigEntries(v ? { greeting_text: v, audio_file: undefined } : { greeting_text: v })"
            :placeholder="ttsPlaceholder"
            class="min-h-[60px] text-xs resize-none"
            :maxlength="500"
          />
          <p class="text-[10px] text-muted-foreground mt-1">
            Variables:
            <code v-pre class="bg-muted px-1 py-0.5 rounded">{{caller_name}}</code>
            <code v-pre class="bg-muted px-1 py-0.5 rounded">{{caller_phone}}</code>
          </p>
        </TabsContent>
      </Tabs>
    </div>

    <!-- Greeting: interruptible -->
    <div v-if="node.type === 'greeting'" class="flex items-center gap-2">
      <Switch :checked="!!config.interruptible" @update:checked="(v: boolean) => updateConfig('interruptible', v)" />
      <Label class="text-xs">Interruptible by DTMF</Label>
    </div>

    <!-- Menu: options -->
    <template v-if="node.type === 'menu'">
      <div class="space-y-1.5">
        <Label class="text-xs">Timeout (seconds)</Label>
        <Input type="number" :model-value="String(config.timeout_seconds || 10)" @update:model-value="(v: string) => updateConfig('timeout_seconds', parseInt(v) || 10)" class="h-8 text-sm" min="1" max="60" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Max Retries</Label>
        <Input type="number" :model-value="String(config.max_retries || 3)" @update:model-value="(v: string) => updateConfig('max_retries', parseInt(v) || 3)" class="h-8 text-sm" min="1" max="10" />
      </div>
      <div class="space-y-1.5">
        <div class="flex items-center justify-between">
          <Label class="text-xs">Menu Options</Label>
          <Button variant="outline" size="sm" class="h-6 text-xs" @click="addMenuOption">
            <Plus class="h-3 w-3 mr-1" /> Add
          </Button>
        </div>
        <div v-for="(opt, digit) in (config.options || {})" :key="String(digit)" class="flex items-center gap-1.5">
          <span class="font-mono text-xs font-bold w-5 text-center">{{ digit }}</span>
          <Input :model-value="(opt as any)?.label || ''" @update:model-value="(v: string) => updateMenuOption(String(digit), 'label', v)" placeholder="Label" class="h-7 text-xs flex-1" />
          <Button variant="ghost" size="icon" class="h-6 w-6" @click="removeMenuOption(String(digit))">
            <Trash2 class="h-3 w-3 text-destructive" />
          </Button>
        </div>
      </div>
    </template>

    <!-- Gather: config -->
    <template v-if="node.type === 'gather'">
      <div class="space-y-1.5">
        <Label class="text-xs">Max Digits</Label>
        <Input type="number" :model-value="String(config.max_digits || 10)" @update:model-value="(v: string) => updateConfig('max_digits', parseInt(v) || 10)" class="h-8 text-sm" min="1" max="20" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Terminator</Label>
        <Input :model-value="config.terminator || '#'" @update:model-value="(v: string) => updateConfig('terminator', v)" class="h-8 text-sm" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Store As (variable name)</Label>
        <Input :model-value="config.store_as || ''" @update:model-value="(v: string) => updateConfig('store_as', v)" placeholder="e.g. account_number" class="h-8 text-sm" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Timeout (seconds)</Label>
        <Input type="number" :model-value="String(config.timeout_seconds || 10)" @update:model-value="(v: string) => updateConfig('timeout_seconds', parseInt(v) || 10)" class="h-8 text-sm" min="1" max="60" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Max Retries</Label>
        <Input type="number" :model-value="String(config.max_retries || 3)" @update:model-value="(v: string) => updateConfig('max_retries', parseInt(v) || 3)" class="h-8 text-sm" min="1" max="10" />
      </div>
    </template>

    <!-- Confirm: playback + DTMF -->
    <template v-if="node.type === 'confirm'">
      <div class="space-y-1.5">
        <Label class="text-xs">Variable to confirm</Label>
        <Input :model-value="config.variable || ''" @update:model-value="(v: string) => updateConfig('variable', v)" placeholder="e.g. account_number" class="h-8 text-sm" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Prompt template</Label>
        <Textarea
          :model-value="config.prompt_template || ''"
          @update:model-value="(v: string) => updateConfig('prompt_template', v)"
          class="min-h-[60px] text-xs resize-none"
          placeholder="You entered {{value}}. Press {{confirm_digit}} to confirm..."
        />
        <p class="text-[10px] text-muted-foreground">
          Used when no uploaded audio / TTS text is set. Vars:
          <code v-pre class="bg-muted px-1 py-0.5 rounded">{{value}}</code>
          <code v-pre class="bg-muted px-1 py-0.5 rounded">{{confirm_digit}}</code>
          <code v-pre class="bg-muted px-1 py-0.5 rounded">{{retry_digit}}</code>
        </p>
      </div>
      <div class="grid grid-cols-2 gap-2">
        <div class="space-y-1.5">
          <Label class="text-xs">Confirm digit</Label>
          <Input :model-value="config.confirm_digit || '1'" @update:model-value="(v: string) => updateConfig('confirm_digit', v)" class="h-8 text-sm" maxlength="1" />
        </div>
        <div class="space-y-1.5">
          <Label class="text-xs">Retry digit</Label>
          <Input :model-value="config.retry_digit || '2'" @update:model-value="(v: string) => updateConfig('retry_digit', v)" class="h-8 text-sm" maxlength="1" />
        </div>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Timeout (seconds)</Label>
        <Input type="number" :model-value="String(config.timeout_seconds || 10)" @update:model-value="(v: string) => updateConfig('timeout_seconds', parseInt(v) || 10)" class="h-8 text-sm" min="1" max="60" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Max Retries</Label>
        <Input type="number" :model-value="String(config.max_retries || 3)" @update:model-value="(v: string) => updateConfig('max_retries', parseInt(v) || 3)" class="h-8 text-sm" min="1" max="10" />
      </div>
    </template>

    <!-- Condition -->
    <template v-if="node.type === 'condition'">
      <div class="space-y-1.5">
        <Label class="text-xs">Field / variable</Label>
        <Input :model-value="config.field || ''" @update:model-value="(v: string) => updateConfig('field', v)" placeholder="account_number or {{account_number}}" class="h-8 text-sm font-mono" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Operator</Label>
        <Select :model-value="config.operator || 'equals'" @update:model-value="(v: any) => updateConfig('operator', v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="equals">Equals</SelectItem>
            <SelectItem value="not_equals">Not equals</SelectItem>
            <SelectItem value="contains">Contains</SelectItem>
            <SelectItem value="not_contains">Not contains</SelectItem>
            <SelectItem value="starts_with">Starts with</SelectItem>
            <SelectItem value="ends_with">Ends with</SelectItem>
            <SelectItem value="empty">Is empty</SelectItem>
            <SelectItem value="not_empty">Is not empty</SelectItem>
            <SelectItem value="regex">Regex</SelectItem>
            <SelectItem value="gt">Number &gt;</SelectItem>
            <SelectItem value="lt">Number &lt;</SelectItem>
            <SelectItem value="gte">Number ≥</SelectItem>
            <SelectItem value="lte">Number ≤</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div v-if="!['empty', 'not_empty'].includes(config.operator || 'equals')" class="space-y-1.5">
        <Label class="text-xs">Value</Label>
        <Input :model-value="config.value || ''" @update:model-value="(v: string) => updateConfig('value', v)" class="h-8 text-sm" />
      </div>
      <div class="flex items-center gap-2">
        <Switch :checked="!!config.case_sensitive" @update:checked="(v: boolean) => updateConfig('case_sensitive', v)" />
        <Label class="text-xs">Case sensitive</Label>
      </div>
    </template>

    <!-- Set Variable -->
    <template v-if="node.type === 'set_variable'">
      <div class="space-y-1.5">
        <div class="flex items-center justify-between">
          <Label class="text-xs">Assignments</Label>
          <Button variant="outline" size="sm" class="h-6 text-xs" @click="addAssignment">
            <Plus class="h-3 w-3 mr-1" /> Add
          </Button>
        </div>
        <div v-for="(a, i) in setVariableAssignments()" :key="i" class="flex items-center gap-1.5">
          <Input :model-value="a.name" @update:model-value="(v: string) => updateAssignment(i, 'name', v)" placeholder="name" class="h-7 text-xs w-28 font-mono" />
          <Input :model-value="a.value" @update:model-value="(v: string) => updateAssignment(i, 'value', v)" placeholder="value or {{var}}" class="h-7 text-xs flex-1 font-mono" />
          <Button variant="ghost" size="icon" class="h-6 w-6" @click="removeAssignment(i)">
            <Trash2 class="h-3 w-3 text-destructive" />
          </Button>
        </div>
      </div>
    </template>

    <!-- Delay -->
    <template v-if="node.type === 'delay'">
      <div class="space-y-1.5">
        <Label class="text-xs">Seconds</Label>
        <Input type="number" :model-value="String(config.seconds ?? 3)" @update:model-value="(v: string) => updateConfig('seconds', parseInt(v) || 0)" class="h-8 text-sm" min="0" max="300" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Mode</Label>
        <Select :model-value="config.mode || 'silence'" @update:model-value="(v: any) => updateConfig('mode', v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="silence">Silence</SelectItem>
            <SelectItem value="hold_music">Hold music</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div v-if="config.mode === 'hold_music'" class="space-y-1.5">
        <Label class="text-xs">Audio file (optional override)</Label>
        <Input :model-value="config.audio_file || ''" @update:model-value="(v: string) => updateConfig('audio_file', v)" placeholder="hold.ogg" class="h-8 text-sm" />
      </div>
    </template>

    <!-- Set Labels -->
    <template v-if="node.type === 'set_labels'">
      <div class="space-y-1.5">
        <Label class="text-xs">Add labels (comma-separated)</Label>
        <Input
          :model-value="Array.isArray(config.add) ? config.add.join(', ') : (config.add || '')"
          @update:model-value="(v: string) => updateConfig('add', v.split(',').map((s: string) => s.trim()).filter(Boolean))"
          placeholder="vip, callback"
          class="h-8 text-sm"
        />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Remove labels (comma-separated)</Label>
        <Input
          :model-value="Array.isArray(config.remove) ? config.remove.join(', ') : (config.remove || '')"
          @update:model-value="(v: string) => updateConfig('remove', v.split(',').map((s: string) => s.trim()).filter(Boolean))"
          placeholder="lead"
          class="h-8 text-sm"
        />
      </div>
    </template>

    <!-- Send Message -->
    <template v-if="node.type === 'send_message'">
      <div class="space-y-1.5">
        <Label class="text-xs">To (phone)</Label>
        <Input
          :model-value="config.to || ''"
          @update:model-value="(v: string) => updateConfig('to', v)"
          class="h-8 text-xs font-mono"
          placeholder="Empty = caller phone"
        />
        <p class="text-[10px] text-muted-foreground">
          WhatsApp number. Leave empty (or use a missing variable) to use the caller’s phone.
          Mid-call Nodes tools can pass <code class="font-mono">phone</code> / <code class="font-mono">message</code> as arguments.
        </p>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Message body</Label>
        <Textarea
          :model-value="config.body || ''"
          @update:model-value="(v: string) => updateConfig('body', v)"
          class="min-h-[80px] text-xs resize-none"
          placeholder="Thanks {{caller_name}}, we received your request."
        />
        <p class="text-[10px] text-muted-foreground">
          WhatsApp text for this organization. Use flow variables; leave body mostly empty only if a Nodes tool will pass
          <code class="font-mono">message</code> or fill name/date/time vars.
        </p>
      </div>
    </template>

    <!-- Webhook -->
    <template v-if="node.type === 'webhook'">
      <div class="space-y-1.5">
        <Label class="text-xs">URL</Label>
        <Input :model-value="config.url || ''" @update:model-value="(v: string) => updateConfig('url', v)" placeholder="https://hooks.example.com/ivr" class="h-8 text-xs font-mono" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Method</Label>
        <Select :model-value="config.method || 'POST'" @update:model-value="(v: any) => updateConfig('method', v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="GET">GET</SelectItem>
            <SelectItem value="POST">POST</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Body template</Label>
        <Textarea :model-value="config.body_template || ''" @update:model-value="(v: string) => updateConfig('body_template', v)" class="min-h-[60px] text-xs font-mono resize-none" />
      </div>
      <p class="text-[10px] text-muted-foreground">Fire-and-forget: does not wait for the response or branch on status.</p>
    </template>

    <!-- Language -->
    <template v-if="node.type === 'language'">
      <div class="space-y-1.5">
        <Label class="text-xs">Locale</Label>
        <Input :model-value="config.locale || ''" @update:model-value="(v: string) => updateConfig('locale', v)" placeholder="en, en-US, fr, ..." class="h-8 text-sm" />
        <p class="text-[10px] text-muted-foreground">Stored on the call session as locale for later prompts / TTS selection.</p>
      </div>
    </template>

    <!-- Voicemail -->
    <template v-if="node.type === 'voicemail'">
      <div class="space-y-1.5">
        <Label class="text-xs">Max seconds</Label>
        <Input type="number" :model-value="String(config.max_seconds ?? 60)" @update:model-value="(v: string) => updateConfig('max_seconds', parseInt(v) || 60)" class="h-8 text-sm" min="1" max="300" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Terminator digit</Label>
        <Input :model-value="config.terminator || '#'" @update:model-value="(v: string) => updateConfig('terminator', v)" class="h-8 text-sm" maxlength="1" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Store as</Label>
        <Input :model-value="config.store_as || 'voicemail_key'" @update:model-value="(v: string) => updateConfig('store_as', v)" class="h-8 text-sm font-mono" />
      </div>
      <div class="flex items-center gap-2">
        <Switch :checked="config.beep !== false" @update:checked="(v: boolean) => updateConfig('beep', v)" />
        <Label class="text-xs">Beep pause before record</Label>
      </div>
    </template>

    <!-- Speech input -->
    <template v-if="node.type === 'speech_input'">
      <div class="space-y-1.5">
        <Label class="text-xs">Max seconds</Label>
        <Input type="number" :model-value="String(config.max_seconds ?? 10)" @update:model-value="(v: string) => updateConfig('max_seconds', parseInt(v) || 10)" class="h-8 text-sm" min="1" max="60" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Store transcript as</Label>
        <Input :model-value="config.store_as || 'speech_text'" @update:model-value="(v: string) => updateConfig('store_as', v)" class="h-8 text-sm font-mono" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Language (optional)</Label>
        <Input :model-value="config.language || ''" @update:model-value="(v: string) => updateConfig('language', v)" placeholder="en" class="h-8 text-sm" />
        <p class="text-[10px] text-muted-foreground">Uses OpenAI Whisper (org chatbot OpenAI key or server ai.openai_key).</p>
      </div>
    </template>

    <!-- AI Voice -->
    <template v-if="node.type === 'ai_voice'">
      <div class="space-y-1.5">
        <Label class="text-xs">Input variable</Label>
        <Input :model-value="config.input_variable || ''" @update:model-value="(v: string) => updateConfig('input_variable', v)" placeholder="speech_text" class="h-8 text-sm font-mono" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Listen seconds (0 = use input var only)</Label>
        <Input type="number" :model-value="String(config.listen_seconds ?? 0)" @update:model-value="(v: string) => updateConfig('listen_seconds', parseInt(v) || 0)" class="h-8 text-sm" min="0" max="60" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Store reply as</Label>
        <Input :model-value="config.store_as || 'ai_reply'" @update:model-value="(v: string) => updateConfig('store_as', v)" class="h-8 text-sm font-mono" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">System prompt override</Label>
        <Textarea :model-value="config.system_prompt || ''" @update:model-value="(v: string) => updateConfig('system_prompt', v)" class="min-h-[60px] text-xs resize-none" />
      </div>
      <div class="flex items-center gap-2">
        <Switch :checked="!!config.interruptible" @update:checked="(v: boolean) => updateConfig('interruptible', v)" />
        <Label class="text-xs">Interruptible TTS reply</Label>
      </div>
    </template>

    <!-- AI Call (multi-turn / realtime) -->
    <template v-if="node.type === 'ai_call'">
      <div class="space-y-1.5">
        <Label class="text-xs font-semibold">Welcome Message</Label>
        <Textarea :model-value="config.welcome_message || ''" @update:model-value="(v: string) => updateConfig('welcome_message', v)" class="min-h-[60px] text-xs resize-none" placeholder="Hello! How can I help?" />
        <p class="text-[10px] text-muted-foreground">Spoken as the first greeting when the call connects (Gemini Live paraphrases it naturally).</p>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs font-semibold">AI</Label>
        <p class="text-xs text-muted-foreground rounded-md border bg-muted/40 px-2.5 py-2">
          Uses organization AI from
          <a href="/settings/ai" class="underline underline-offset-2 hover:text-foreground">Settings → AI</a>
          (Google/Gemini API key). With Pipecat enabled, this node runs a
          <span class="text-foreground/90">Gemini Live</span>
          realtime voice session (continuous audio — no STT→LLM→TTS round-trip). Without Pipecat, it falls back to turn-based speech.
        </p>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs font-semibold">Knowledge</Label>
        <p class="text-[10px] text-muted-foreground">
          Choose
          <a href="/chatbot/ai" class="underline underline-offset-2 hover:text-foreground">AI Contexts</a>
          to merge into the session (static + API). Disabled contexts are skipped.
        </p>
        <div class="rounded-md border bg-muted/40 p-2.5 space-y-2">
          <label class="flex items-center gap-2 cursor-pointer">
            <input
              type="radio"
              class="h-3.5 w-3.5"
              :checked="aiContextMode === 'all'"
              @change="setAIContextMode('all')"
            />
            <span class="text-xs">All enabled contexts</span>
          </label>
          <label class="flex items-center gap-2 cursor-pointer">
            <input
              type="radio"
              class="h-3.5 w-3.5"
              :checked="aiContextMode === 'selected'"
              @change="setAIContextMode('selected')"
            />
            <span class="text-xs">Selected contexts</span>
          </label>
          <div v-if="aiContextMode === 'selected'" class="space-y-1.5 pt-1 border-t border-border/60">
            <p v-if="aiContextsLoading" class="text-[10px] text-muted-foreground">Loading contexts…</p>
            <p v-else-if="aiContextOptions.length === 0" class="text-[10px] text-muted-foreground">
              No AI Contexts yet.
              <a href="/chatbot/ai" class="underline underline-offset-2 hover:text-foreground">Create one</a>
            </p>
            <div
              v-for="c in aiContextOptions"
              :key="c.id"
              class="flex items-start gap-2 py-0.5"
            >
              <Checkbox
                class="mt-0.5"
                :checked="isAIContextSelected(c.id)"
                @update:checked="(v: boolean) => toggleAIContextId(c.id, !!v)"
              />
              <div class="min-w-0 flex-1">
                <div class="text-xs truncate">{{ c.name }}</div>
                <div class="text-[10px] text-muted-foreground flex gap-1.5 flex-wrap">
                  <span class="capitalize">{{ c.context_type || 'static' }}</span>
                  <span v-if="!(c.enabled ?? c.is_enabled)" class="text-amber-600">disabled</span>
                </div>
              </div>
            </div>
            <p v-if="aiContextMode === 'selected' && selectedAIContextIds.length === 0" class="text-[10px] text-muted-foreground">
              None selected — no knowledge block will be injected.
            </p>
          </div>
        </div>
        <div class="space-y-1.5 pt-1">
          <div class="flex items-center justify-between">
            <Label class="text-xs">Extra knowledge</Label>
            <Button variant="outline" size="sm" class="h-6 text-xs" @click="addExtraKnowledge">
              <Plus class="h-3 w-3 mr-1" /> Add
            </Button>
          </div>
          <p class="text-[10px] text-muted-foreground">
            Named facts injected into the AI prompt. Value can be literal text or a session variable
            (e.g. <code v-pre class="bg-muted px-1 py-0.5 rounded">{{caller_name}}</code>).
          </p>
          <div v-for="(row, i) in extraKnowledgeRows()" :key="i" class="flex items-center gap-1.5">
            <Input
              :model-value="row.name"
              @update:model-value="(v: string) => updateExtraKnowledge(i, 'name', v)"
              placeholder="Caller name"
              class="h-7 text-xs w-32"
            />
            <Input
              :model-value="row.value"
              @update:model-value="(v: string) => updateExtraKnowledge(i, 'value', v)"
              :placeholder="'Test User or {{caller_name}}'"
              class="h-7 text-xs flex-1 font-mono"
            />
            <Button variant="ghost" size="icon" class="h-6 w-6" @click="removeExtraKnowledge(i)">
              <Trash2 class="h-3 w-3 text-destructive" />
            </Button>
          </div>
        </div>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs font-semibold">System Instructions</Label>
        <Textarea :model-value="config.system_instructions || ''" @update:model-value="(v: string) => updateConfig('system_instructions', v)" class="min-h-[80px] text-xs resize-none" placeholder="Optional override — leave blank to use org AI system prompt" />
        <p class="text-[10px] text-muted-foreground">Blank = org system prompt from Settings → AI. Selected Knowledge contexts are merged into the Live session instructions.</p>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs font-semibold">Voice</Label>
        <Select
          :model-value="config.voice_provider || 'gemini'"
          @update:model-value="(v: any) => updateConfig('voice_provider', v)"
        >
          <SelectTrigger class="h-8 text-sm"><SelectValue placeholder="Gemini Live" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="gemini">Gemini Live (recommended)</SelectItem>
            <SelectItem value="openai">OpenAI TTS (turn-based fallback)</SelectItem>
            <SelectItem value="elevenlabs">ElevenLabs (turn-based fallback)</SelectItem>
            <SelectItem value="piper">Piper local (turn-based fallback)</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div v-if="!config.voice_provider || config.voice_provider === 'gemini'" class="space-y-1.5">
        <Label class="text-xs">Gemini voice</Label>
        <div class="flex gap-1.5 items-center">
          <Select
            :model-value="config.voice_id || 'Puck'"
            @update:model-value="(v: any) => updateConfig('voice_id', v)"
          >
            <SelectTrigger class="h-8 text-sm flex-1">
              <SelectValue>
                {{ geminiVoiceLabel(config.voice_id || 'Puck') }}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="v in geminiLiveVoices" :key="v.name" :value="v.name">
                {{ v.name }} · {{ v.gender }}
              </SelectItem>
            </SelectContent>
          </Select>
          <Button
            type="button"
            variant="outline"
            size="sm"
            class="h-8 w-8 p-0 shrink-0"
            :disabled="previewingGeminiVoice"
            title="Preview voice using welcome message"
            @click="previewGeminiVoice"
          >
            <Loader2 v-if="previewingGeminiVoice" class="h-3.5 w-3.5 animate-spin" />
            <Pause v-else-if="geminiPreviewPlaying" class="h-3.5 w-3.5" />
            <Play v-else class="h-3.5 w-3.5" />
          </Button>
        </div>
        <p class="text-[10px] text-muted-foreground">Preview plays the welcome message with this voice.</p>
      </div>
      <div v-if="config.voice_provider === 'openai'" class="space-y-1.5">
        <Label class="text-xs">OpenAI voice</Label>
        <Select :model-value="config.openai_voice || 'alloy'" @update:model-value="(v: any) => updateConfig('openai_voice', v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="alloy">alloy</SelectItem>
            <SelectItem value="echo">echo</SelectItem>
            <SelectItem value="fable">fable</SelectItem>
            <SelectItem value="onyx">onyx</SelectItem>
            <SelectItem value="nova">nova</SelectItem>
            <SelectItem value="shimmer">shimmer</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div v-if="config.voice_provider === 'elevenlabs'" class="space-y-1.5">
        <Label class="text-xs">ElevenLabs voice ID</Label>
        <Input :model-value="config.voice_id || ''" @update:model-value="(v: string) => updateConfig('voice_id', v)" class="h-8 text-sm font-mono" placeholder="21m00Tcm4TlvDq8ikWAM" />
      </div>
      <div class="space-y-1.5 pt-1 border-t">
        <Label class="text-xs font-semibold text-muted-foreground">Turn-based fallback</Label>
        <p class="text-[10px] text-muted-foreground">Only used when Pipecat / Gemini Live is unavailable.</p>
      </div>
      <div class="grid grid-cols-2 gap-2">
        <div class="space-y-1.5">
          <Label class="text-xs">Listen seconds</Label>
          <Input type="number" :model-value="String(config.listen_seconds ?? 8)" @update:model-value="(v: string) => updateConfig('listen_seconds', parseInt(v) || 8)" class="h-8 text-sm" min="2" max="30" />
        </div>
        <div class="space-y-1.5">
          <Label class="text-xs">Max turns</Label>
          <Input type="number" :model-value="String(config.max_turns ?? 8)" @update:model-value="(v: string) => updateConfig('max_turns', parseInt(v) || 8)" class="h-8 text-sm" min="1" max="30" />
        </div>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Exit digit (optional)</Label>
        <Input :model-value="config.exit_digit || ''" @update:model-value="(v: string) => updateConfig('exit_digit', v)" class="h-8 text-sm" placeholder="#" maxlength="1" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Transfer team (on transfer tool)</Label>
        <Select :model-value="config.transfer_team_id || ''" @update:model-value="(v: any) => updateConfig('transfer_team_id', v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue placeholder="Select team…" /></SelectTrigger>
          <SelectContent>
            <SelectItem v-for="team in teamsStore.teams" :key="team.id" :value="team.id">{{ team.name }}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <!-- Recording & Transcription -->
      <div class="space-y-3 pt-2 border-t">
        <Label class="text-xs font-semibold">Recording &amp; Transcription</Label>

        <div class="space-y-2 rounded-md border bg-muted/30 p-2.5">
          <div class="flex items-center justify-between gap-2">
            <div class="min-w-0">
              <Label class="text-xs">Enable Recording</Label>
              <p class="text-[10px] text-muted-foreground leading-snug">
                Save audio for user, assistant, and stereo mix
              </p>
            </div>
            <Switch
              :checked="!!config.recording_enabled"
              @update:checked="(v: boolean) => updateConfig('recording_enabled', v)"
            />
          </div>
          <div v-if="config.recording_enabled" class="space-y-1.5 pl-0.5 pt-1 border-t border-border/50">
            <label class="flex items-center gap-2 text-xs cursor-pointer">
              <input
                type="checkbox"
                class="rounded border-muted-foreground/40"
                :checked="config.record_user !== false"
                @change="(e: Event) => updateConfig('record_user', (e.target as HTMLInputElement).checked)"
              />
              <span>User audio</span>
            </label>
            <label class="flex items-center gap-2 text-xs cursor-pointer">
              <input
                type="checkbox"
                class="rounded border-muted-foreground/40"
                :checked="config.record_assistant !== false"
                @change="(e: Event) => updateConfig('record_assistant', (e.target as HTMLInputElement).checked)"
              />
              <span>Assistant audio</span>
            </label>
            <label class="flex items-center gap-2 text-xs cursor-pointer">
              <input
                type="checkbox"
                class="rounded border-muted-foreground/40"
                :checked="config.record_stereo !== false"
                @change="(e: Event) => updateConfig('record_stereo', (e.target as HTMLInputElement).checked)"
              />
              <span>Stereo mix (both)</span>
            </label>
          </div>
        </div>

        <div class="space-y-2 rounded-md border bg-muted/30 p-2.5">
          <div class="flex items-center justify-between gap-2">
            <div class="min-w-0">
              <Label class="text-xs">Enable Transcription</Label>
              <p class="text-[10px] text-muted-foreground leading-snug">
                Save text transcripts of user and assistant speech
              </p>
            </div>
            <Switch
              :checked="!!config.transcription_enabled"
              @update:checked="(v: boolean) => updateConfig('transcription_enabled', v)"
            />
          </div>
        </div>

        <p class="text-[10px] text-muted-foreground leading-snug">
          Tip: when enabled, Gemini Live returns live transcripts; call audio uses the server recording pipeline when available.
        </p>
      </div>

      <div class="space-y-2 pt-1 border-t">
        <div class="flex items-center justify-between">
          <Label class="text-xs font-semibold">Functions</Label>
          <Button type="button" variant="outline" size="sm" class="h-7 text-xs" @click="addAICallTool">
            <Plus class="w-3 h-3 mr-1" /> Add
          </Button>
        </div>
        <p class="text-[10px] text-muted-foreground leading-snug">
          Only the <strong>Nodes</strong> handler gets a <code class="font-mono">fn:&lt;name&gt;</code> canvas edge
          (wire to Send Message, Condition, HTTP, …). Other handlers run inline.
          <strong>Return to call</strong> means the AI Call continues after the function.
        </p>
        <div v-for="(tool, idx) in aiCallTools" :key="idx" class="rounded border p-2 space-y-1.5 bg-muted/30">
          <div class="flex justify-between gap-1">
            <Input :model-value="tool.name" @update:model-value="(v: string) => updateAICallTool(idx, 'name', v)" placeholder="function_name" class="h-7 text-xs font-mono" />
            <Button type="button" variant="ghost" size="sm" class="h-7 w-7 p-0" @click="removeAICallTool(idx)"><Trash2 class="w-3 h-3" /></Button>
          </div>
          <Input :model-value="tool.description" @update:model-value="(v: string) => updateAICallTool(idx, 'description', v)" placeholder="Description for the LLM" class="h-7 text-xs" />
          <Select :model-value="tool.handler || 'http'" @update:model-value="(v: any) => updateAICallTool(idx, 'handler', v)">
            <SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="http">HTTP</SelectItem>
              <SelectItem value="nodes">Nodes</SelectItem>
              <SelectItem value="transfer">Transfer</SelectItem>
              <SelectItem value="hangup">Hangup</SelectItem>
              <SelectItem value="set_variable">Set variable</SelectItem>
            </SelectContent>
          </Select>
          <template v-if="(tool.handler || 'http') === 'http'">
            <Select
              :model-value="tool.http?.method || 'POST'"
              @update:model-value="(v: any) => updateAICallToolHTTP(idx, 'method', v)"
            >
              <SelectTrigger class="h-7 text-xs"><SelectValue placeholder="Method" /></SelectTrigger>
              <SelectContent>
                <SelectItem value="GET">GET</SelectItem>
                <SelectItem value="POST">POST</SelectItem>
                <SelectItem value="PUT">PUT</SelectItem>
                <SelectItem value="PATCH">PATCH</SelectItem>
                <SelectItem value="DELETE">DELETE</SelectItem>
              </SelectContent>
            </Select>
            <Input :model-value="tool.http?.url || ''" @update:model-value="(v: string) => updateAICallToolHTTP(idx, 'url', v)" placeholder="https://… (inline)" class="h-7 text-xs font-mono" />
            <Textarea
              v-if="(tool.http?.method || 'POST') !== 'GET'"
              :model-value="tool.http?.body_template || ''"
              @update:model-value="(v: string) => updateAICallToolHTTP(idx, 'body_template', v)"
              placeholder='{"id":"{{order_id}}"}'
              class="min-h-[40px] text-[10px] font-mono resize-none"
            />
            <p class="text-[10px] text-muted-foreground">
              Success = HTTP <code class="font-mono">2xx</code>. Non-2xx (e.g. 405) returns
              <code class="font-mono">success: false</code> to the AI.
            </p>
          </template>
          <p v-else-if="tool.handler === 'nodes'" class="text-[10px] text-muted-foreground">
            Canvas handle <code class="font-mono">fn:{{ tool.name || '…' }}</code> —
            link to Send Message, Condition, HTTP, or other logic nodes.
          </p>
          <div class="flex items-center gap-2 pt-0.5 border-t border-border/50">
            <Checkbox
              :checked="!!tool.return_to_call"
              @update:checked="(v: boolean) => updateAICallTool(idx, 'return_to_call', !!v)"
            />
            <Label class="text-xs font-normal cursor-pointer" @click="updateAICallTool(idx, 'return_to_call', !tool.return_to_call)">
              Return to call
            </Label>
          </div>
          <p v-if="tool.handler === 'nodes'" class="text-[10px] text-muted-foreground">
            Nodes always resume this AI Call after the linked subgraph. The edge is only on the Nodes type.
          </p>
        </div>
      </div>
    </template>

    <!-- Execute Function -->
    <template v-if="node.type === 'execute_function'">
      <div class="space-y-1.5">
        <Label class="text-xs">Name</Label>
        <Input :model-value="config.name || ''" @update:model-value="(v: string) => updateConfig('name', v)" placeholder="lookup_order" class="h-8 text-sm" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">URL</Label>
        <Input :model-value="config.url || ''" @update:model-value="(v: string) => updateConfig('url', v)" placeholder="https://api.example.com/fn" class="h-8 text-xs font-mono" />
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
          </SelectContent>
        </Select>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Body template</Label>
        <Textarea :model-value="config.body_template || ''" @update:model-value="(v: string) => updateConfig('body_template', v)" :placeholder="httpBodyPlaceholder" class="min-h-[60px] text-xs font-mono resize-none" />
        <p class="text-[10px] text-muted-foreground">HTTP only (no in-process JS). Host custom logic at your own URL.</p>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Store response as</Label>
        <Input :model-value="config.response_store_as || ''" @update:model-value="(v: string) => updateConfig('response_store_as', v)" class="h-8 text-sm font-mono" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Response path (optional)</Label>
        <Input :model-value="config.response_path || ''" @update:model-value="(v: string) => updateConfig('response_path', v)" placeholder="data.id" class="h-8 text-sm font-mono" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Timeout (seconds)</Label>
        <Input type="number" :model-value="String(config.timeout_seconds ?? 15)" @update:model-value="(v: string) => updateConfig('timeout_seconds', parseInt(v) || 15)" class="h-8 text-sm" min="1" max="60" />
      </div>
    </template>

    <!-- Dial out -->
    <template v-if="node.type === 'dial_out'">
      <div class="space-y-1.5">
        <Label class="text-xs">Phone (WhatsApp number)</Label>
        <Input :model-value="config.phone || ''" @update:model-value="(v: string) => updateConfig('phone', v)" placeholder="{{callback_phone}} or E.164" class="h-8 text-sm font-mono" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Ring timeout (seconds)</Label>
        <Input type="number" :model-value="String(config.ring_seconds ?? 45)" @update:model-value="(v: string) => updateConfig('ring_seconds', parseInt(v) || 45)" class="h-8 text-sm" min="5" max="120" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Max talk seconds</Label>
        <Input type="number" :model-value="String(config.max_talk_seconds ?? 300)" @update:model-value="(v: string) => updateConfig('max_talk_seconds', parseInt(v) || 300)" class="h-8 text-sm" min="10" max="3600" />
      </div>
    </template>

    <!-- HTTP Callback: config -->
    <template v-if="node.type === 'http_callback'">
      <div class="space-y-1.5">
        <Label class="text-xs">URL</Label>
        <Input :model-value="config.url || ''" @update:model-value="(v: string) => updateConfig('url', v)" placeholder="https://api.example.com/ivr" class="h-8 text-xs font-mono" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Method</Label>
        <Select :model-value="config.method || 'GET'" @update:model-value="(v: any) => updateConfig('method', v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="GET">GET</SelectItem>
            <SelectItem value="POST">POST</SelectItem>
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
        <Label class="text-xs">Body Template</Label>
        <Textarea :model-value="config.body_template || ''" @update:model-value="(v: string) => updateConfig('body_template', v)" :placeholder="httpBodyPlaceholder" class="min-h-[60px] text-xs font-mono resize-none" />
        <p class="text-[10px] text-muted-foreground">
          Built-in:
          <code class="bg-muted px-1 py-0.5 rounded">caller_phone</code>
          <code class="bg-muted px-1 py-0.5 rounded">caller_name</code>
          <code class="bg-muted px-1 py-0.5 rounded">call_id</code>
        </p>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Timeout (seconds)</Label>
        <Input type="number" :model-value="String(config.timeout_seconds || 10)" @update:model-value="(v: string) => updateConfig('timeout_seconds', parseInt(v) || 10)" class="h-8 text-sm" min="1" max="30" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Store Response As (variable name)</Label>
        <Input :model-value="config.response_store_as || ''" @update:model-value="(v: string) => updateConfig('response_store_as', v)" placeholder="e.g. api_response" class="h-8 text-sm" />
      </div>
    </template>

    <!-- Transfer: team selector -->
    <template v-if="node.type === 'transfer'">
      <div class="space-y-1.5">
        <Label class="text-xs">Team</Label>
        <Select :model-value="config.team_id || 'none'" @update:model-value="(v: any) => updateConfig('team_id', v === 'none' ? '' : v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue placeholder="Select team" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="none">Select team...</SelectItem>
            <SelectItem v-for="team in teamsStore.teams" :key="team.id" :value="team.id">
              {{ team.name }}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Preferred agent ID (optional)</Label>
        <Input :model-value="config.agent_id || ''" @update:model-value="(v: string) => updateConfig('agent_id', v)" placeholder="UUID" class="h-8 text-xs font-mono" />
      </div>
      <div class="space-y-1.5">
        <Label class="text-xs">Overflow team (on no answer)</Label>
        <Select :model-value="config.overflow_team_id || 'none'" @update:model-value="(v: any) => updateConfig('overflow_team_id', v === 'none' ? '' : v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue placeholder="None" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="none">None</SelectItem>
            <SelectItem v-for="team in teamsStore.teams" :key="team.id" :value="team.id">
              {{ team.name }}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div class="flex items-center gap-2">
        <Switch :checked="!!config.announce_queue" @update:checked="(v: boolean) => updateConfig('announce_queue', v)" />
        <Label class="text-xs">Announce queue position</Label>
      </div>
      <div v-if="config.announce_queue" class="space-y-1.5">
        <Label class="text-xs">Announce text</Label>
        <Textarea
          :model-value="config.queue_announce_text || ''"
          @update:model-value="(v: string) => updateConfig('queue_announce_text', v)"
          class="min-h-[50px] text-xs resize-none"
          placeholder="Please hold. You are number {{position}} in the queue."
        />
      </div>

      <!-- HTTP Callbacks per lifecycle event -->
      <div class="space-y-2 mt-3">
        <Label class="text-xs font-medium">HTTP Callbacks</Label>
        <p class="text-[10px] text-muted-foreground">Configure API calls to your CRM at each transfer stage.</p>

        <div v-for="event in callbackEvents" :key="event" class="border rounded-md">
          <button class="w-full flex items-center justify-between px-3 py-1.5 text-xs font-medium hover:bg-muted/50" @click="updateCallbackField(event, '_expanded', !getCallbackConfig(event)._expanded)">
            <span>{{ callbackLabels[event] }}</span>
            <span v-if="getCallbackConfig(event).url" class="text-[10px] text-emerald-500">Configured</span>
          </button>

          <div v-if="getCallbackConfig(event)._expanded" class="px-3 pb-3 space-y-1.5 border-t">
            <div class="space-y-1 pt-2">
              <Label class="text-[10px]">URL</Label>
              <Input :model-value="getCallbackConfig(event).url || ''" @update:model-value="(v: string) => updateCallbackField(event, 'url', v)" placeholder="https://crm.example.com/api/call" class="h-7 text-xs font-mono" />
            </div>
            <div class="space-y-1">
              <Label class="text-[10px]">Method</Label>
              <Select :model-value="getCallbackConfig(event).method || 'POST'" @update:model-value="(v: any) => updateCallbackField(event, 'method', v)">
                <SelectTrigger class="h-7 text-xs"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="GET">GET</SelectItem>
                  <SelectItem value="POST">POST</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div class="space-y-1">
              <div class="flex items-center justify-between">
                <Label class="text-[10px]">Headers</Label>
                <Button variant="outline" size="sm" class="h-5 text-[10px] px-1.5" @click="addCallbackHeader(event)">
                  <Plus class="h-2.5 w-2.5 mr-0.5" /> Add
                </Button>
              </div>
              <div v-for="(val, key) in (getCallbackConfig(event).headers || {})" :key="String(key)" class="flex items-center gap-1">
                <Input :model-value="String(key)" @update:model-value="(v: string) => updateCallbackHeaderKey(event, String(key), v)" placeholder="Key" class="h-6 text-[10px] flex-1" />
                <Input :model-value="String(val)" @update:model-value="(v: string) => updateCallbackHeaderValue(event, String(key), v)" placeholder="Value" class="h-6 text-[10px] flex-1" />
                <Button variant="ghost" size="icon" class="h-5 w-5" @click="removeCallbackHeader(event, String(key))">
                  <Trash2 class="h-2.5 w-2.5 text-destructive" />
                </Button>
              </div>
            </div>
            <div v-if="(getCallbackConfig(event).method || 'POST') === 'POST'" class="space-y-1">
              <Label class="text-[10px]">Body Template</Label>
              <Textarea :model-value="getCallbackConfig(event).body_template || ''" @update:model-value="(v: string) => updateCallbackField(event, 'body_template', v)" :placeholder='`{"phone": "{{caller_phone}}", "transfer_id": "{{transfer_id}}"}`' class="min-h-[50px] text-[10px] font-mono resize-none" />
            </div>
          </div>
        </div>
      </div>

      <!-- Available variables reference -->
      <div class="border rounded-md mt-2">
        <button class="w-full flex items-center justify-between px-3 py-1.5 text-xs font-medium hover:bg-muted/50" @click="updateConfig('_vars_expanded', !config._vars_expanded)">
          <span>Available Variables</span>
        </button>
        <div v-if="config._vars_expanded" class="px-3 pb-2 border-t">
          <div class="flex flex-wrap gap-1 pt-2">
            <code v-for="v in ['caller_phone', 'caller_name', 'contact_id', 'call_log_id', 'transfer_id', 'team_id', 'whatsapp_account', 'status', 'transferred_at', 'ivr_path', 'agent_id *', 'agent_email *', 'agent_name *', 'hold_duration **', 'talk_duration **']" :key="v" class="bg-muted px-1.5 py-0.5 rounded text-[10px] cursor-pointer hover:bg-muted/80" :title="v.includes('*') ? 'Available on connect/complete only' : ''">{{ v.replace(' *', '').replace(' **', '') }}</code>
          </div>
          <p class="text-[9px] text-muted-foreground mt-1.5">* on_connect/on_complete only &nbsp; ** on_complete only</p>
        </div>
      </div>
    </template>

    <!-- Goto Flow: flow selector -->
    <template v-if="node.type === 'goto_flow'">
      <div class="space-y-1.5">
        <Label class="text-xs">Target Flow</Label>
        <Select :model-value="config.flow_id || 'none'" @update:model-value="(v: any) => updateConfig('flow_id', v === 'none' ? '' : v)">
          <SelectTrigger class="h-8 text-sm"><SelectValue placeholder="Select flow" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="none">Select flow...</SelectItem>
            <SelectItem v-for="flow in gotoFlowTargets" :key="flow.id" :value="flow.id">
              {{ flow.name }}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>
    </template>

    <!-- Timing: schedule -->
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
      </div>
    </template>
  </div>
</template>
