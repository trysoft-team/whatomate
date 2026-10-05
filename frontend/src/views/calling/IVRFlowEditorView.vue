<script setup lang="ts">
import { ref, computed, onMounted, markRaw } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { useVueFlow, MarkerType, type NodeMouseEvent, type Edge, type EdgeMouseEvent, type Connection } from '@vue-flow/core'
import FlowCanvas from '@/components/shared/FlowCanvas.vue'
import { useCallingStore } from '@/stores/calling'
import { useTeamsStore } from '@/stores/teams'
import { ivrFlowsService, type IVRNode, type IVREdge, type IVRFlowData, type IVRNodeType } from '@/services/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Label } from '@/components/ui/label'
import { Card } from '@/components/ui/card'
import { ArrowLeft, Save, Volume2, Grid3X3, Hash, Globe, Users, ExternalLink, Clock, PhoneOff, GitBranch, Variable, CheckCircle2, Timer, Tag, MessageSquare, Zap, Languages, Mic, AudioLines, Bot, PhoneForwarded, Sparkles, Code2 } from 'lucide-vue-next'
import AuditLogPanel from '@/components/shared/AuditLogPanel.vue'
import MetadataPanel from '@/components/shared/MetadataPanel.vue'
import { ScrollArea } from '@/components/ui/scroll-area'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/shared/ConfirmDialog.vue'
import ErrorState from '@/components/shared/ErrorState.vue'
import IVRNodeProperties from '@/components/calling/IVRNodeProperties.vue'
import GreetingNode from '@/components/calling/nodes/GreetingNode.vue'
import MenuNode from '@/components/calling/nodes/MenuNode.vue'
import GatherNode from '@/components/calling/nodes/GatherNode.vue'
import HTTPCallbackNode from '@/components/calling/nodes/HTTPCallbackNode.vue'
import TransferNode from '@/components/calling/nodes/TransferNode.vue'
import GotoFlowNode from '@/components/calling/nodes/GotoFlowNode.vue'
import TimingNode from '@/components/calling/nodes/TimingNode.vue'
import HangupNode from '@/components/calling/nodes/HangupNode.vue'
import ConditionNode from '@/components/calling/nodes/ConditionNode.vue'
import SetVariableNode from '@/components/calling/nodes/SetVariableNode.vue'
import ConfirmNode from '@/components/calling/nodes/ConfirmNode.vue'
import DelayNode from '@/components/calling/nodes/DelayNode.vue'
import SetLabelsNode from '@/components/calling/nodes/SetLabelsNode.vue'
import SendMessageNode from '@/components/calling/nodes/SendMessageNode.vue'
import WebhookNode from '@/components/calling/nodes/WebhookNode.vue'
import LanguageNode from '@/components/calling/nodes/LanguageNode.vue'
import VoicemailNode from '@/components/calling/nodes/VoicemailNode.vue'
import SpeechInputNode from '@/components/calling/nodes/SpeechInputNode.vue'
import AIVoiceNode from '@/components/calling/nodes/AIVoiceNode.vue'
import AICallNode from '@/components/calling/nodes/AICallNode.vue'
import ExecuteFunctionNode from '@/components/calling/nodes/ExecuteFunctionNode.vue'
import DialOutNode from '@/components/calling/nodes/DialOutNode.vue'
import StartNode from '@/components/calling/nodes/StartNode.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const callingStore = useCallingStore()
const teamsStore = useTeamsStore()

const flowId = computed(() => route.params.id as string)
const flowName = ref('')
const isActive = ref(true)
const isCallStart = ref(false)
const isOutgoingEnd = ref(false)
const saving = ref(false)
const auditRefreshKey = ref(0)
const flowCreatedAt = ref('')
const flowUpdatedAt = ref('')
const flowCreatedByName = ref('')
const flowUpdatedByName = ref('')
const loading = ref(true)
const loadError = ref(false)

// Node deletion confirmation
const showDeleteNodeConfirm = ref(false)

