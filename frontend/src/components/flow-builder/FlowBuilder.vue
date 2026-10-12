<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { Card, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Textarea } from '@/components/ui/textarea'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import {
  Plus,
  Trash2,
  GripVertical,
  Type,
  TextCursorInput,
  CircleDot,
  CheckSquare,
  Calendar,
  ChevronDown,
  Image,
  ArrowRight,
  Settings2,
  Layers,
  AlignLeft,
  FileText,
  Link2,
  ShieldCheck,
  Upload,
  GitBranch,
  Split,
  CalendarRange,
  Camera,
  FileUp,
  Copy,
  Check,
  ChevronDown as ChevronDownIcon,
  ChevronLeft,
  MoreVertical,
  HelpCircle,
  Flag,
  RotateCcw,
} from 'lucide-vue-next'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { toast } from 'vue-sonner'
import draggable from 'vuedraggable'
import FlowComponentPreview from '@/components/flow-builder/FlowComponentPreview.vue'
import { useColorMode } from '@/composables/useColorMode'

// Component types available in WhatsApp Flows (Meta Flow JSON)
const componentTypes = [
  { type: 'TextHeading', label: 'Heading', icon: Type },
  { type: 'TextSubheading', label: 'Subheading', icon: Type },
  { type: 'TextBody', label: 'Text', icon: Type },
  { type: 'TextCaption', label: 'Caption', icon: AlignLeft },
  { type: 'RichText', label: 'Rich Text', icon: FileText },
  { type: 'TextInput', label: 'Text Input', icon: TextCursorInput },
  { type: 'TextArea', label: 'Text Area', icon: TextCursorInput },
  { type: 'Dropdown', label: 'Dropdown', icon: ChevronDown },
  { type: 'RadioButtonsGroup', label: 'Radio Buttons', icon: CircleDot },
  { type: 'CheckboxGroup', label: 'Checkboxes', icon: CheckSquare },
  { type: 'OptIn', label: 'Opt-In', icon: ShieldCheck },
  { type: 'DatePicker', label: 'Date Picker', icon: Calendar },
  { type: 'CalendarPicker', label: 'Calendar', icon: CalendarRange },
  { type: 'PhotoPicker', label: 'Photo Upload', icon: Camera },
  { type: 'DocumentPicker', label: 'Doc Upload', icon: FileUp },
  { type: 'Image', label: 'Image', icon: Image },
  { type: 'EmbeddedLink', label: 'Link', icon: Link2 },
  { type: 'If', label: 'If', icon: GitBranch },
  { type: 'Switch', label: 'Switch', icon: Split },
  { type: 'Footer', label: 'Footer Button', icon: ArrowRight },
]

/** Nested components allowed inside If then/else (and Switch cases). */
const nestableComponentTypes = [
  { type: 'TextHeading', label: 'Heading' },
  { type: 'TextSubheading', label: 'Subheading' },
  { type: 'TextBody', label: 'Text' },
  { type: 'TextCaption', label: 'Caption' },
  { type: 'EmbeddedLink', label: 'Link' },
  { type: 'OptIn', label: 'Opt-In' },
  { type: 'TextInput', label: 'Text Input' },
  { type: 'Dropdown', label: 'Dropdown' },
]

interface FlowComponent {
  id: string
  type: string
  name?: string
  label?: string
  text?: string
  required?: boolean
  markdown?: boolean
  'data-source'?: any[]
  'on-click-action'?: any
  'input-type'?: string
  [key: string]: any
}

interface FlowScreen {
  id: string
  title: string
  data: Record<string, any>
  layout: {
    type: string
    children: FlowComponent[]
  }
}

interface Props {
  modelValue?: { screens: FlowScreen[] }
  /** Flow has a data endpoint, so Footers may use data_exchange. */
  endpointEnabled?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  modelValue: () => ({ screens: [] }),
  endpointEnabled: false,
})

/** Select value for "no next screen": the endpoint completes the flow. */
const FINISH_FLOW = '__finish__'

const emit = defineEmits<{
  'update:modelValue': [value: { screens: FlowScreen[] }]
}>()

const { isDark } = useColorMode()

const screens = ref<FlowScreen[]>(props.modelValue?.screens || [])
const selectedScreenIndex = ref<number>(0)
const selectedComponentIndex = ref<number | null>(null)
/** Nested child under If/Switch currently being edited in the properties panel. */
const selectedNested = ref<{ branch: 'then' | 'else' | 'case'; caseKey?: string; index: number } | null>(null)

watch(() => props.modelValue, (newVal) => {
  if (newVal?.screens) {
    screens.value = newVal.screens
  }
}, { deep: true })

watch(screens, (newScreens) => {
  emit('update:modelValue', { screens: newScreens })
}, { deep: true })

const selectedScreen = computed(() => screens.value[selectedScreenIndex.value])
const selectedComponent = computed(() => {
  if (selectedComponentIndex.value === null || !selectedScreen.value) return null
  return selectedScreen.value.layout.children[selectedComponentIndex.value]
})

const selectedNestedComponent = computed(() => {
  if (!selectedComponent.value || !selectedNested.value) return null
  const nest = selectedNested.value
  if (selectedComponent.value.type === 'If') {
    const list = (selectedComponent.value[nest.branch === 'else' ? 'else' : 'then'] || []) as FlowComponent[]
    return list[nest.index] || null
  }
  if (selectedComponent.value.type === 'Switch' && nest.branch === 'case' && nest.caseKey) {
    const list = (selectedComponent.value.cases?.[nest.caseKey] || []) as FlowComponent[]
    return list[nest.index] || null
  }
  return null
})

const switchCaseKeys = computed(() => {
  if (selectedComponent.value?.type !== 'Switch') return [] as string[]
  return Object.keys(selectedComponent.value.cases || {})
})

const activeEditComponent = computed(() => selectedNestedComponent.value || selectedComponent.value)

const usesLongText = computed(() => {
  const t = activeEditComponent.value?.type
  return t === 'RichText' || t === 'TextBody' || t === 'TextCaption'
})

function generateId() {
  const chars = 'abcdefghijklmnopqrstuvwxyz'
  let result = 'id_'
  for (let i = 0; i < 12; i++) {
    result += chars.charAt(Math.floor(Math.random() * chars.length))
  }
  return result
}

function generateFieldName(type: string): string {
  let count = 1
  const prefix = type === 'TextInput' ? 'text_input' :
                 type === 'TextArea' ? 'text_area' :
                 type === 'Dropdown' ? 'dropdown' :
                 type === 'RadioButtonsGroup' ? 'radio' :
                 type === 'CheckboxGroup' ? 'checkbox' :
                 type === 'DatePicker' ? 'date' :
                 type === 'CalendarPicker' ? 'calendar' :
                 type === 'PhotoPicker' ? 'photo_picker' :
                 type === 'DocumentPicker' ? 'document_picker' :
                 type === 'OptIn' ? 'opt_in' : 'field'

  for (const screen of screens.value) {
    for (const comp of screen.layout.children) {
      if (comp.name?.startsWith(prefix)) {
        count++
      }
    }
  }

  return `${prefix}_${count}`
}

function numberToLetters(num: number): string {
  let result = ''
  while (num > 0) {
    num--
    result = String.fromCharCode(65 + (num % 26)) + result
    num = Math.floor(num / 26)
  }
  return result
}

function addScreen() {
  const screenNum = screens.value.length + 1
  screens.value.push({
    id: `SCREEN_${numberToLetters(screenNum)}`,
    title: `Screen ${screenNum}`,
    data: {},
    layout: {
      type: 'SingleColumnLayout',
      children: []
    }
  })
  selectedScreenIndex.value = screens.value.length - 1
  selectedComponentIndex.value = null
}

function removeScreen(index: number) {
  screens.value.splice(index, 1)
  if (selectedScreenIndex.value >= screens.value.length) {
    selectedScreenIndex.value = Math.max(0, screens.value.length - 1)
  }
  selectedComponentIndex.value = null
}

function selectScreen(index: number) {
  selectedScreenIndex.value = index
  selectedComponentIndex.value = null
  selectedNested.value = null
}