// Vue Flow custom node types — cast to any to avoid strict NodeComponent type checks
const START_NODE_ID = '__start__'
const nodeTypes: any = {
  start: markRaw(StartNode),
  greeting: markRaw(GreetingNode),
  menu: markRaw(MenuNode),
  gather: markRaw(GatherNode),
  http_callback: markRaw(HTTPCallbackNode),
  transfer: markRaw(TransferNode),
  goto_flow: markRaw(GotoFlowNode),
  timing: markRaw(TimingNode),
  hangup: markRaw(HangupNode),
  condition: markRaw(ConditionNode),
  set_variable: markRaw(SetVariableNode),
  confirm: markRaw(ConfirmNode),
  delay: markRaw(DelayNode),
  set_labels: markRaw(SetLabelsNode),
  send_message: markRaw(SendMessageNode),
  webhook: markRaw(WebhookNode),
  language: markRaw(LanguageNode),
  voicemail: markRaw(VoicemailNode),
  speech_input: markRaw(SpeechInputNode),
  ai_voice: markRaw(AIVoiceNode),
  ai_call: markRaw(AICallNode),
  execute_function: markRaw(ExecuteFunctionNode),
  dial_out: markRaw(DialOutNode),
}

// Node palette — categorized like the chatbot flow builder
type PaletteItem = { type: IVRNodeType; label: string; icon: any; color: string; category: string }
const palette: PaletteItem[] = [
  { type: 'greeting', label: 'Greeting', icon: Volume2, color: 'bg-green-600', category: 'Prompt' },
  { type: 'menu', label: 'Menu', icon: Grid3X3, color: 'bg-purple-600', category: 'Prompt' },
  { type: 'gather', label: 'Gather', icon: Hash, color: 'bg-blue-600', category: 'Prompt' },
  { type: 'confirm', label: 'Confirm', icon: CheckCircle2, color: 'bg-emerald-600', category: 'Prompt' },
  { type: 'http_callback', label: 'HTTP', icon: Globe, color: 'bg-orange-600', category: 'Request' },
  { type: 'execute_function', label: 'Execute Function', icon: Code2, color: 'bg-orange-700', category: 'Request' },
  { type: 'webhook', label: 'Webhook', icon: Zap, color: 'bg-orange-800', category: 'Request' },
  { type: 'send_message', label: 'Send Message', icon: MessageSquare, color: 'bg-green-700', category: 'Request' },
  { type: 'set_variable', label: 'Set Variable', icon: Variable, color: 'bg-fuchsia-600', category: 'Input' },
  { type: 'set_labels', label: 'Labels', icon: Tag, color: 'bg-pink-600', category: 'Input' },
  { type: 'language', label: 'Language', icon: Languages, color: 'bg-violet-600', category: 'Input' },
  { type: 'speech_input', label: 'Speech', icon: AudioLines, color: 'bg-blue-800', category: 'Input' },
  { type: 'voicemail', label: 'Voicemail', icon: Mic, color: 'bg-rose-700', category: 'Prompt' },
  { type: 'ai_voice', label: 'AI Voice', icon: Bot, color: 'bg-emerald-800', category: 'Prompt' },
  { type: 'ai_call', label: 'AI Call', icon: Sparkles, color: 'bg-violet-700', category: 'Prompt' },
  { type: 'condition', label: 'Condition', icon: GitBranch, color: 'bg-indigo-600', category: 'Logic' },
  { type: 'delay', label: 'Delay', icon: Timer, color: 'bg-sky-600', category: 'Logic' },
  { type: 'timing', label: 'Timing', icon: Clock, color: 'bg-cyan-600', category: 'Logic' },
  { type: 'transfer', label: 'Transfer', icon: Users, color: 'bg-amber-600', category: 'Logic' },
  { type: 'dial_out', label: 'Dial Out', icon: PhoneForwarded, color: 'bg-amber-800', category: 'Logic' },
  { type: 'goto_flow', label: 'Go to Flow', icon: ExternalLink, color: 'bg-teal-600', category: 'Logic' },
  { type: 'hangup', label: 'Hangup', icon: PhoneOff, color: 'bg-red-600', category: 'Logic' },
]

const paletteCategories = ['All', 'Prompt', 'Request', 'Input', 'Logic'] as const
const paletteCategory = ref<(typeof paletteCategories)[number]>('All')
const paletteSearch = ref('')

const filteredPalette = computed(() => {
  const q = paletteSearch.value.trim().toLowerCase()
  return palette.filter((p) => {
    if (paletteCategory.value !== 'All' && p.category !== paletteCategory.value) return false
    if (!q) return true
    return p.label.toLowerCase().includes(q) || p.type.includes(q) || p.category.toLowerCase().includes(q)
  })
})