function createDefaultComponent(type: string): FlowComponent {
  const component: FlowComponent = {
    id: generateId(),
    type
  }

  switch (type) {
    case 'TextHeading':
    case 'TextSubheading':
      component.text = 'Enter text here'
      break
    case 'TextBody':
      component.text = 'Enter text here'
      component.markdown = false
      break
    case 'TextCaption':
      component.text = 'Caption text'
      component.markdown = false
      break
    case 'RichText':
      component.text = '# Heading\n\nEnter **rich** text here. Supports markdown.'
      break
    case 'TextInput':
      component.name = generateFieldName(type)
      component.label = 'Label'
      component.required = false
      component['input-type'] = 'text'
      break
    case 'TextArea':
      component.name = generateFieldName(type)
      component.label = 'Label'
      component.required = false
      break
    case 'Dropdown':
      component.name = generateFieldName(type)
      component.label = 'Select an option'
      component.required = false
      component['data-source'] = [
        { id: 'option_a', title: 'Option 1' },
        { id: 'option_b', title: 'Option 2' }
      ]
      break
    case 'RadioButtonsGroup':
      component.name = generateFieldName(type)
      component.label = 'Choose one'
      component.required = false
      component['data-source'] = [
        { id: 'option_a', title: 'Option 1' },
        { id: 'option_b', title: 'Option 2' }
      ]
      break
    case 'CheckboxGroup':
      component.name = generateFieldName(type)
      component.label = 'Select options'
      component.required = false
      component['data-source'] = [
        { id: 'option_a', title: 'Option 1' },
        { id: 'option_b', title: 'Option 2' }
      ]
      break
    case 'OptIn':
      component.name = generateFieldName(type)
      component.label = 'I agree to the terms and conditions'
      component.required = true
      break
    case 'DatePicker':
      component.name = generateFieldName(type)
      component.label = 'Select date'
      component.required = false
      component['helper-text'] = ''
      component['min-date'] = ''
      component['max-date'] = ''
      component['unavailable-dates'] = ''
      break
    case 'CalendarPicker':
      component.name = generateFieldName(type)
      component.label = 'Select date'
      component.mode = 'single'
      component.required = false
      break
    case 'PhotoPicker':
      component.name = generateFieldName(type)
      component.label = 'Upload a photo'
      component.description = ''
      component['photo-source'] = 'camera_gallery'
      component['min-uploaded-photos'] = 0
      component['max-uploaded-photos'] = 1
      break
    case 'DocumentPicker':
      component.name = generateFieldName(type)
      component.label = 'Upload a document'
      component.description = ''
      component['min-uploaded-documents'] = 0
      component['max-uploaded-documents'] = 1
      break
    case 'Image':
      component.src = ''
      component['aspect-ratio'] = 1
      component['scale-type'] = 'contain'
      component['alt-text'] = ''
      break
    case 'EmbeddedLink':
      component.text = 'Learn more'
      component['on-click-action'] = {
        name: 'open_url',
        url: 'https://example.com'
      }
      break
    case 'If':
      component.condition = "${form.field_name} == true"
      component.then = [
        { id: generateId(), type: 'TextBody', text: 'Shown when condition is true' }
      ]
      component.else = [
        { id: generateId(), type: 'TextBody', text: 'Shown when condition is false' }
      ]
      break
    case 'Switch':
      component.value = '${form.field_name}'
      component.cases = {
        option_a: [{ id: generateId(), type: 'TextBody', text: 'Case option_a' }],
        option_b: [{ id: generateId(), type: 'TextBody', text: 'Case option_b' }],
      }
      break
    case 'Footer':
      component.label = 'Continue'
      component['on-click-action'] = {
        name: 'complete',
        payload: {}
      }
      break
  }

  return component
}

function addComponent(type: string) {
  if (!selectedScreen.value) return
  selectedScreen.value.layout.children.push(createDefaultComponent(type))
  selectedComponentIndex.value = selectedScreen.value.layout.children.length - 1
  selectedNested.value = null
}

function removeComponent(index: number) {
  if (!selectedScreen.value) return
  selectedScreen.value.layout.children.splice(index, 1)
  selectedComponentIndex.value = null
  selectedNested.value = null
}

function selectComponent(index: number) {
  selectedComponentIndex.value = index
  selectedNested.value = null
}

function moveComponent(index: number, direction: 'up' | 'down') {
  if (!selectedScreen.value) return
  const children = selectedScreen.value.layout.children
  const newIndex = direction === 'up' ? index - 1 : index + 1

  if (newIndex < 0 || newIndex >= children.length) return

  const temp = children[index]
  children[index] = children[newIndex]
  children[newIndex] = temp

  selectedComponentIndex.value = newIndex
  selectedNested.value = null
}

function updateComponentProperty(key: string, value: any) {
  if (selectedNestedComponent.value) {
    selectedNestedComponent.value[key] = value
    return
  }
  if (selectedComponentIndex.value === null || !selectedScreen.value) return
  selectedScreen.value.layout.children[selectedComponentIndex.value][key] = value
}

function getIfBranch(branch: 'then' | 'else'): FlowComponent[] {
  if (!selectedComponent.value || selectedComponent.value.type !== 'If') return []
  if (!Array.isArray(selectedComponent.value[branch])) {
    selectedComponent.value[branch] = []
  }
  return selectedComponent.value[branch]
}

function addNestedToIf(branch: 'then' | 'else', type: string) {
  const list = getIfBranch(branch)
  list.push(createDefaultComponent(type))
  selectedNested.value = { branch, index: list.length - 1 }
}

function removeNestedFromIf(branch: 'then' | 'else', index: number) {
  getIfBranch(branch).splice(index, 1)
  if (selectedNested.value?.branch === branch && selectedNested.value.index === index) {
    selectedNested.value = null
  }
}

function selectNestedIf(branch: 'then' | 'else', index: number) {
  selectedNested.value = { branch, index }
}

function addSwitchCase() {
  if (!selectedComponent.value || selectedComponent.value.type !== 'Switch') return
  if (!selectedComponent.value.cases) selectedComponent.value.cases = {}
  let n = 1
  while (selectedComponent.value.cases[`case_${n}`]) n++
  const key = `case_${n}`
  selectedComponent.value.cases[key] = [createDefaultComponent('TextBody')]
  selectedNested.value = { branch: 'case', caseKey: key, index: 0 }
}

function removeSwitchCase(key: string) {
  if (!selectedComponent.value?.cases) return
  delete selectedComponent.value.cases[key]
  if (selectedNested.value?.caseKey === key) selectedNested.value = null
}

function renameSwitchCase(oldKey: string, newKey: string) {
  if (!selectedComponent.value?.cases || !newKey || oldKey === newKey) return
  if (selectedComponent.value.cases[newKey]) {
    toast.error('Case key already exists')
    return
  }
  selectedComponent.value.cases[newKey] = selectedComponent.value.cases[oldKey]
  delete selectedComponent.value.cases[oldKey]
  if (selectedNested.value?.caseKey === oldKey) {
    selectedNested.value = { ...selectedNested.value, caseKey: newKey }
  }
}

function addNestedToSwitchCase(caseKey: string, type: string) {
  if (!selectedComponent.value?.cases) return
  if (!Array.isArray(selectedComponent.value.cases[caseKey])) {
    selectedComponent.value.cases[caseKey] = []
  }
  const list = selectedComponent.value.cases[caseKey] as FlowComponent[]
  list.push(createDefaultComponent(type))
  selectedNested.value = { branch: 'case', caseKey, index: list.length - 1 }
}

function removeNestedFromSwitchCase(caseKey: string, index: number) {
  const list = selectedComponent.value?.cases?.[caseKey] as FlowComponent[] | undefined
  if (!list) return
  list.splice(index, 1)
  if (selectedNested.value?.caseKey === caseKey && selectedNested.value.index === index) {
    selectedNested.value = null
  }
}

function selectNestedSwitchCase(caseKey: string, index: number) {
  selectedNested.value = { branch: 'case', caseKey, index }
}

function onDragReorder() {
  // Keep selection on the same component after reorder
  const id = selectedComponent.value?.id
  if (!id || !selectedScreen.value) return
  const idx = selectedScreen.value.layout.children.findIndex(c => c.id === id)
  selectedComponentIndex.value = idx >= 0 ? idx : null
  selectedNested.value = null
}

function selectComponentById(id?: string) {
  if (!id || !selectedScreen.value) return
  const idx = selectedScreen.value.layout.children.findIndex(c => c.id === id)
  if (idx >= 0) selectComponent(idx)
}

function isComponentSelected(comp: FlowComponent | null | undefined) {
  return !!comp?.id && selectedComponent.value?.id === comp.id
}

const playgroundBodyComponents = computed(() => {
  if (!selectedScreen.value) return [] as FlowComponent[]
  return selectedScreen.value.layout.children.filter(c => c.type !== 'Footer')
})

const playgroundFooter = computed(() => {
  if (!selectedScreen.value) return null
  return selectedScreen.value.layout.children.find(c => c.type === 'Footer') || null
})

const playgroundProgress = computed(() => {
  const total = screens.value.length
  if (total <= 1) return ''
  return `${selectedScreenIndex.value + 1} of ${total}`
})

/** Meta Flow progress bar percent (matches Meta playground aria-valuenow). */
const playgroundProgressPercent = computed(() => {
  const total = screens.value.length
  if (total <= 0) return 0
  if (total === 1) return 100
  return Math.round(((selectedScreenIndex.value + 1) / total) * 100)
})

const canGoBackInPreview = computed(() => selectedScreenIndex.value > 0)

/** Meta playground settings */
const playgroundInteractive = ref(true)
const playgroundPlatform = ref<'android' | 'ios'>('android')
const playgroundTheme = ref<'light' | 'dark'>('light')
const previewSent = ref(false)
const previewKey = ref(0)
const copiedJson = ref(false)
const playgroundSettingsOpen = ref(false)
const playgroundNavMenuOpen = ref(false)

function typeLabel(type: string) {
  return componentTypes.find(t => t.type === type)?.label || type
}

function copyFlowJson() {
  const payload = {
    version: '6.1',
    screens: sanitizeFlowForMeta({ screens: screens.value }).screens,
  }
  const text = JSON.stringify(payload, null, 2)
  navigator.clipboard.writeText(text).then(() => {
    copiedJson.value = true
    toast.success('Flow JSON copied')
    setTimeout(() => { copiedJson.value = false }, 2000)
  }).catch(() => toast.error('Failed to copy JSON'))
}

function restartPreview() {
  previewSent.value = false
  playgroundNavMenuOpen.value = false
  previewKey.value += 1
  // Reset to first screen for multi-screen preview feel
  if (screens.value.length > 0) {
    selectedScreenIndex.value = 0
    selectedComponentIndex.value = null
    selectedNested.value = null
  }
}

function previewGoBack() {
  if (!playgroundInteractive.value || !canGoBackInPreview.value) return
  selectedScreenIndex.value -= 1
  selectedComponentIndex.value = null
  selectedNested.value = null
  playgroundNavMenuOpen.value = false
}