// Vue Flow instance
const { nodes, edges, addNodes, addEdges, removeNodes, removeEdges, onConnect, project, fitView } = useVueFlow({
  defaultEdgeOptions: {
    type: 'default',
    animated: true,
    markerEnd: MarkerType.ArrowClosed,
  },
})

// Selected node for properties panel
const selectedNodeId = ref<string | null>(null)

const selectedNode = computed(() => {
  if (!selectedNodeId.value) return null
  return nodes.value.find(n => n.id === selectedNodeId.value) || null
})

function onNodeClick(event: NodeMouseEvent) {
  selectedNodeId.value = event.node.id
}

function onPaneClick() {
  selectedNodeId.value = null
}

// Add node from palette
const entryNodeId = ref<string>('')

let nodeCounter = 0

function addNodeFromPalette(type: IVRNodeType) {
  // Place node at center of viewport
  const pos = project({ x: window.innerWidth / 2 - 100, y: window.innerHeight / 2 - 50 })
  const id = `node_${Date.now()}_${nodeCounter++}`

  const defaultConfigs: Partial<Record<IVRNodeType, Record<string, any>>> = {
    start: {},
    greeting: { audio_file: '', interruptible: false },
    menu: { audio_file: '', timeout_seconds: 10, max_retries: 3, options: {} },
    gather: { audio_file: '', max_digits: 10, terminator: '#', timeout_seconds: 10, max_retries: 3, store_as: '' },
    confirm: {
      variable: '',
      prompt_template: 'You entered {{value}}. Press {{confirm_digit}} to confirm, or {{retry_digit}} to re-enter.',
      confirm_digit: '1',
      retry_digit: '2',
      timeout_seconds: 10,
      max_retries: 3,
    },
    condition: { field: '', operator: 'equals', value: '', case_sensitive: false },
    set_variable: { assignments: [{ name: '', value: '' }] },
    delay: { seconds: 3, mode: 'silence' },
    set_labels: { add: [], remove: [] },
    send_message: { body: '', to: '' },
    webhook: { url: '', method: 'POST', headers: {}, body_template: '', timeout_seconds: 10 },
    language: { locale: 'en' },
    voicemail: { max_seconds: 60, terminator: '#', store_as: 'voicemail_key', beep: true },
    speech_input: { max_seconds: 10, store_as: 'speech_text', language: '' },
    ai_voice: { input_variable: 'speech_text', store_as: 'ai_reply', listen_seconds: 0, interruptible: true, system_prompt: '' },
    ai_call: {
      welcome_message: 'Hello! How can I help you today?',
      system_instructions: '',
      voice_provider: 'gemini',
      openai_voice: 'alloy',
      voice_id: 'Puck',
      listen_seconds: 8,
      max_turns: 8,
      exit_digit: '',
      transfer_team_id: '',
      recording_enabled: false,
      record_user: true,
      record_assistant: true,
      record_stereo: true,
      transcription_enabled: false,
      tools: [],
    },
    execute_function: {
      name: '',
      url: '',
      method: 'POST',
      headers: {},
      body_template: '',
      timeout_seconds: 15,
      response_store_as: 'function_result',
      response_path: '',
    },
    dial_out: { phone: '', ring_seconds: 45, max_talk_seconds: 300 },
    http_callback: { url: '', method: 'GET', headers: {}, body_template: '', timeout_seconds: 10 },
    transfer: { team_id: '', agent_id: '', overflow_team_id: '', announce_queue: false, queue_announce_text: '' },
    goto_flow: { flow_id: '' },
    timing: { schedule: [
      { day: 'monday', enabled: true, start_time: '09:00', end_time: '17:00' },
      { day: 'tuesday', enabled: true, start_time: '09:00', end_time: '17:00' },
      { day: 'wednesday', enabled: true, start_time: '09:00', end_time: '17:00' },
      { day: 'thursday', enabled: true, start_time: '09:00', end_time: '17:00' },
      { day: 'friday', enabled: true, start_time: '09:00', end_time: '17:00' },
      { day: 'saturday', enabled: false, start_time: '09:00', end_time: '17:00' },
      { day: 'sunday', enabled: false, start_time: '09:00', end_time: '17:00' },
    ]},
    hangup: { audio_file: '' },
  }

  addNodes([{
    id,
    type,
    position: { x: pos.x, y: pos.y },
    data: {
      label: type.replace('_', ' ').replace(/\b\w/g, (c: string) => c.toUpperCase()),
      config: defaultConfigs[type] || {},
      isEntryNode: false,
    },
  }])

  // First action node → auto-wire Start → it (parity with chatbot builder).
  const startHasOutgoing = edges.value.some((e) => e.source === START_NODE_ID)
  if (!startHasOutgoing && nodes.value.some((n) => n.id === START_NODE_ID)) {
    addEdges([{
      id: `edge_start_${id}`,
      source: START_NODE_ID,
      target: id,
      sourceHandle: undefined,
      type: 'default',
      animated: true,
      markerEnd: MarkerType.ArrowClosed,
      label: '',
    }])
  }
  selectedNodeId.value = id
}