function onPreviewFooterClick() {
  if (!playgroundInteractive.value) {
    if (playgroundFooter.value) selectComponentById(playgroundFooter.value.id)
    return
  }
  const clickAction = playgroundFooter.value?.['on-click-action']
  const action = clickAction?.name
  if (action === 'navigate' || (action === 'data_exchange' && clickAction?.payload?.next_screen)) {
    const nextId = action === 'navigate' ? clickAction?.next?.name : clickAction?.payload?.next_screen
    const idx = screens.value.findIndex(s => s.id === nextId)
    if (idx >= 0) {
      selectedScreenIndex.value = idx
      selectedComponentIndex.value = null
      selectedNested.value = null
      return
    }
  }
  // complete / default → show Sent state like Meta playground
  previewSent.value = true
}

function updateClickAction(name: string) {
  if (!selectedComponent.value) return
  if (name === 'navigate') {
    updateComponentProperty('on-click-action', {
      name: 'navigate',
      next: { type: 'screen', name: '' },
      payload: {}
    })
  } else if (name === 'open_url') {
    updateComponentProperty('on-click-action', {
      name: 'open_url',
      url: selectedComponent.value['on-click-action']?.url || 'https://'
    })
  } else if (name === 'complete') {
    updateComponentProperty('on-click-action', { name: 'complete', payload: {} })
  } else if (name === 'data_exchange') {
    updateComponentProperty('on-click-action', { name: 'data_exchange', payload: {} })
  }
}

function setDataExchangeNextScreen(value: string) {
  if (!selectedComponent.value) return
  const payload = { ...(selectedComponent.value['on-click-action']?.payload || {}) }
  if (value && value !== FINISH_FLOW) payload.next_screen = value
  else delete payload.next_screen
  updateComponentProperty('on-click-action', { name: 'data_exchange', payload })
}

/** Meta Image.src is raw base64; preview needs a data URL. */
function imagePreviewSrc(src?: string): string {
  if (!src) return ''
  if (src.startsWith('data:image/')) return src
  const mime = src.startsWith('iVBOR') ? 'image/png'
    : src.startsWith('/9j/') ? 'image/jpeg'
    : src.startsWith('UklGR') || src.startsWith('RIFF') ? 'image/webp'
    : 'image/jpeg'
  return `data:${mime};base64,${src}`
}

function stripImageDataUrl(src: string): string {
  const match = src.match(/^data:image\/[^;]+;base64,(.+)$/s)
  return match ? match[1] : src
}

const IMAGE_MAX_BYTES = 300 * 1024 // Meta recommends up to ~300KB
const IMAGE_ACCEPT = 'image/jpeg,image/png,image/jpg'

async function onImageFileSelected(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file || !selectedComponent.value || selectedComponent.value.type !== 'Image') return

  if (!IMAGE_ACCEPT.split(',').includes(file.type) && !/\.(jpe?g|png)$/i.test(file.name)) {
    toast.error('Use a JPEG or PNG image')
    return
  }
  if (file.size > IMAGE_MAX_BYTES) {
    toast.error(`Image must be under 300KB (got ${Math.round(file.size / 1024)}KB)`)
    return
  }

  try {
    const dataUrl = await readFileAsDataURL(file)
    const base64 = stripImageDataUrl(dataUrl)
    updateComponentProperty('src', base64)

    // Infer aspect ratio from natural size when possible
    const img = new window.Image()
    img.onload = () => {
      if (img.naturalWidth > 0 && img.naturalHeight > 0) {
        const ratio = Math.round((img.naturalWidth / img.naturalHeight) * 100) / 100
        updateComponentProperty('aspect-ratio', ratio)
        if (!selectedComponent.value?.width) {
          updateComponentProperty('width', Math.min(img.naturalWidth, 800))
        }
        if (!selectedComponent.value?.height) {
          updateComponentProperty('height', Math.min(img.naturalHeight, 800))
        }
      }
    }
    img.src = dataUrl
    if (!selectedComponent.value?.['alt-text']) {
      updateComponentProperty('alt-text', file.name.replace(/\.[^.]+$/, ''))
    }
  } catch {
    toast.error('Failed to read image file')
  }
}

function readFileAsDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result || ''))
    reader.onerror = () => reject(reader.error)
    reader.readAsDataURL(file)
  })
}

function clearImageSrc() {
  updateComponentProperty('src', '')
}

function setOptInReadMore(enabled: boolean) {
  const target = activeEditComponent.value
  if (!target || target.type !== 'OptIn') return
  if (enabled) {
    updateComponentProperty('on-click-action', {
      name: 'open_url',
      url: 'https://example.com/terms'
    })
  } else {
    delete target['on-click-action']
  }
}

function addOption() {
  const target = activeEditComponent.value
  if (!target || !target['data-source']) return
  target['data-source'].push({
    id: generateId(),
    title: 'New Option',
    description: '',
    metadata: '',
  })
}

function removeOption(index: number) {
  const target = activeEditComponent.value
  if (!target || !target['data-source']) return
  target['data-source'].splice(index, 1)
}

function updateOption(index: number, key: string, value: string) {
  const target = activeEditComponent.value
  if (!target || !target['data-source']) return
  target['data-source'][index][key] = value
}

async function onOptionImageSelected(index: number, event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  const target = activeEditComponent.value
  if (!file || !target?.['data-source']?.[index]) return

  if (!IMAGE_ACCEPT.split(',').includes(file.type) && !/\.(jpe?g|png)$/i.test(file.name)) {
    toast.error('Use a JPEG or PNG image')
    return
  }
  if (file.size > IMAGE_MAX_BYTES) {
    toast.error(`Option image must be under 300KB`)
    return
  }
  try {
    const dataUrl = await readFileAsDataURL(file)
    target['data-source'][index].image = stripImageDataUrl(dataUrl)
    if (!target['data-source'][index]['alt-text']) {
      target['data-source'][index]['alt-text'] = file.name.replace(/\.[^.]+$/, '')
    }
  } catch {
    toast.error('Failed to read option image')
  }
}

function clearOptionImage(index: number) {
  const target = activeEditComponent.value
  if (!target?.['data-source']?.[index]) return
  delete target['data-source'][index].image
  delete target['data-source'][index]['alt-text']
}

/** Parse comma/newline separated YYYY-MM-DD dates for DatePicker unavailable-dates. */
function parseDateList(value: string | string[] | undefined): string[] {
  if (Array.isArray(value)) return value.filter(Boolean)
  if (!value || typeof value !== 'string') return []
  return value.split(/[\n,]+/).map(s => s.trim()).filter(Boolean)
}

function formatDateList(value: string | string[] | undefined): string {
  if (Array.isArray(value)) return value.join(', ')
  return typeof value === 'string' ? value : ''
}

function getComponentLabel(comp: FlowComponent): string {
  const typeInfo = componentTypes.find(t => t.type === comp.type)
  return typeInfo?.label || comp.type
}

function getComponentIcon(type: string) {
  return componentTypes.find(t => t.type === type)?.icon || Type
}

const componentsWithoutId = [
  'TextHeading',
  'TextSubheading',
  'TextBody',
  'TextCaption',
  'RichText',
  'TextInput',
  'TextArea',
  'Dropdown',
  'RadioButtonsGroup',
  'CheckboxGroup',
  'OptIn',
  'DatePicker',
  'CalendarPicker',
  'PhotoPicker',
  'DocumentPicker',
  'Image',
  'EmbeddedLink',
  'If',
  'Switch',
  'Footer',
]

function sanitizeComponent(comp: FlowComponent): Record<string, any> {
  const { id, ...rest } = comp
  const cleaned: Record<string, any> = {}
  for (const [k, v] of Object.entries(rest)) {
    if (v !== undefined && v !== '') cleaned[k] = v
  }

  if (comp.type === 'Image' && typeof cleaned.src === 'string') {
    cleaned.src = stripImageDataUrl(cleaned.src)
    if (!cleaned['alt-text']) delete cleaned['alt-text']
    if (cleaned.width === undefined || cleaned.width === null || cleaned.width === '') delete cleaned.width
    if (cleaned.height === undefined || cleaned.height === null || cleaned.height === '') delete cleaned.height
  }

  // Option lists: strip empty optional fields; normalize option images to raw base64
  if (Array.isArray(cleaned['data-source'])) {
    cleaned['data-source'] = cleaned['data-source'].map((opt: any) => {
      const o: Record<string, any> = { id: opt.id, title: opt.title }
      if (opt.description) o.description = opt.description
      if (opt.metadata) o.metadata = opt.metadata
      if (opt.image) o.image = stripImageDataUrl(String(opt.image))
      if (opt['alt-text']) o['alt-text'] = opt['alt-text']
      if (opt.color) o.color = opt.color
      if (opt.enabled === false) o.enabled = false
      return o
    })
  }

  if (comp.type === 'DatePicker') {
    if (cleaned['unavailable-dates'] !== undefined) {
      const list = parseDateList(cleaned['unavailable-dates'])
      if (list.length) cleaned['unavailable-dates'] = list
      else delete cleaned['unavailable-dates']
    }
    if (!cleaned['helper-text']) delete cleaned['helper-text']
    if (!cleaned['min-date']) delete cleaned['min-date']
    if (!cleaned['max-date']) delete cleaned['max-date']
  }

  if (comp.type === 'PhotoPicker' || comp.type === 'DocumentPicker') {
    if (!cleaned.description) delete cleaned.description
  }

  if (comp.type === 'If') {
    cleaned.then = (Array.isArray(comp.then) ? comp.then : []).map((c: FlowComponent) => sanitizeComponent(c))
    if (Array.isArray(comp.else) && comp.else.length > 0) {
      cleaned.else = comp.else.map((c: FlowComponent) => sanitizeComponent(c))
    } else {
      delete cleaned.else
    }
  }

  if (comp.type === 'Switch') {
    const cases: Record<string, any[]> = {}
    for (const [key, children] of Object.entries(comp.cases || {})) {
      cases[key] = (Array.isArray(children) ? children : []).map((c: FlowComponent) => sanitizeComponent(c))
    }
    cleaned.cases = cases
  }

  if (componentsWithoutId.includes(comp.type)) {
    return cleaned
  }
  return { id, ...cleaned }
}