function ensureStartNode() {
  if (nodes.value.some((n) => n.type === 'start')) return
  addNodes([
    {
      id: START_NODE_ID,
      type: 'start',
      position: { x: 100, y: 100 },
      data: { label: 'Start', config: {}, isEntryNode: true },
      deletable: false,
    },
  ])
  entryNodeId.value = START_NODE_ID
}

// Handle new connections — one edge per output handle
onConnect((params) => {
  // Remove existing edge from this source handle (enforce single connection)
  const existing = edges.value.filter(
    e => e.source === params.source && e.sourceHandle === params.sourceHandle,
  )
  if (existing.length > 0) removeEdges(existing)

  addEdges([{
    ...params,
    type: 'default',
    animated: true,
    markerEnd: MarkerType.ArrowClosed,
    label: params.sourceHandle || 'default',
  }])
  spreadParallelLabels()
})

// Spread labels vertically when multiple edges share the same source→target.
function spreadParallelLabels() {
  const groups = new Map<string, Edge[]>()
  for (const e of edges.value) {
    const key = `${e.source}→${e.target}`
    if (!groups.has(key)) groups.set(key, [])
    groups.get(key)!.push(e)
  }
  for (const group of groups.values()) {
    for (let i = 0; i < group.length; i++) {
      const yOffset = group.length > 1 ? (i - (group.length - 1) / 2) * 22 : 0
      group[i].labelStyle = { transform: `translateY(${yOffset}px)` }
      group[i].labelBgStyle = { fill: 'none', fillOpacity: 0 }
      group[i].labelBgPadding = [0, 0] as [number, number]
    }
  }
}

// Select edge on click (Backspace/Delete will remove it)
function onEdgeClick({ edge }: EdgeMouseEvent) {
  // Deselect all nodes/edges, then select this edge
  nodes.value.forEach(n => (n.selected = false))
  edges.value.forEach(e => (e.selected = false))
  edge.selected = true
  selectedNodeId.value = null
}

function onEdgesChange(changes: { type: string }[]) {
  if (changes.some((c) => c.type === 'remove')) {
    spreadParallelLabels()
  }
}

// Handle edge reconnection (drag endpoint to a different node)
function onEdgeUpdate({ edge, connection }: { edge: Edge; connection: Connection }) {
  // Remove old edge, add new one
  removeEdges([edge])
  addEdges([{
    ...connection,
    type: 'default',
    animated: true,
    markerEnd: MarkerType.ArrowClosed,
    label: connection.sourceHandle || 'default',
  }])
  spreadParallelLabels()
}

// Update node data from properties panel
function onUpdateNode(updatedIVRNode: IVRNode) {
  const node = nodes.value.find(n => n.id === updatedIVRNode.id)
  if (!node) return

  node.data = {
    ...node.data,
    label: updatedIVRNode.label,
    config: updatedIVRNode.config,
  }
}

// Delete selected node — show confirmation first
function requestDeleteSelectedNode() {
  if (!selectedNode.value || selectedNode.value.type === 'start') return
  showDeleteNodeConfirm.value = true
}

function confirmDeleteSelectedNode() {
  const node = selectedNode.value
  if (!node || node.type === 'start') return
  const nodeId = node.id

  // Remove connected edges first
  const connectedEdges = edges.value.filter(e => e.source === nodeId || e.target === nodeId)
  if (connectedEdges.length > 0) {
    removeEdges(connectedEdges)
  }

  removeNodes([nodeId])
  selectedNodeId.value = null
  showDeleteNodeConfirm.value = false
}

// Convert Vue Flow state to IVRFlowData for saving
function toFlowData(): IVRFlowData {
  const ivrNodes: IVRNode[] = nodes.value.map(n => ({
    id: n.id,
    type: n.type as IVRNodeType,
    label: n.data?.label || '',
    position: { x: n.position.x, y: n.position.y },
    config: n.data?.config || {},
  }))

  const ivrEdges: IVREdge[] = edges.value.map(e => ({
    from: e.source,
    to: e.target,
    condition: e.sourceHandle || (e as any).label || 'default',
  }))

  // Entry is always the Start sentinel when present (parity with chatbot).
  const start = ivrNodes.find(n => n.type === 'start')
  let entryNode = ''
  if (start) {
    entryNode = start.id
  } else {
    const nodesWithIncoming = new Set(ivrEdges.map(e => e.to))
    entryNode = ivrNodes.find(n => !nodesWithIncoming.has(n.id))?.id || ivrNodes[0]?.id || ''
  }

  return {
    version: 2,
    nodes: ivrNodes,
    edges: ivrEdges,
    entry_node: entryNode,
  }
}

// Load flow data into Vue Flow
function loadFlowData(data: IVRFlowData) {
  // Legacy graphs may lack an explicit Start — inject and rewire like chatbot.
  const hasStart = data.nodes.some(n => n.type === 'start')
  const originalEntry = data.entry_node || data.nodes[0]?.id || ''
  const effectiveEntry = hasStart ? data.entry_node : START_NODE_ID

  const vfNodes = data.nodes.map(n => ({
    id: n.id,
    type: n.type,
    position: { x: n.position.x, y: n.position.y },
    data: {
      label: n.label,
      config: n.config,
      isEntryNode: n.id === effectiveEntry,
    },
    deletable: n.type !== 'start',
  }))

  const vfEdges = data.edges.map((e, idx) => ({
    id: `edge_${idx}`,
    source: e.from,
    target: e.to,
    sourceHandle: e.condition !== 'default' ? e.condition : undefined,
    type: 'default' as const,
    animated: true,
    markerEnd: MarkerType.ArrowClosed,
    label: e.condition !== 'default' ? e.condition : '',
  }))

  if (!hasStart) {
    const entry = data.nodes.find(n => n.id === originalEntry)
    const startPos = entry
      ? { x: entry.position?.x ?? 100, y: (entry.position?.y ?? 100) - 140 }
      : { x: 100, y: 100 }
    vfNodes.unshift({
      id: START_NODE_ID,
      type: 'start',
      position: startPos,
      data: { label: 'Start', config: {}, isEntryNode: true },
      deletable: false,
    })
    if (originalEntry) {
      vfEdges.unshift({
        id: 'edge_start',
        source: START_NODE_ID,
        target: originalEntry,
        sourceHandle: undefined,
        type: 'default' as const,
        animated: true,
        markerEnd: MarkerType.ArrowClosed,
        label: '',
      })
    }
    entryNodeId.value = START_NODE_ID
  } else {
    entryNodeId.value = data.entry_node || START_NODE_ID
  }

  addNodes(vfNodes)
  addEdges(vfEdges)
  spreadParallelLabels()

  setTimeout(() => fitView({ padding: 0.2 }), 100)
}

// Save flow
async function saveFlow() {
  if (!flowName.value.trim()) {
    toast.error(t('calling.nameRequired'))
    return
  }
  if (nodes.value.length === 0) {
    toast.error(t('calling.noIVRFlows'))
    return
  }

  saving.value = true
  try {
    const flowData = toFlowData()
    const updated = await callingStore.updateIVRFlow(flowId.value, {
      name: flowName.value,
      is_active: isActive.value,
      is_call_start: isCallStart.value,
      is_outgoing_end: isOutgoingEnd.value,
      menu: flowData,
    })

    // Sync server-generated fields (e.g. TTS audio_file) back to canvas nodes
    const serverMenu = updated?.menu as IVRFlowData | undefined
    if (serverMenu?.nodes) {
      const serverConfigs = new Map(serverMenu.nodes.map((n: IVRNode) => [n.id, n.config]))
      for (const node of nodes.value) {
        const serverConfig = serverConfigs.get(node.id)
        if (serverConfig) {
          node.data = { ...node.data, config: serverConfig }
        }
      }
    }

    toast.success(t('calling.flowUpdated'))
    auditRefreshKey.value++
  } catch (e: any) {
    const msg = e?.response?.data?.message || t('calling.flowSaveFailed')
    toast.error(msg)
  } finally {
    saving.value = false
  }
}