function sanitizeFlowForMeta(flowData: { screens: FlowScreen[] }): { screens: any[] } {
  return {
    screens: flowData.screens.map(screen => ({
      id: screen.id,
      title: screen.title,
      data: screen.data,
      layout: {
        type: screen.layout.type,
        children: screen.layout.children.map(comp => sanitizeComponent(comp))
      }
    }))
  }
}

defineExpose({
  sanitizeFlowForMeta
})
</script>

<template>
  <div class="flex h-full gap-4">
    <!-- Screens Panel -->
    <Card class="w-64 flex-shrink-0 flex flex-col overflow-hidden">
      <CardHeader class="py-3 px-4">
        <div class="flex items-center justify-between">
          <CardTitle class="text-sm font-medium flex items-center gap-2">
            <Layers class="h-4 w-4" />
            Screens
          </CardTitle>
          <Button variant="ghost" size="icon" class="h-7 w-7" @click="addScreen">
            <Plus class="h-4 w-4" />
          </Button>
        </div>
      </CardHeader>
      <Separator />
      <ScrollArea class="flex-1">
        <div class="p-2 space-y-1">
          <div
            v-for="(screen, index) in screens"
            :key="screen.id"
            :class="[
              'flex items-center gap-2 p-2 rounded-md cursor-pointer text-sm',
              selectedScreenIndex === index ? 'bg-primary text-primary-foreground light:bg-primary light:text-primary-foreground' : 'hover:bg-muted'
            ]"
            @click="selectScreen(index)"
          >
            <GripVertical class="h-4 w-4 opacity-50" />
            <span class="flex-1 truncate">{{ screen.title }}</span>
            <Button
              v-if="screens.length > 1"
              variant="ghost"
              size="icon"
              class="h-6 w-6 opacity-50 hover:opacity-100"
              @click.stop="removeScreen(index)"
            >
              <Trash2 class="h-3 w-3" />
            </Button>
          </div>
          <div
            v-if="screens.length === 0"
            class="p-4 text-center text-sm text-muted-foreground"
          >
            No screens yet
          </div>
        </div>
      </ScrollArea>
    </Card>

    <!-- Screen Editor -->
    <Card class="flex-1 flex flex-col overflow-hidden">
      <CardHeader class="py-3 px-4 flex-shrink-0">
        <div class="flex items-center justify-between">
          <div v-if="selectedScreen" class="flex items-center gap-2">
            <Input
              v-model="selectedScreen.title"
              class="h-8 w-48 text-sm font-medium"
              placeholder="Screen Title"
            />
            <Badge variant="outline">{{ selectedScreen.id }}</Badge>
          </div>
          <CardTitle v-else class="text-sm font-medium">Select a screen</CardTitle>
        </div>
      </CardHeader>
      <Separator />

      <div v-if="selectedScreen" class="flex-1 flex overflow-hidden">
        <!-- Component Palette -->
        <ScrollArea class="w-52 border-r flex-shrink-0">
          <div class="p-3">
            <p class="text-xs font-medium text-muted-foreground mb-2">Add Components</p>
            <div class="grid grid-cols-2 gap-1">
              <Button
                v-for="comp in componentTypes"
                :key="comp.type"
                variant="outline"
                size="sm"
                class="h-auto py-2 flex-col gap-1 text-xs"
                @click="addComponent(comp.type)"
              >
                <component :is="comp.icon" class="h-4 w-4" />
                <span class="text-[10px]">{{ comp.label }}</span>
              </Button>
            </div>
          </div>
        </ScrollArea>

        <!-- Structure (drag) + Playground preview -->
        <div class="flex-1 flex overflow-hidden min-w-0">
          <!-- Left: component structure with drag reorder -->
          <div class="flex-1 flex flex-col border-r min-w-0 overflow-hidden">
            <div class="px-3 py-2 border-b flex-shrink-0">
              <p class="text-xs font-medium text-muted-foreground">Structure</p>
              <p class="text-[10px] text-muted-foreground">Drag to reorder</p>
            </div>
            <ScrollArea class="flex-1">
              <div class="p-3">
                <draggable
                  v-if="selectedScreen.layout.children.length"
                  v-model="selectedScreen.layout.children"
                  item-key="id"
                  handle=".drag-handle"
                  ghost-class="opacity-40"
                  class="space-y-2"
                  @end="onDragReorder"
                >
                  <template #item="{ element: comp, index }">
                    <div
                      :class="[
                        'rounded-md border bg-card/50 cursor-pointer transition-colors',
                        selectedComponentIndex === index
                          ? 'border-primary ring-1 ring-primary/40'
                          : 'border-border hover:border-muted-foreground/30'
                      ]"
                      @click="selectComponent(index)"
                    >
                      <div class="flex items-center gap-2 px-2 py-1.5 border-b border-border/60">
                        <button type="button" class="drag-handle p-0.5 text-muted-foreground hover:text-foreground cursor-grab active:cursor-grabbing" @click.stop>
                          <GripVertical class="h-4 w-4" />
                        </button>
                        <component :is="getComponentIcon(comp.type)" class="h-3.5 w-3.5 text-muted-foreground flex-shrink-0" />
                        <span class="text-xs font-medium truncate flex-1">{{ getComponentLabel(comp) }}</span>
                        <span class="text-[10px] text-muted-foreground tabular-nums">{{ index + 1 }}</span>
                      </div>
                      <div class="p-2 bg-white/[0.02] light:bg-white/80">
                        <FlowComponentPreview :comp="comp" :label-for-type="typeLabel" compact :dark="isDark" />
                      </div>
                    </div>
                  </template>
                </draggable>
                <div
                  v-else
                  class="p-8 text-center text-sm text-muted-foreground border-2 border-dashed rounded-lg"
                >
                  Add components from the palette
                </div>
              </div>
            </ScrollArea>
          </div>

          <!-- Right: Meta Flow playground (copy of Meta preview chrome) -->
          <div class="flex-1 flex flex-col min-w-0 overflow-hidden bg-black/20 light:bg-[#f5f6f7]">
            <!-- Top actions: Copy Flow JSON + Settings (like Meta) -->
            <div class="flex items-center justify-between gap-2 px-3 py-2 border-b border-white/[0.06] light:border-zinc-200 bg-white/[0.03] light:bg-white flex-shrink-0">
              <span class="text-xs font-semibold text-foreground">Preview</span>
              <div class="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                class="h-8 text-xs"
                @click="copyFlowJson"
              >
                <component :is="copiedJson ? Check : Copy" class="h-3.5 w-3.5 mr-1.5" />
                Copy Flow JSON
              </Button>
              <DropdownMenu v-model:open="playgroundSettingsOpen">
                <DropdownMenuTrigger as-child>
                  <Button variant="outline" size="sm" class="h-8 text-xs">
                    Settings
                    <ChevronDownIcon class="h-3.5 w-3.5 ml-1 opacity-60" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" class="w-[225px]">
                  <DropdownMenuLabel class="text-xs font-medium">Interactive mode</DropdownMenuLabel>
                  <DropdownMenuItem
                    class="text-xs flex items-center justify-between gap-2"
                    @select.prevent="playgroundInteractive = !playgroundInteractive"
                  >
                    <span>{{ playgroundInteractive ? 'Enabled' : 'Disabled' }}</span>
                    <Switch :checked="playgroundInteractive" class="pointer-events-none scale-90" />
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuLabel class="text-xs font-medium">Platform</DropdownMenuLabel>
                  <DropdownMenuItem class="text-xs" @select="playgroundPlatform = 'android'">
                    Android
                    <Check v-if="playgroundPlatform === 'android'" class="h-3.5 w-3.5 ml-auto text-[#00a884]" />
                  </DropdownMenuItem>
                  <DropdownMenuItem class="text-xs" @select="playgroundPlatform = 'ios'">
                    iOS
                    <Check v-if="playgroundPlatform === 'ios'" class="h-3.5 w-3.5 ml-auto text-[#00a884]" />
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuLabel class="text-xs font-medium">Theme</DropdownMenuLabel>
                  <DropdownMenuItem class="text-xs" @select="playgroundTheme = 'light'">
                    Light
                    <Check v-if="playgroundTheme === 'light'" class="h-3.5 w-3.5 ml-auto text-[#00a884]" />
                  </DropdownMenuItem>
                  <DropdownMenuItem class="text-xs" @select="playgroundTheme = 'dark'">
                    Dark
                    <Check v-if="playgroundTheme === 'dark'" class="h-3.5 w-3.5 ml-auto text-[#00a884]" />
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
              </div>
            </div>

            <p class="px-3 py-1.5 text-[10px] text-muted-foreground bg-white/[0.02] light:bg-white border-b border-white/[0.04] light:border-zinc-100 flex-shrink-0">
              Rendering and interaction varies based on device.
            </p>

            <ScrollArea class="flex-1">
              <div class="p-4 flex flex-col items-center gap-3" :key="previewKey">
                <p class="text-[11px] font-medium text-muted-foreground self-start w-full max-w-[300px]">Preview Flow</p>

                <!-- Device frame -->
                <div
                  class="w-full max-w-[280px] overflow-hidden shadow-xl flex flex-col"
                  :class="[
                    playgroundPlatform === 'ios' ? 'rounded-[2rem] border-[5px]' : 'rounded-2xl border-[4px]',
                    playgroundTheme === 'dark' ? 'border-zinc-700 bg-zinc-900' : 'border-zinc-800 bg-black',
                  ]"
                >
                  <!-- Notch / status -->
                  <div
                    class="flex-shrink-0 flex items-center justify-center"
                    :class="playgroundPlatform === 'ios' ? 'h-7' : 'h-5'"
                    :style="{ background: playgroundTheme === 'dark' ? '#111' : '#000' }"
                  >
                    <div
                      v-if="playgroundPlatform === 'ios'"
                      class="w-20 h-4 rounded-full bg-black"
                    />
                  </div>

                  <!-- Chat chrome only for Sent state (Meta shows WA chat after complete) -->
                  <div
                    v-if="previewSent"
                    class="flex-shrink-0 px-3 py-2 flex items-center gap-2"
                    :class="playgroundTheme === 'dark' ? 'bg-[#1f2c34]' : 'bg-[#008069]'"
                  >
                    <div class="h-7 w-7 rounded-full bg-white/20 text-white text-[10px] font-bold flex items-center justify-center">WA</div>
                    <div class="min-w-0 flex-1">
                      <p class="text-white text-xs font-medium truncate">Business</p>
                      <p class="text-white/70 text-[9px]">online</p>
                    </div>
                  </div>

                  <!-- Chat / Sent state OR Flow screen -->
                  <div
                    class="flex flex-col h-[420px]"
                    :class="playgroundTheme === 'dark' ? 'bg-[#0b141a]' : (previewSent ? 'bg-[#efeae2]' : 'bg-white')"
                  >
                    <!-- Sent bubble -->
                    <div v-if="previewSent" class="flex-1 flex flex-col p-3 justify-end gap-2">
                      <div class="self-end max-w-[85%]">
                        <div
                          class="rounded-lg px-3 py-2 text-xs shadow-sm"
                          :class="playgroundTheme === 'dark' ? 'bg-[#005c4b] text-white' : 'bg-[#d9fdd3] text-zinc-900'"
                        >
                          <p class="font-medium mb-1">{{ selectedScreen.title || 'Flow' }}</p>
                          <p class="opacity-80 text-[10px]">Flow message</p>
                          <p class="text-[9px] opacity-60 mt-1 text-right">Sent</p>
                        </div>
                      </div>
                      <p class="text-center text-[10px] text-muted-foreground py-2">Preview sent</p>
                    </div>

                    <!-- Active flow form (Meta Flow sheet) -->
                    <template v-else>
                      <div
                        class="flex-1 flex flex-col min-h-0 relative"
                        :class="playgroundTheme === 'dark' ? 'bg-[#111b21]' : 'bg-white'"
                      >
                        <!-- Interactive: Meta Flow navbar (back + progress + menu) -->
                        <header
                          v-if="playgroundInteractive"
                          class="flex-shrink-0"
                          :class="playgroundTheme === 'dark' ? 'bg-[#111b21]' : 'bg-white'"
                        >
                          <nav class="flex items-center gap-1 px-1.5 h-10">
                            <button
                              type="button"
                              class="h-8 w-8 inline-flex items-center justify-center rounded-full shrink-0"
                              :class="[
                                playgroundTheme === 'dark' ? 'text-zinc-200 hover:bg-white/10' : 'text-zinc-700 hover:bg-zinc-100',
                                !canGoBackInPreview ? 'opacity-30 pointer-events-none' : ''
                              ]"
                              :aria-label="'Back'"
                              :disabled="!canGoBackInPreview"
                              @click="previewGoBack"
                            >
                              <ChevronLeft class="h-5 w-5" />
                            </button>
                            <div class="flex-1 min-w-0" />
                            <div class="relative">
                              <button
                                type="button"
                                class="h-8 w-8 inline-flex items-center justify-center rounded-full"
                                :class="playgroundTheme === 'dark' ? 'text-zinc-200 hover:bg-white/10' : 'text-zinc-700 hover:bg-zinc-100'"
                                title="Menu"
                                aria-label="Menu"
                                aria-haspopup="menu"
                                :aria-expanded="playgroundNavMenuOpen"
                                @click="playgroundNavMenuOpen = !playgroundNavMenuOpen"
                              >
                                <MoreVertical class="h-4 w-4" />
                              </button>
                              <div
                                v-if="playgroundNavMenuOpen"
                                class="absolute right-0 top-full mt-1 z-20 min-w-[120px] rounded-lg shadow-lg border overflow-hidden py-1"
                                :class="playgroundTheme === 'dark' ? 'bg-[#233138] border-zinc-700' : 'bg-white border-zinc-200'"
                                role="menu"
                              >
                                <button
                                  type="button"
                                  class="w-full px-3 py-2 text-left text-xs flex items-center gap-2"
                                  :class="playgroundTheme === 'dark' ? 'text-zinc-100 hover:bg-white/10' : 'text-zinc-800 hover:bg-zinc-50'"
                                  role="menuitem"
                                  @click="playgroundNavMenuOpen = false"
                                >
                                  <HelpCircle class="h-3.5 w-3.5" />
                                  Help
                                </button>
                                <button
                                  type="button"
                                  class="w-full px-3 py-2 text-left text-xs flex items-center gap-2"
                                  :class="playgroundTheme === 'dark' ? 'text-zinc-100 hover:bg-white/10' : 'text-zinc-800 hover:bg-zinc-50'"
                                  role="menuitem"
                                  @click="playgroundNavMenuOpen = false"
                                >
                                  <Flag class="h-3.5 w-3.5" />
                                  Report
                                </button>
                              </div>
                            </div>
                          </nav>
                          <!-- Flow progress bar (Meta interactive mode) -->
                          <div
                            class="px-3 pb-2"
                            role="progressbar"
                            :aria-valuetext="`Flow progress: ${playgroundProgressPercent}%`"
                            aria-valuemin="0"
                            aria-valuemax="100"
                            :aria-valuenow="playgroundProgressPercent"
                          >
                            <div
                              class="h-1 w-full rounded-full overflow-hidden"
                              :class="playgroundTheme === 'dark' ? 'bg-zinc-700' : 'bg-zinc-200'"
                            >
                              <div
                                class="h-full rounded-full bg-[#00a884] transition-all duration-300"
                                :style="{ width: `${playgroundProgressPercent}%` }"
                              />
                            </div>
                          </div>
                        </header>

                        <!-- Non-interactive: simple title header -->
                        <div
                          v-else
                          class="flex-shrink-0 px-4 pt-3 pb-2 border-b"
                          :class="playgroundTheme === 'dark' ? 'border-zinc-800' : 'border-zinc-100'"
                        >
                          <p
                            class="text-[15px] font-semibold leading-tight truncate"
                            :class="playgroundTheme === 'dark' ? 'text-zinc-100' : 'text-zinc-900'"
                          >
                            {{ selectedScreen.title }}
                          </p>
                          <p
                            v-if="playgroundProgress"
                            class="text-xs mt-0.5"
                            :class="playgroundTheme === 'dark' ? 'text-zinc-400' : 'text-zinc-500'"
                          >
                            {{ playgroundProgress }}
                          </p>
                        </div>

                        <!-- Screen title under nav when interactive -->
                        <div
                          v-if="playgroundInteractive"
                          class="flex-shrink-0 px-4 pb-2"
                        >
                          <p
                            class="text-[15px] font-semibold leading-tight truncate"
                            :class="playgroundTheme === 'dark' ? 'text-zinc-100' : 'text-zinc-900'"
                          >
                            {{ selectedScreen.title }}
                          </p>
                        </div>

                        <div
                          class="flex-1 overflow-y-auto px-4 py-3 space-y-4"
                          @click="playgroundNavMenuOpen = false"
                        >
                          <div
                            v-for="comp in playgroundBodyComponents"
                            :key="comp.id"
                            :class="[
                              'rounded-md -mx-1 px-1 py-0.5 cursor-pointer transition',
                              isComponentSelected(comp)
                                ? 'ring-2 ring-[#00a884] ring-offset-1'
                                : playgroundTheme === 'dark' ? 'hover:bg-white/5' : 'hover:bg-zinc-50'
                            ]"
                            @click="selectComponentById(comp.id)"
                          >
                            <FlowComponentPreview
                              :comp="comp"
                              :label-for-type="typeLabel"
                              playground
                              :dark="playgroundTheme === 'dark'"
                            />
                          </div>
                          <div
                            v-if="!playgroundBodyComponents.length && !playgroundFooter"
                            class="text-center text-xs py-16"
                            :class="playgroundTheme === 'dark' ? 'text-zinc-500' : 'text-zinc-400'"
                          >
                            Add components to preview
                          </div>
                        </div>

                        <div
                          v-if="playgroundFooter"
                          class="flex-shrink-0 p-3 border-t cursor-pointer"
                          :class="[
                            playgroundTheme === 'dark' ? 'border-zinc-800 bg-[#111b21]' : 'border-zinc-100 bg-white',
                            isComponentSelected(playgroundFooter) ? 'ring-2 ring-inset ring-[#00a884]' : ''
                          ]"
                          @click="onPreviewFooterClick"
                        >
                          <FlowComponentPreview
                            :comp="playgroundFooter"
                            :label-for-type="typeLabel"
                            playground
                            :dark="playgroundTheme === 'dark'"
                          />
                        </div>

                        <!-- Meta branding strip (interactive mode) -->
                        <div
                          v-if="playgroundInteractive"
                          class="flex-shrink-0 px-3 py-1.5 flex items-center justify-center gap-1"
                          :class="playgroundTheme === 'dark' ? 'bg-[#0b141a]' : 'bg-zinc-50'"
                        >
                          <p
                            class="text-[10px] leading-none"
                            :class="playgroundTheme === 'dark' ? 'text-zinc-400' : 'text-zinc-500'"
                          >
                            Managed by the business.
                            <button
                              type="button"
                              class="underline underline-offset-2"
                              :class="playgroundTheme === 'dark' ? 'text-zinc-300' : 'text-zinc-600'"
                            >
                              Learn more
                            </button>
                          </p>
                        </div>
                      </div>
                    </template>
                  </div>

                  <!-- Home indicator -->
                  <div
                    class="h-4 flex items-center justify-center flex-shrink-0"
                    :class="playgroundTheme === 'dark' ? 'bg-[#111b21]' : 'bg-white'"
                  >
                    <div class="w-20 h-1 rounded-full bg-zinc-400/50" />
                  </div>
                </div>

                <!-- Restart preview (Meta playground) -->
                <button
                  type="button"
                  class="w-full max-w-[280px] h-9 rounded-lg border border-white/[0.1] light:border-zinc-300 bg-white/[0.04] light:bg-white text-xs font-medium text-foreground hover:bg-white/[0.08] light:hover:bg-zinc-50 inline-flex items-center justify-center gap-1.5"
                  @click="restartPreview"
                >
                  <RotateCcw class="h-3.5 w-3.5" />
                  Restart preview
                </button>
              </div>
            </ScrollArea>
          </div>
        </div>
      </div>

      <div v-else class="flex-1 flex items-center justify-center text-muted-foreground">
        <div class="text-center">
          <Layers class="h-12 w-12 mx-auto mb-4 opacity-50" />
          <p>Add a screen to get started</p>
          <Button class="mt-4" @click="addScreen">
            <Plus class="h-4 w-4 mr-2" />
            Add Screen
          </Button>
        </div>
      </div>
    </Card>

    <!-- Properties Panel -->
    <Card class="w-72 flex-shrink-0 flex flex-col overflow-hidden">
      <CardHeader class="py-3 px-4 flex-shrink-0">
        <CardTitle class="text-sm font-medium flex items-center gap-2">
          <Settings2 class="h-4 w-4" />
          Properties
        </CardTitle>
      </CardHeader>
      <Separator />
      <ScrollArea class="flex-1">
        <div v-if="selectedComponent" class="p-4 space-y-4">
          <div class="flex items-center justify-between">
            <div class="flex flex-col gap-1 min-w-0">
              <Badge>{{ getComponentLabel(selectedComponent) }}</Badge>
              <button
                v-if="selectedNestedComponent"
                type="button"
                class="text-[10px] text-primary text-left truncate"
                @click="selectedNested = null"
              >
                Nested: {{ getComponentLabel(selectedNestedComponent) }} (back)
              </button>
            </div>
            <div class="flex gap-1">
              <Button
                variant="ghost"
                size="icon"
                class="h-7 w-7"
                :disabled="selectedComponentIndex === 0 || !!selectedNested"
                @click="moveComponent(selectedComponentIndex!, 'up')"
              >
                <span class="rotate-180">▼</span>
              </Button>
              <Button
                variant="ghost"
                size="icon"
                class="h-7 w-7"
                :disabled="selectedComponentIndex === selectedScreen!.layout.children.length - 1 || !!selectedNested"
                @click="moveComponent(selectedComponentIndex!, 'down')"
              >
                ▼
              </Button>
              <Button
                variant="ghost"
                size="icon"
                class="h-7 w-7 text-destructive"
                @click="removeComponent(selectedComponentIndex!)"
              >
                <Trash2 class="h-4 w-4" />
              </Button>
            </div>
          </div>

          <!-- Text property -->
          <div v-if="activeEditComponent && 'text' in activeEditComponent" class="space-y-2">
            <Label class="text-xs">{{ activeEditComponent.type === 'RichText' ? 'Markdown' : 'Text' }}</Label>
            <Textarea
              v-if="usesLongText"
              :model-value="activeEditComponent.text"
              @update:model-value="updateComponentProperty('text', $event)"
              rows="6"
              class="text-sm font-mono"
            />
            <Input
              v-else
              :model-value="activeEditComponent.text"
              @update:model-value="updateComponentProperty('text', $event)"
            />
          </div>

          <!-- Markdown toggle (TextBody / TextCaption) -->
          <div
            v-if="activeEditComponent && (activeEditComponent.type === 'TextBody' || activeEditComponent.type === 'TextCaption')"
            class="flex items-center justify-between"
          >
            <Label class="text-xs">Markdown</Label>
            <Switch
              :checked="!!activeEditComponent.markdown"
              @update:checked="updateComponentProperty('markdown', $event)"
            />
          </div>

          <!-- Label property -->
          <div v-if="activeEditComponent && 'label' in activeEditComponent && activeEditComponent.type !== 'Footer'" class="space-y-2">
            <Label class="text-xs">Label</Label>
            <Input
              :model-value="activeEditComponent.label"
              @update:model-value="updateComponentProperty('label', $event)"
            />
          </div>

          <!-- Name property -->
          <div v-if="activeEditComponent && 'name' in activeEditComponent" class="space-y-2">
            <Label class="text-xs">Field Name (Key)</Label>
            <Input
              :model-value="activeEditComponent.name"
              @update:model-value="updateComponentProperty('name', $event)"
              class="font-mono text-sm"
              placeholder="e.g. email, phone, message"
            />
            <p class="text-xs text-muted-foreground">
              This key is used in the response data. Use lowercase with underscores.
            </p>
          </div>

          <!-- TextInput input-type -->
          <div v-if="activeEditComponent?.type === 'TextInput'" class="space-y-2">
            <Label class="text-xs">Input Type</Label>
            <Select
              :model-value="activeEditComponent['input-type'] || 'text'"
              @update:model-value="updateComponentProperty('input-type', $event)"
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="text">Text</SelectItem>
                <SelectItem value="number">Number</SelectItem>
                <SelectItem value="email">Email</SelectItem>
                <SelectItem value="phone">Phone</SelectItem>
                <SelectItem value="password">Password</SelectItem>
                <SelectItem value="passcode">Passcode</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <!-- Required property -->
          <div v-if="activeEditComponent && 'required' in activeEditComponent" class="flex items-center justify-between">
            <Label class="text-xs">Required</Label>
            <Switch
              :checked="activeEditComponent.required"
              @update:checked="updateComponentProperty('required', $event)"
            />
          </div>

          <!-- Options for Dropdown, Radio, Checkbox -->
          <div v-if="activeEditComponent?.['data-source']" class="space-y-2">
            <div class="flex items-center justify-between">
              <Label class="text-xs">Options</Label>
              <Button variant="ghost" size="sm" class="h-6 text-xs" @click="addOption">
                <Plus class="h-3 w-3 mr-1" />
                Add
              </Button>
            </div>
            <div class="space-y-3">
              <div
                v-for="(opt, index) in activeEditComponent['data-source']"
                :key="opt.id"
                class="space-y-1.5 rounded-md border p-2"
              >
                <div class="flex gap-2">
                  <Input
                    :model-value="opt.title"
                    @update:model-value="updateOption(index, 'title', $event)"
                    class="text-sm"
                    placeholder="Title"
                  />
                  <Button
                    variant="ghost"
                    size="icon"
                    class="h-9 w-9 flex-shrink-0"
                    @click="removeOption(index)"
                  >
                    <Trash2 class="h-3 w-3" />
                  </Button>
                </div>
                <Input
                  :model-value="opt.description || ''"
                  @update:model-value="updateOption(index, 'description', $event)"
                  class="text-xs"
                  placeholder="Description (optional)"
                />
                <Input
                  :model-value="opt.metadata || ''"
                  @update:model-value="updateOption(index, 'metadata', $event)"
                  class="text-xs"
                  placeholder="Metadata (optional)"
                />
                <div class="flex items-center gap-2">
                  <label class="inline-flex items-center gap-1 text-[10px] text-muted-foreground cursor-pointer hover:text-foreground">
                    <Upload class="h-3 w-3" />
                    {{ opt.image ? 'Replace img' : 'Add img' }}
                    <input
                      type="file"
                      class="sr-only"
                      accept="image/jpeg,image/png,image/jpg,.jpg,.jpeg,.png"
                      @change="onOptionImageSelected(index, $event)"
                    />
                  </label>
                  <button
                    v-if="opt.image"
                    type="button"
                    class="text-[10px] text-destructive"
                    @click="clearOptionImage(index)"
                  >
                    Remove
                  </button>
                  <img
                    v-if="opt.image"
                    :src="imagePreviewSrc(opt.image)"
                    class="h-6 w-6 rounded object-cover ml-auto"
                    alt=""
                  />
                </div>
              </div>
            </div>
          </div>

          <!-- DatePicker bounds -->
          <div v-if="selectedComponent.type === 'DatePicker' && !selectedNested" class="space-y-3">
            <div class="space-y-2">
              <Label class="text-xs">Helper text</Label>
              <Input
                :model-value="selectedComponent['helper-text'] || ''"
                @update:model-value="updateComponentProperty('helper-text', $event)"
              />
            </div>
            <div class="space-y-2">
              <Label class="text-xs">Min date (YYYY-MM-DD)</Label>
              <Input
                :model-value="selectedComponent['min-date'] || ''"
                @update:model-value="updateComponentProperty('min-date', $event)"
                placeholder="2024-01-01"
                class="font-mono text-sm"
              />
            </div>
            <div class="space-y-2">
              <Label class="text-xs">Max date (YYYY-MM-DD)</Label>
              <Input
                :model-value="selectedComponent['max-date'] || ''"
                @update:model-value="updateComponentProperty('max-date', $event)"
                placeholder="2025-12-31"
                class="font-mono text-sm"
              />
            </div>
            <div class="space-y-2">
              <Label class="text-xs">Unavailable dates</Label>
              <Textarea
                :model-value="formatDateList(selectedComponent['unavailable-dates'])"
                @update:model-value="updateComponentProperty('unavailable-dates', $event)"
                rows="2"
                class="font-mono text-xs"
                placeholder="2024-12-25, 2025-01-01"
              />
              <p class="text-[10px] text-muted-foreground">Comma-separated YYYY-MM-DD</p>
            </div>
          </div>

          <!-- PhotoPicker -->
          <div v-if="selectedComponent.type === 'PhotoPicker' && !selectedNested" class="space-y-3">
            <div class="space-y-2">
              <Label class="text-xs">Description</Label>
              <Input
                :model-value="selectedComponent.description || ''"
                @update:model-value="updateComponentProperty('description', $event)"
              />
            </div>
            <div class="space-y-2">
              <Label class="text-xs">Photo source</Label>
              <Select
                :model-value="selectedComponent['photo-source'] || 'camera_gallery'"
                @update:model-value="updateComponentProperty('photo-source', $event)"
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="camera_gallery">Camera or gallery</SelectItem>
                  <SelectItem value="camera">Camera only</SelectItem>
                  <SelectItem value="gallery">Gallery only</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div class="grid grid-cols-2 gap-2">
              <div class="space-y-2">
                <Label class="text-xs">Min photos</Label>
                <Input
                  type="number"
                  min="0"
                  max="30"
                  :model-value="selectedComponent['min-uploaded-photos'] ?? 0"
                  @update:model-value="updateComponentProperty('min-uploaded-photos', Number($event) || 0)"
                />
              </div>
              <div class="space-y-2">
                <Label class="text-xs">Max photos</Label>
                <Input
                  type="number"
                  min="1"
                  max="30"
                  :model-value="selectedComponent['max-uploaded-photos'] ?? 1"
                  @update:model-value="updateComponentProperty('max-uploaded-photos', Number($event) || 1)"
                />
              </div>
            </div>
            <p class="text-[10px] text-muted-foreground">
              {{ endpointEnabled
                ? 'Whatomate decrypts and saves uploaded photos (proxy mode forwards Data Exchange uploads to your server as-is).'
                : 'Without a data endpoint, Complete Flow can send only 1 photo, so keep Max at 1.' }}
              One upload per screen.
            </p>
          </div>

          <!-- DocumentPicker -->
          <div v-if="selectedComponent.type === 'DocumentPicker' && !selectedNested" class="space-y-3">
            <div class="space-y-2">
              <Label class="text-xs">Description</Label>
              <Input
                :model-value="selectedComponent.description || ''"
                @update:model-value="updateComponentProperty('description', $event)"
              />
            </div>
            <div class="grid grid-cols-2 gap-2">
              <div class="space-y-2">
                <Label class="text-xs">Min docs</Label>
                <Input
                  type="number"
                  min="0"
                  max="30"
                  :model-value="selectedComponent['min-uploaded-documents'] ?? 0"
                  @update:model-value="updateComponentProperty('min-uploaded-documents', Number($event) || 0)"
                />
              </div>
              <div class="space-y-2">
                <Label class="text-xs">Max docs</Label>
                <Input
                  type="number"
                  min="1"
                  max="30"
                  :model-value="selectedComponent['max-uploaded-documents'] ?? 1"
                  @update:model-value="updateComponentProperty('max-uploaded-documents', Number($event) || 1)"
                />
              </div>
            </div>
            <p class="text-[10px] text-muted-foreground">
              {{ endpointEnabled
                ? 'Whatomate decrypts and saves uploaded documents (proxy mode forwards Data Exchange uploads to your server as-is).'
                : 'Without a data endpoint, Complete Flow can send only 1 document, so keep Max at 1.' }}
              One upload per screen.
            </p>
          </div>

          <!-- CalendarPicker -->
          <div v-if="selectedComponent.type === 'CalendarPicker' && !selectedNested" class="space-y-3">
            <div class="space-y-2">
              <Label class="text-xs">Mode</Label>
              <Select
                :model-value="selectedComponent.mode || 'single'"
                @update:model-value="updateComponentProperty('mode', $event)"
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="single">Single date</SelectItem>
                  <SelectItem value="range">Date range</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div class="space-y-2">
              <Label class="text-xs">Min date (YYYY-MM-DD)</Label>
              <Input
                :model-value="selectedComponent['min-date'] || ''"
                @update:model-value="updateComponentProperty('min-date', $event)"
                placeholder="2024-01-01"
                class="font-mono text-sm"
              />
            </div>
            <div class="space-y-2">
              <Label class="text-xs">Max date (YYYY-MM-DD)</Label>
              <Input
                :model-value="selectedComponent['max-date'] || ''"
                @update:model-value="updateComponentProperty('max-date', $event)"
                placeholder="2025-12-31"
                class="font-mono text-sm"
              />
            </div>
          </div>

          <!-- If branches -->
          <div v-if="selectedComponent.type === 'If' && !selectedNested" class="space-y-4">
            <div class="space-y-2">
              <Label class="text-xs">Condition</Label>
              <Input
                :model-value="selectedComponent.condition"
                @update:model-value="updateComponentProperty('condition', $event)"
                class="font-mono text-xs"
                placeholder="${form.opt_in} == true"
              />
              <p class="text-[10px] text-muted-foreground">
                Example: ${form.opt_in} == true
              </p>
            </div>

            <div v-for="branch in (['then', 'else'] as const)" :key="branch" class="space-y-2">
              <div class="flex items-center justify-between">
                <Label class="text-xs capitalize">{{ branch }}</Label>
                <Select @update:model-value="(v) => addNestedToIf(branch, String(v))">
                  <SelectTrigger class="h-7 w-[120px] text-xs">
                    <SelectValue placeholder="Add…" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem v-for="n in nestableComponentTypes" :key="n.type" :value="n.type">
                      {{ n.label }}
                    </SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div class="space-y-1">
                <button
                  v-for="(child, idx) in (selectedComponent[branch] || [])"
                  :key="child.id || idx"
                  type="button"
                  class="w-full flex items-center justify-between gap-2 px-2 py-1.5 rounded border text-xs text-left hover:bg-muted"
                  @click="selectNestedIf(branch, idx)"
                >
                  <span class="truncate">{{ getComponentLabel(child) }}: {{ child.text || child.label || child.name || '' }}</span>
                  <Trash2
                    class="h-3 w-3 text-destructive flex-shrink-0"
                    @click.stop="removeNestedFromIf(branch, idx)"
                  />
                </button>
                <p v-if="!(selectedComponent[branch] || []).length" class="text-[10px] text-muted-foreground px-1">
                  No components
                </p>
              </div>
            </div>
          </div>

          <!-- Switch cases -->
          <div v-if="selectedComponent.type === 'Switch' && !selectedNested" class="space-y-4">
            <div class="space-y-2">
              <Label class="text-xs">Value</Label>
              <Input
                :model-value="selectedComponent.value"
                @update:model-value="updateComponentProperty('value', $event)"
                class="font-mono text-xs"
                placeholder="${form.field_name}"
              />
            </div>
            <div class="flex items-center justify-between">
              <Label class="text-xs">Cases</Label>
              <Button variant="ghost" size="sm" class="h-6 text-xs" @click="addSwitchCase">
                <Plus class="h-3 w-3 mr-1" />
                Case
              </Button>
            </div>
            <div v-for="key in switchCaseKeys" :key="key" class="space-y-2 border rounded-md p-2">
              <div class="flex gap-1">
                <Input
                  :model-value="key"
                  class="font-mono text-xs h-7"
                  @blur="(e: FocusEvent) => renameSwitchCase(key, (e.target as HTMLInputElement).value.trim())"
                />
                <Button variant="ghost" size="icon" class="h-7 w-7 text-destructive" @click="removeSwitchCase(key)">
                  <Trash2 class="h-3 w-3" />
                </Button>
              </div>
              <Select @update:model-value="(v) => addNestedToSwitchCase(key, String(v))">
                <SelectTrigger class="h-7 text-xs">
                  <SelectValue placeholder="Add component…" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="n in nestableComponentTypes" :key="n.type" :value="n.type">
                    {{ n.label }}
                  </SelectItem>
                </SelectContent>
              </Select>
              <button
                v-for="(child, idx) in (selectedComponent.cases[key] || [])"
                :key="child.id || idx"
                type="button"
                class="w-full flex items-center justify-between gap-2 px-2 py-1 rounded border text-xs text-left hover:bg-muted"
                @click="selectNestedSwitchCase(key, idx)"
              >
                <span class="truncate">{{ getComponentLabel(child) }}</span>
                <Trash2
                  class="h-3 w-3 text-destructive flex-shrink-0"
                  @click.stop="removeNestedFromSwitchCase(key, idx)"
                />
              </button>
            </div>
          </div>

          <!-- Image specific -->
          <div v-if="selectedComponent.type === 'Image'" class="space-y-4">
            <div class="space-y-2">
              <Label class="text-xs">Image file</Label>
              <div class="flex gap-2">
                <label class="flex-1 inline-flex items-center justify-center gap-1.5 h-8 px-3 rounded-md border border-input bg-background text-xs font-medium cursor-pointer hover:bg-accent hover:text-accent-foreground">
                  <Upload class="h-3.5 w-3.5" />
                  {{ selectedComponent.src ? 'Replace image' : 'Choose file' }}
                  <input
                    type="file"
                    class="sr-only"
                    accept="image/jpeg,image/png,image/jpg,.jpg,.jpeg,.png"
                    @change="onImageFileSelected"
                  />
                </label>
                <Button
                  v-if="selectedComponent.src"
                  type="button"
                  variant="ghost"
                  size="icon"
                  class="h-8 w-8 text-destructive"
                  @click="clearImageSrc"
                >
                  <Trash2 class="h-3.5 w-3.5" />
                </Button>
              </div>
              <p class="text-xs text-muted-foreground">JPEG or PNG, max 300KB. Converted to base64 for Meta.</p>
              <div
                v-if="selectedComponent.src"
                class="rounded-md border overflow-hidden bg-muted/40"
              >
                <img
                  :src="imagePreviewSrc(selectedComponent.src)"
                  :alt="selectedComponent['alt-text'] || 'Preview'"
                  class="w-full max-h-32 object-contain"
                />
              </div>
            </div>

            <div class="space-y-2">
              <Label class="text-xs">Alt text</Label>
              <Input
                :model-value="selectedComponent['alt-text'] || ''"
                @update:model-value="updateComponentProperty('alt-text', $event)"
                placeholder="Describe the image"
              />
            </div>

            <div class="grid grid-cols-2 gap-2">
              <div class="space-y-2">
                <Label class="text-xs">Width</Label>
                <Input
                  type="number"
                  min="1"
                  :model-value="selectedComponent.width ?? ''"
                  @update:model-value="updateComponentProperty('width', $event === '' ? undefined : Number($event))"
                  placeholder="auto"
                />
              </div>
              <div class="space-y-2">
                <Label class="text-xs">Height</Label>
                <Input
                  type="number"
                  min="1"
                  :model-value="selectedComponent.height ?? ''"
                  @update:model-value="updateComponentProperty('height', $event === '' ? undefined : Number($event))"
                  placeholder="auto"
                />
              </div>
            </div>

            <div class="space-y-2">
              <Label class="text-xs">Aspect ratio</Label>
              <Input
                type="number"
                min="0.1"
                step="0.01"
                :model-value="selectedComponent['aspect-ratio'] ?? 1"
                @update:model-value="updateComponentProperty('aspect-ratio', Number($event) || 1)"
              />
            </div>

            <div class="space-y-2">
              <Label class="text-xs">Scale type</Label>
              <Select
                :model-value="selectedComponent['scale-type'] || 'contain'"
                @update:model-value="updateComponentProperty('scale-type', $event)"
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="contain">Contain</SelectItem>
                  <SelectItem value="cover">Cover</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          <!-- OptIn Read more -->
          <div v-if="activeEditComponent?.type === 'OptIn'" class="space-y-3">
            <div class="flex items-center justify-between">
              <Label class="text-xs">Read more link</Label>
              <Switch
                :checked="!!activeEditComponent['on-click-action']"
                @update:checked="setOptInReadMore($event)"
              />
            </div>
            <template v-if="activeEditComponent['on-click-action']">
              <div class="space-y-2">
                <Label class="text-xs">Read more action</Label>
                <Select
                  :model-value="activeEditComponent['on-click-action']?.name || 'open_url'"
                  @update:model-value="updateClickAction(String($event))"
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="open_url">Open URL</SelectItem>
                    <SelectItem value="navigate">Navigate to Screen</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div v-if="activeEditComponent['on-click-action']?.name === 'open_url'" class="space-y-2">
                <Label class="text-xs">URL</Label>
                <Input
                  :model-value="activeEditComponent['on-click-action']?.url || ''"
                  @update:model-value="updateComponentProperty('on-click-action', {
                    name: 'open_url',
                    url: $event
                  })"
                  placeholder="https://"
                />
              </div>
              <div v-else-if="activeEditComponent['on-click-action']?.name === 'navigate'" class="space-y-2">
                <Label class="text-xs">Target Screen</Label>
                <Select
                  :model-value="activeEditComponent['on-click-action']?.next?.name || ''"
                  @update:model-value="updateComponentProperty('on-click-action', {
                    name: 'navigate',
                    next: { type: 'screen', name: $event },
                    payload: {}
                  })"
                >
                  <SelectTrigger>
                    <SelectValue placeholder="Select screen" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem
                      v-for="screen in screens.filter(s => s.id !== selectedScreen?.id)"
                      :key="screen.id"
                      :value="screen.id"
                    >
                      {{ screen.title }}
                    </SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </template>
          </div>

          <!-- EmbeddedLink actions -->
          <div v-if="selectedComponent.type === 'EmbeddedLink'" class="space-y-3">
            <div class="space-y-2">
              <Label class="text-xs">Action</Label>
              <Select
                :model-value="selectedComponent['on-click-action']?.name || 'open_url'"
                @update:model-value="updateClickAction(String($event))"
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="open_url">Open URL</SelectItem>
                  <SelectItem value="navigate">Navigate to Screen</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div v-if="selectedComponent['on-click-action']?.name === 'open_url'" class="space-y-2">
              <Label class="text-xs">URL</Label>
              <Input
                :model-value="selectedComponent['on-click-action']?.url || ''"
                @update:model-value="updateComponentProperty('on-click-action', {
                  name: 'open_url',
                  url: $event
                })"
                placeholder="https://"
              />
            </div>
            <div v-else-if="selectedComponent['on-click-action']?.name === 'navigate'" class="space-y-2">
              <Label class="text-xs">Target Screen</Label>
              <Select
                :model-value="selectedComponent['on-click-action']?.next?.name || ''"
                @update:model-value="updateComponentProperty('on-click-action', {
                  name: 'navigate',
                  next: { type: 'screen', name: $event },
                  payload: {}
                })"
              >
                <SelectTrigger>
                  <SelectValue placeholder="Select screen" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem
                    v-for="screen in screens.filter(s => s.id !== selectedScreen?.id)"
                    :key="screen.id"
                    :value="screen.id"
                  >
                    {{ screen.title }}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          <!-- Footer specific -->
          <div v-if="selectedComponent.type === 'Footer'" class="space-y-4">
            <div class="space-y-2">
              <Label class="text-xs">Button Text</Label>
              <Input
                :model-value="selectedComponent.label"
                @update:model-value="updateComponentProperty('label', $event)"
              />
            </div>
            <div class="space-y-2">
              <Label class="text-xs">Action</Label>
              <Select
                :model-value="selectedComponent['on-click-action']?.name || 'complete'"
                @update:model-value="updateClickAction(String($event))"
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="complete">Complete Flow</SelectItem>
                  <SelectItem value="navigate">Navigate to Screen</SelectItem>
                  <SelectItem value="open_url">Open URL</SelectItem>
                  <SelectItem value="data_exchange">Send to Endpoint (Data Exchange)</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div
              v-if="selectedComponent['on-click-action']?.name === 'data_exchange'"
              class="space-y-2"
            >
              <Label class="text-xs">Then</Label>
              <Select
                :model-value="selectedComponent['on-click-action']?.payload?.next_screen || FINISH_FLOW"
                @update:model-value="setDataExchangeNextScreen(String($event))"
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem :value="FINISH_FLOW">Finish flow</SelectItem>
                  <SelectItem
                    v-for="screen in screens.filter(s => s.id !== selectedScreen?.id)"
                    :key="screen.id"
                    :value="screen.id"
                  >
                    Go to {{ screen.title }}
                  </SelectItem>
                </SelectContent>
              </Select>
              <p class="text-[11px] text-muted-foreground">
                The screen's answers go to your endpoint. The built-in endpoint returns the chosen screen, or finishes and sends all answers to the chatbot.
              </p>
              <p v-if="!endpointEnabled" class="text-[11px] text-amber-500">
                Turn on the data endpoint in Endpoint settings, or Meta will reject this flow.
              </p>
            </div>
            <div
              v-if="selectedComponent['on-click-action']?.name === 'navigate'"
              class="space-y-2"
            >
              <Label class="text-xs">Target Screen</Label>
              <Select
                :model-value="selectedComponent['on-click-action']?.next?.name || ''"
                @update:model-value="updateComponentProperty('on-click-action', {
                  name: 'navigate',
                  next: { type: 'screen', name: $event },
                  payload: {}
                })"
              >
                <SelectTrigger>
                  <SelectValue placeholder="Select screen" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem
                    v-for="screen in screens.filter(s => s.id !== selectedScreen?.id)"
                    :key="screen.id"
                    :value="screen.id"
                  >
                    {{ screen.title }}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div
              v-if="selectedComponent['on-click-action']?.name === 'open_url'"
              class="space-y-2"
            >
              <Label class="text-xs">URL</Label>
              <Input
                :model-value="selectedComponent['on-click-action']?.url || ''"
                @update:model-value="updateComponentProperty('on-click-action', {
                  name: 'open_url',
                  url: $event
                })"
                placeholder="https://"
              />
            </div>
          </div>
        </div>
        <div v-else class="p-4 text-center text-sm text-muted-foreground">
          Select a component to edit its properties
        </div>
      </ScrollArea>
    </Card>
  </div>
</template>