// Computed IVR node for properties panel
const selectedIVRNode = computed<IVRNode | null>(() => {
  const node = selectedNode.value
  if (!node) return null
  return {
    id: node.id,
    type: node.type as IVRNodeType,
    label: node.data?.label || '',
    position: node.position,
    config: node.data?.config || {},
  }
})

// Load flow data from server
const AI_IVR_FLOW_DRAFT_KEY = 'whatomate:ai-ivr-flow-draft'

function hydrateAIGeneratedDraft(): boolean {
  try {
    const raw = sessionStorage.getItem(AI_IVR_FLOW_DRAFT_KEY)
    if (!raw) return false
    sessionStorage.removeItem(AI_IVR_FLOW_DRAFT_KEY)
    const draft = JSON.parse(raw) as {
      name?: string
      description?: string
      menu?: IVRFlowData
    }
    if (!draft.menu || draft.menu.version !== 2 || !Array.isArray(draft.menu.nodes)) {
      return false
    }
    if (draft.name) flowName.value = draft.name
    loadFlowData(draft.menu)
    return true
  } catch {
    return false
  }
}

async function loadFlow() {
  loading.value = true
  loadError.value = false
  try {
    await Promise.all([callingStore.fetchIVRFlows(), teamsStore.fetchTeams()])
    const res = await ivrFlowsService.get(flowId.value)
    const flow = (res.data as any)?.data || res.data
    flowName.value = flow.name
    isActive.value = flow.is_active
    isCallStart.value = flow.is_call_start
    isOutgoingEnd.value = flow.is_outgoing_end
    flowCreatedAt.value = flow.created_at || ''
    flowUpdatedAt.value = flow.updated_at || ''
    flowCreatedByName.value = flow.created_by?.full_name || ''
    flowUpdatedByName.value = flow.updated_by?.full_name || ''

    if (route.query.ai === '1' && hydrateAIGeneratedDraft()) {
      // AI draft replaces empty menu from create
    } else if (flow.menu && flow.menu.version === 2) {
      loadFlowData(flow.menu)
    } else {
      ensureStartNode()
    }
  } catch {
    loadError.value = true
  } finally {
    loading.value = false
  }
}

// Load flow on mount
onMounted(() => {
  loadFlow()
})
</script>

<template>
  <div class="flex flex-col h-screen bg-muted/30">
    <!-- Header (mirrors chatbot flow builder) -->
    <header class="border-b bg-background px-4 py-3 flex-shrink-0 space-y-2">
      <div class="flex items-center gap-4">
        <Button variant="ghost" size="icon" class="shrink-0" :aria-label="t('calling.backToFlows')" @click="router.push({ name: 'ivr-flows' })">
          <ArrowLeft class="h-5 w-5" />
        </Button>

        <div class="flex-1 flex items-center gap-4 min-w-0 flex-wrap">
          <div class="flex items-center gap-2">
            <Label class="text-sm text-muted-foreground whitespace-nowrap">Name</Label>
            <Input v-model="flowName" placeholder="Flow name" class="w-48 font-medium" />
          </div>
        </div>
      </div>

      <div class="flex items-center justify-between gap-3 pl-12 flex-wrap">
        <div class="flex items-center gap-4 flex-wrap">
          <div class="flex items-center gap-2">
            <Switch v-model:checked="isActive" />
            <span class="text-sm">{{ isActive ? 'Active' : 'Inactive' }}</span>
          </div>
          <div class="flex items-center gap-2">
            <Switch v-model:checked="isCallStart" :disabled="!isActive" />
            <span class="text-sm whitespace-nowrap">Incoming Call Start</span>
          </div>
          <div class="flex items-center gap-2">
            <Switch v-model:checked="isOutgoingEnd" :disabled="!isActive" />
            <span class="text-sm whitespace-nowrap">Outgoing Post-Call</span>
          </div>
        </div>

        <div class="flex items-center gap-2 flex-wrap">
          <Button variant="outline" size="sm" @click="router.push({ name: 'ivr-flows' })">
            Cancel
          </Button>
          <Button size="sm" :disabled="saving" @click="saveFlow">
            <Save class="h-4 w-4 mr-2" />
            {{ saving ? t('calling.flowSaving') : t('calling.flowSave') }}
          </Button>
        </div>
      </div>
    </header>

    <!-- Node palette: categorized + searchable -->
    <div class="px-4 py-2 border-b bg-muted/30 shrink-0 space-y-2">
      <div class="flex items-center gap-2 flex-wrap">
        <span class="text-xs text-muted-foreground shrink-0">Add node:</span>
        <Input
          v-model="paletteSearch"
          placeholder="Search nodes…"
          class="h-7 text-xs w-44"
        />
        <div class="flex gap-1 flex-wrap">
          <Button
            v-for="cat in paletteCategories"
            :key="cat"
            :variant="paletteCategory === cat ? 'default' : 'outline'"
            size="sm"
            class="h-7 text-xs"
            @click="paletteCategory = cat"
          >
            {{ cat }}
          </Button>
        </div>
      </div>
      <div class="flex flex-wrap items-center gap-2 content-start">
        <Button
          v-for="p in filteredPalette"
          :key="p.type"
          variant="outline"
          size="sm"
          class="h-7 text-xs gap-1.5"
          @click="addNodeFromPalette(p.type)"
        >
          <div :class="['w-2 h-2 rounded-full', p.color]" />
          <component :is="p.icon" class="w-3 h-3" />
          {{ p.label }}
        </Button>
        <span v-if="filteredPalette.length === 0" class="text-xs text-muted-foreground">No nodes match</span>
      </div>
    </div>

    <!-- Main content -->
    <div class="flex-1 flex overflow-hidden">
      <!-- Canvas -->
      <div class="flex-1 relative">
        <div v-if="loading" class="absolute inset-0 flex items-center justify-center bg-background/80 z-10">
          <div class="animate-spin rounded-full h-8 w-8 border-b-2 border-primary" />
        </div>
        <ErrorState
          v-else-if="loadError"
          :title="t('calling.flowLoadFailed')"
          :description="t('calling.flowLoadFailedDesc')"
          class="absolute inset-0 z-10 bg-background"
        >
          <template #action>
            <div class="flex gap-2">
              <Button variant="outline" size="sm" @click="router.push({ name: 'ivr-flows' })">
                {{ t('calling.goBack') }}
              </Button>
              <Button size="sm" @click="loadFlow">
                {{ t('common.retry') }}
              </Button>
            </div>
          </template>
        </ErrorState>
        <FlowCanvas
          :node-types="nodeTypes"
          edge-type="default"
          @node-click="onNodeClick"
          @pane-click="onPaneClick"
          @edge-update="onEdgeUpdate"
          @edge-click="onEdgeClick"
          @edges-change="onEdgesChange"
        />
      </div>

      <!-- Right Panel -->
      <Card class="w-[420px] min-w-0 border-y-0 border-r-0 rounded-none shrink-0 flex flex-col">
        <!-- Node Properties (when a node is selected) -->
        <div v-if="selectedIVRNode" class="flex-1 overflow-y-auto">
          <IVRNodeProperties
            :node="selectedIVRNode"
            :current-flow-id="flowId"
            @update:node="onUpdateNode"
            @delete="requestDeleteSelectedNode"
          />
        </div>

        <!-- Metadata + Activity Log (when no node is selected) -->
        <ScrollArea v-else orientation="vertical" class="flex-1">
          <div class="p-4 space-y-4 min-w-0">
            <MetadataPanel
              :created-at="flowCreatedAt"
              :updated-at="flowUpdatedAt"
              :created-by-name="flowCreatedByName"
              :updated-by-name="flowUpdatedByName"
            />
            <AuditLogPanel :key="auditRefreshKey" resource-type="ivr_flow" :resource-id="flowId" />
          </div>
        </ScrollArea>
      </Card>
    </div>

    <!-- Node Delete Confirmation -->
    <ConfirmDialog
      v-model:open="showDeleteNodeConfirm"
      :title="t('calling.deleteNodeConfirmTitle')"
      :description="t('calling.deleteNodeConfirmDesc')"
      :confirm-label="t('common.delete')"
      variant="destructive"
      @confirm="confirmDeleteSelectedNode"
    />
  </div>
</template>

