<script setup lang="ts">
import { computed } from 'vue'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Type,
  ChevronDown,
  ChevronRight,
  Calendar,
  CalendarRange,
  Image,
  Camera,
  FileUp,
  GitBranch,
  Split,
  List,
  Tags,
  GalleryHorizontal,
} from 'lucide-vue-next'

export interface PreviewComponent {
  id?: string
  type: string
  name?: string
  label?: string
  text?: string
  required?: boolean
  description?: string
  mode?: string
  src?: string
  then?: PreviewComponent[]
  else?: PreviewComponent[]
  cases?: Record<string, PreviewComponent[]>
  condition?: string
  value?: string
  images?: Array<{ src?: string; 'alt-text'?: string }>
  'list-items'?: any[]
  'data-source'?: any[]
  'on-click-action'?: any
  'input-type'?: string
  'helper-text'?: string
  'aspect-ratio'?: number | string
  'scale-type'?: string
  'alt-text'?: string
  'media-size'?: string
  height?: number
  children?: PreviewComponent[]
  [key: string]: any
}

const props = withDefaults(defineProps<{
  comp: PreviewComponent
  compact?: boolean
  playground?: boolean
  /** When true with playground, inputs update form values for If/Switch. */
  interactive?: boolean
  formValues?: Record<string, unknown>
  dark?: boolean
  labelForType?: (type: string) => string
}>(), {
  compact: false,
  playground: false,
  interactive: false,
  formValues: () => ({}),
  dark: false,
  labelForType: (type: string) => type,
})

const emit = defineEmits<{
  'set-form-value': [name: string, value: unknown]
}>()

function setValue(value: unknown) {
  if (!props.interactive || !props.comp.name) return
  emit('set-form-value', props.comp.name, value)
}

function currentValue(): unknown {
  if (!props.comp.name) return undefined
  return props.formValues?.[props.comp.name]
}

function isSelectedOption(id: string): boolean {
  const v = currentValue()
  if (Array.isArray(v)) return v.map(String).includes(String(id))
  return v != null && String(v) === String(id)
}

function toggleMulti(id: string) {
  if (!props.interactive || !props.comp.name) return
  const cur = currentValue()
  const list = Array.isArray(cur) ? [...cur.map(String)] : []
  const idx = list.indexOf(String(id))
  if (idx >= 0) list.splice(idx, 1)
  else list.push(String(id))
  const max = Number(props.comp['max-selected-items'] || 0)
  if (max > 0 && list.length > max) list.splice(0, list.length - max)
  setValue(list)
}

const t = computed(() => ({
  heading: props.dark ? 'text-zinc-100' : 'text-zinc-900',
  body: props.dark ? 'text-zinc-200' : 'text-zinc-800',
  muted: props.dark ? 'text-zinc-400' : 'text-zinc-500',
  label: props.dark ? 'text-zinc-100' : 'text-zinc-900',
  link: props.dark ? 'text-emerald-400' : 'text-emerald-700',
  surface: props.dark ? 'bg-zinc-800 border-zinc-600' : 'bg-white border-zinc-300',
  surfaceSoft: props.dark ? 'bg-zinc-800/80 border-zinc-700' : 'bg-white border-zinc-200',
  optionRow: props.dark ? 'bg-zinc-800' : 'bg-white',
  divide: props.dark ? 'divide-zinc-700 border-zinc-700' : 'divide-zinc-100 border-zinc-200',
  code: props.dark ? 'bg-zinc-800 text-zinc-300' : 'bg-zinc-100 text-zinc-700',
  dashed: props.dark ? 'border-zinc-600 bg-zinc-800/50 text-zinc-400' : 'border-zinc-300 bg-zinc-50 text-zinc-500',
  imageBg: props.dark ? 'bg-zinc-800' : 'bg-zinc-100',
  chip: props.dark ? 'bg-zinc-800 border-zinc-600 text-zinc-200' : 'bg-zinc-100 border-zinc-300 text-zinc-800',
  badge: props.dark ? 'bg-emerald-500/20 text-emerald-300' : 'bg-emerald-100 text-emerald-800',
}))

function imagePreviewSrc(src?: string): string {
  if (!src) return ''
  if (src.startsWith('data:image/') || src.startsWith('http://') || src.startsWith('https://') || src.startsWith('blob:')) {
    return src
  }
  const mime = src.startsWith('iVBOR') ? 'image/png'
    : src.startsWith('/9j/') ? 'image/jpeg'
    : src.startsWith('UklGR') || src.startsWith('RIFF') ? 'image/webp'
    : 'image/jpeg'
  return `data:${mime};base64,${src}`
}

function summarizeNested(list?: PreviewComponent[]): string {
  if (!list?.length) return 'empty'
  return list.map(c => props.labelForType(c.type)).join(', ')
}

function richTextDisplay(text: unknown): string {
  if (Array.isArray(text)) return text.map(String).join('\n\n')
  return typeof text === 'string' ? text : ''
}

const listItems = computed(() => Array.isArray(props.comp['list-items']) ? props.comp['list-items'] : [])
const dataSource = computed(() => Array.isArray(props.comp['data-source']) ? props.comp['data-source'] : [])
const carouselImages = computed(() => Array.isArray(props.comp.images) ? props.comp.images : [])

const carouselAspect = computed(() => {
  const ar = String(props.comp['aspect-ratio'] || '4:3')
  if (ar === '16:9') return '16 / 9'
  if (ar.includes(':')) {
    const [w, h] = ar.split(':').map(Number)
    if (w && h) return `${w} / ${h}`
  }
  return '4 / 3'
})
</script>

<template>
  <div :class="compact ? 'space-y-1' : 'space-y-1.5'">
    <template v-if="comp.type === 'TextHeading'">
      <h2 class="text-xl font-bold leading-snug" :class="t.heading">{{ comp.text || 'Heading' }}</h2>
    </template>
    <template v-else-if="comp.type === 'TextSubheading'">
      <h3 class="text-base font-semibold leading-snug" :class="t.heading">{{ comp.text || 'Subheading' }}</h3>
    </template>
    <template v-else-if="comp.type === 'TextBody'">
      <p
        class="text-sm whitespace-pre-wrap leading-relaxed"
        :class="[
          t.body,
          comp['font-weight'] === 'bold' || comp['font-weight'] === 'bold_italic' ? 'font-semibold' : '',
          comp['font-weight'] === 'italic' || comp['font-weight'] === 'bold_italic' ? 'italic' : '',
          comp.strikethrough ? 'line-through' : '',
        ]"
      >{{ comp.text || 'Body text' }}</p>
    </template>
    <template v-else-if="comp.type === 'TextCaption'">
      <p
        class="text-xs whitespace-pre-wrap leading-relaxed"
        :class="[
          t.muted,
          comp['font-weight'] === 'bold' || comp['font-weight'] === 'bold_italic' ? 'font-semibold' : '',
          comp['font-weight'] === 'italic' || comp['font-weight'] === 'bold_italic' ? 'italic' : '',
          comp.strikethrough ? 'line-through' : '',
        ]"
      >{{ comp.text || 'Caption' }}</p>
    </template>
    <template v-else-if="comp.type === 'RichText'">
      <div
        class="text-sm whitespace-pre-wrap border-l-2 pl-2.5 leading-relaxed"
        :class="[t.body, dark ? 'border-zinc-600' : 'border-zinc-300']"
      >
        {{ richTextDisplay(comp.text) || 'Rich text' }}
      </div>
    </template>

    <template v-else-if="comp.type === 'TextInput' || comp.type === 'TextArea'">
      <Label class="text-sm font-medium" :class="t.label">
        {{ comp.label || 'Label' }}
        <span v-if="comp.required" class="text-red-500">*</span>
      </Label>
      <Input
        v-if="comp.type === 'TextInput'"
        :disabled="!interactive"
        :model-value="String(currentValue() ?? '')"
        :type="comp['input-type'] === 'password' || comp['input-type'] === 'passcode' ? 'password' : (comp['input-type'] === 'email' ? 'email' : 'text')"
        :placeholder="comp['helper-text'] || comp.label || 'Enter text'"
        class="mt-1.5"
        :class="t.surface"
        @update:model-value="setValue($event)"
        @click.stop
      />
      <textarea
        v-else
        :disabled="!interactive"
        :value="String(currentValue() ?? '')"
        :placeholder="comp['helper-text'] || comp.label || 'Enter text'"
        class="mt-1.5 w-full p-2.5 rounded-lg border text-sm min-h-[80px]"
        :class="[t.surface, t.body]"
        rows="3"
        @input="setValue(($event.target as HTMLTextAreaElement).value)"
        @click.stop
      />
      <p v-if="comp['helper-text']" class="text-[11px] mt-1" :class="t.muted">{{ comp['helper-text'] }}</p>
    </template>

    <template v-else-if="comp.type === 'Dropdown'">
      <Label class="text-sm font-medium" :class="t.label">
        {{ comp.label || 'Select an option' }}
        <span v-if="comp.required" class="text-red-500">*</span>
      </Label>
      <div class="relative mt-1.5" @click.stop>
        <select
          v-if="interactive"
          class="w-full appearance-none p-2.5 rounded-lg border text-sm pr-8"
          :class="[t.surface, t.body]"
          :value="String(currentValue() ?? '')"
          @change="setValue(($event.target as HTMLSelectElement).value)"
        >
          <option v-for="opt in dataSource" :key="opt.id" :value="opt.id">{{ opt.title }}</option>
        </select>
        <div
          v-else
          class="p-2.5 rounded-lg border text-sm flex items-center justify-between"
          :class="[t.surface, t.muted]"
        >
          <span class="truncate">
            {{ dataSource.find(o => String(o.id) === String(currentValue()))?.title || dataSource[0]?.title || 'Select...' }}
          </span>
          <ChevronDown class="h-4 w-4 shrink-0" />
        </div>
        <ChevronDown
          v-if="interactive"
          class="pointer-events-none absolute right-2.5 top-1/2 -translate-y-1/2 h-4 w-4"
          :class="t.muted"
        />
      </div>
      <p v-if="!compact && dataSource.length" class="text-[10px] mt-1" :class="t.muted">
        {{ dataSource.length }} option{{ dataSource.length === 1 ? '' : 's' }}
      </p>
    </template>

    <template v-else-if="comp.type === 'RadioButtonsGroup'">
      <Label class="text-sm mb-2 block font-medium" :class="t.label">
        {{ comp.label || 'Choose one' }}
        <span v-if="comp.required" class="text-red-500">*</span>
      </Label>
      <div
        :class="playground
          ? ['space-y-0 divide-y rounded-lg border overflow-hidden', t.divide]
          : 'space-y-2'"
      >
        <button
          v-for="opt in dataSource"
          :key="opt.id"
          type="button"
          :disabled="!interactive"
          :class="playground ? ['flex items-center gap-3 px-3 py-2.5 w-full text-left', t.optionRow] : 'flex items-start gap-2 w-full text-left'"
          @click.stop="setValue(opt.id)"
        >
          <div
            :class="playground
              ? ['h-5 w-5 rounded-full border-2 flex-shrink-0 flex items-center justify-center', dark ? 'border-zinc-500' : 'border-zinc-400']
              : ['mt-0.5 h-4 w-4 rounded-full border-2 flex-shrink-0 flex items-center justify-center', dark ? 'border-zinc-500' : 'border-zinc-400']"
          >
            <span
              v-if="isSelectedOption(opt.id)"
              class="h-2.5 w-2.5 rounded-full bg-[#00a884]"
            />
          </div>
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <img v-if="opt.image" :src="imagePreviewSrc(opt.image)" class="h-6 w-6 rounded object-cover" alt="" />
              <span class="text-sm" :class="t.heading">{{ opt.title }}</span>
            </div>
            <p v-if="opt.description" class="text-xs" :class="t.muted">{{ opt.description }}</p>
          </div>
        </button>
        <p v-if="!dataSource.length" class="text-xs px-1" :class="t.muted">No options</p>
      </div>
    </template>

    <template v-else-if="comp.type === 'CheckboxGroup'">
      <Label class="text-sm mb-2 block font-medium" :class="t.label">
        {{ comp.label || 'Select options' }}
        <span v-if="comp.required" class="text-red-500">*</span>
      </Label>
      <div
        :class="playground
          ? ['space-y-0 divide-y rounded-lg border overflow-hidden', t.divide]
          : 'space-y-2'"
      >
        <button
          v-for="opt in dataSource"
          :key="opt.id"
          type="button"
          :disabled="!interactive"
          :class="playground ? ['flex items-center gap-3 px-3 py-2.5 w-full text-left', t.optionRow] : 'flex items-start gap-2 w-full text-left'"
          @click.stop="toggleMulti(opt.id)"
        >
          <div
            :class="[
              playground
                ? 'h-5 w-5 rounded border-2 flex-shrink-0 flex items-center justify-center'
                : 'mt-0.5 h-4 w-4 rounded border flex-shrink-0 flex items-center justify-center',
              isSelectedOption(opt.id) ? 'border-[#00a884] bg-[#00a884]' : (dark ? 'border-zinc-500' : 'border-zinc-400'),
            ]"
          >
            <span v-if="isSelectedOption(opt.id)" class="text-white text-[10px] font-bold leading-none">✓</span>
          </div>
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <img v-if="opt.image" :src="imagePreviewSrc(opt.image)" class="h-6 w-6 rounded object-cover" alt="" />
              <span class="text-sm" :class="t.heading">{{ opt.title }}</span>
            </div>
            <p v-if="opt.description" class="text-xs" :class="t.muted">{{ opt.description }}</p>
          </div>
        </button>
        <p v-if="!dataSource.length" class="text-xs px-1" :class="t.muted">No options</p>
      </div>
    </template>

    <template v-else-if="comp.type === 'ChipsSelector'">
      <Label class="text-sm font-medium" :class="t.label">
        {{ comp.label || 'Select chips' }}
        <span v-if="comp.required" class="text-red-500">*</span>
      </Label>
      <p v-if="comp.description" class="text-xs mt-0.5" :class="t.muted">{{ comp.description }}</p>
      <div class="mt-2 flex flex-wrap gap-1.5">
        <button
          v-for="opt in dataSource"
          :key="opt.id"
          type="button"
          :disabled="!interactive || opt.enabled === false"
          class="inline-flex items-center rounded-full border px-2.5 py-1 text-xs transition-colors"
          :class="[
            isSelectedOption(opt.id)
              ? 'bg-[#00a884] border-[#00a884] text-white'
              : t.chip,
            opt.enabled === false ? 'opacity-40' : '',
          ]"
          @click.stop="toggleMulti(opt.id)"
        >
          {{ opt.title }}
        </button>
        <span v-if="!dataSource.length" class="text-xs" :class="t.muted">No chips</span>
      </div>
    </template>

    <template v-else-if="comp.type === 'OptIn'">
      <button
        type="button"
        class="flex items-start gap-2 w-full text-left"
        :disabled="!interactive"
        @click.stop="setValue(!currentValue())"
      >
        <div
          class="mt-0.5 h-4 w-4 rounded border flex-shrink-0 flex items-center justify-center"
          :class="currentValue() ? 'border-[#00a884] bg-[#00a884]' : (dark ? 'border-zinc-500' : 'border-zinc-400')"
        >
          <span v-if="currentValue()" class="text-white text-[9px] font-bold leading-none">✓</span>
        </div>
        <div class="text-sm" :class="t.body">
          <span>{{ comp.label || 'I agree' }}</span>
          <span v-if="comp.required" class="text-red-500">*</span>
          <span v-if="comp['on-click-action']" class="block text-xs underline mt-1" :class="t.link">Read more</span>
        </div>
      </button>
    </template>

    <template v-else-if="comp.type === 'DatePicker'">
      <Label class="text-sm font-medium" :class="t.label">
        {{ comp.label || 'Select date' }}
        <span v-if="comp.required" class="text-red-500">*</span>
      </Label>
      <div
        class="mt-1.5 p-2.5 rounded-lg border text-sm flex items-center justify-between"
        :class="[t.surface, t.muted]"
      >
        <span>Select date...</span>
        <Calendar class="h-4 w-4" />
      </div>
      <p v-if="comp['helper-text']" class="text-xs mt-1" :class="t.muted">{{ comp['helper-text'] }}</p>
    </template>

    <template v-else-if="comp.type === 'CalendarPicker'">
      <Label class="text-sm font-medium" :class="t.label">
        {{ comp.label || 'Select date' }}
        <span v-if="comp.required" class="text-red-500">*</span>
      </Label>
      <div
        class="mt-1.5 p-2.5 rounded-lg border text-sm flex items-center justify-between"
        :class="[t.surface, t.muted]"
      >
        <span>{{ comp.mode === 'range' ? 'Select date range...' : 'Select date...' }}</span>
        <CalendarRange class="h-4 w-4" />
      </div>
    </template>

    <template v-else-if="comp.type === 'PhotoPicker'">
      <Label class="text-sm font-medium" :class="t.label">
        {{ comp.label || 'Upload a photo' }}
        <span v-if="(comp['min-uploaded-photos'] || 0) > 0" class="text-red-500">*</span>
      </Label>
      <p v-if="comp.description" class="text-xs mt-0.5" :class="t.muted">{{ comp.description }}</p>
      <div
        class="mt-2 border border-dashed rounded-lg p-4 flex flex-col items-center gap-1"
        :class="t.dashed"
      >
        <Camera class="h-6 w-6" />
        <span class="text-xs">Photo upload</span>
      </div>
    </template>

    <template v-else-if="comp.type === 'DocumentPicker'">
      <Label class="text-sm font-medium" :class="t.label">
        {{ comp.label || 'Upload a document' }}
        <span v-if="(comp['min-uploaded-documents'] || 0) > 0" class="text-red-500">*</span>
      </Label>
      <p v-if="comp.description" class="text-xs mt-0.5" :class="t.muted">{{ comp.description }}</p>
      <div
        class="mt-2 border border-dashed rounded-lg p-4 flex flex-col items-center gap-1"
        :class="t.dashed"
      >
        <FileUp class="h-6 w-6" />
        <span class="text-xs">Document upload</span>
      </div>
    </template>

    <template v-else-if="comp.type === 'NavigationList'">
      <div v-if="comp.label" class="text-sm font-medium mb-1" :class="t.label">{{ comp.label }}</div>
      <p v-if="comp.description" class="text-xs mb-2" :class="t.muted">{{ comp.description }}</p>
      <div class="space-y-1.5">
        <div
          v-for="(item, idx) in listItems"
          :key="item.id || idx"
          class="flex items-center gap-2.5 rounded-lg border px-2.5 py-2"
          :class="t.surfaceSoft"
        >
          <div
            v-if="item.start?.image"
            class="rounded-md overflow-hidden flex-shrink-0 bg-zinc-200"
            :class="comp['media-size'] === 'large' ? 'h-12 w-12' : 'h-9 w-9'"
          >
            <img
              :src="imagePreviewSrc(item.start.image)"
              :alt="item.start['alt-text'] || ''"
              class="h-full w-full object-cover"
            />
          </div>
          <div
            v-else
            class="rounded-md flex-shrink-0 flex items-center justify-center"
            :class="[t.imageBg, comp['media-size'] === 'large' ? 'h-12 w-12' : 'h-9 w-9']"
          >
            <List class="h-4 w-4" :class="t.muted" />
          </div>
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-1.5">
              <span class="text-sm font-medium truncate" :class="t.heading">
                {{ item['main-content']?.title || `Item ${idx + 1}` }}
              </span>
              <span
                v-if="item.badge"
                class="text-[9px] font-semibold px-1.5 py-0.5 rounded-full shrink-0"
                :class="t.badge"
              >{{ item.badge }}</span>
            </div>
            <p v-if="item['main-content']?.description" class="text-[11px] truncate" :class="t.muted">
              {{ item['main-content'].description }}
            </p>
            <div v-if="item.tags?.length" class="flex flex-wrap gap-1 mt-1">
              <span
                v-for="tag in item.tags.slice(0, 3)"
                :key="tag"
                class="text-[9px] px-1.5 py-0.5 rounded border"
                :class="t.chip"
              >{{ tag }}</span>
            </div>
          </div>
          <div v-if="item.end?.title || item.end?.description" class="text-right shrink-0 max-w-[64px]">
            <p v-if="item.end.title" class="text-[11px] font-medium truncate" :class="t.heading">{{ item.end.title }}</p>
            <p v-if="item.end.description" class="text-[10px] truncate" :class="t.muted">{{ item.end.description }}</p>
          </div>
          <ChevronRight class="h-4 w-4 shrink-0" :class="t.muted" />
        </div>
        <p v-if="!listItems.length" class="text-xs flex items-center gap-1.5" :class="t.muted">
          <List class="h-3.5 w-3.5" /> Empty navigation list
        </p>
      </div>
    </template>

    <template v-else-if="comp.type === 'ImageCarousel'">
      <div
        class="relative rounded-lg overflow-hidden"
        :class="t.imageBg"
        :style="{ aspectRatio: carouselAspect }"
      >
        <template v-if="carouselImages.length">
          <img
            :src="imagePreviewSrc(carouselImages[0].src)"
            :alt="carouselImages[0]['alt-text'] || 'Carousel image'"
            class="absolute inset-0 w-full h-full"
            :class="comp['scale-type'] === 'cover' ? 'object-cover' : 'object-contain'"
          />
          <div class="absolute bottom-2 left-0 right-0 flex justify-center gap-1">
            <span
              v-for="(_, i) in carouselImages"
              :key="i"
              class="h-1.5 w-1.5 rounded-full"
              :class="i === 0 ? 'bg-white' : 'bg-white/40'"
            />
          </div>
          <span
            class="absolute top-2 right-2 text-[10px] font-medium px-1.5 py-0.5 rounded-full bg-black/50 text-white"
          >1 / {{ carouselImages.length }}</span>
        </template>
        <div v-else class="absolute inset-0 flex flex-col items-center justify-center gap-1" :class="t.muted">
          <GalleryHorizontal class="h-8 w-8" />
          <span class="text-xs">Image carousel</span>
        </div>
      </div>
    </template>

    <template v-else-if="comp.type === 'If'">
      <div class="space-y-2 text-xs" :class="t.body">
        <div class="flex items-center gap-1.5 font-medium">
          <GitBranch class="h-3.5 w-3.5" />
          If
        </div>
        <code class="block text-[10px] px-1.5 py-1 rounded break-all" :class="t.code">{{ comp.condition || 'condition' }}</code>
        <p class="pl-2 border-l-2 border-emerald-500/50" :class="t.muted">Then: {{ summarizeNested(comp.then) }}</p>
        <p class="pl-2 border-l-2 border-amber-500/50" :class="t.muted">Else: {{ summarizeNested(comp.else) }}</p>
      </div>
    </template>

    <template v-else-if="comp.type === 'Switch'">
      <div class="space-y-2 text-xs" :class="t.body">
        <div class="flex items-center gap-1.5 font-medium">
          <Split class="h-3.5 w-3.5" />
          Switch
        </div>
        <code class="block text-[10px] px-1.5 py-1 rounded break-all" :class="t.code">{{ comp.value || 'value' }}</code>
        <div v-for="(children, key) in (comp.cases || {})" :key="key" class="pl-2 border-l-2 border-sky-500/40">
          <p :class="t.muted">
            <span class="font-mono">{{ key }}</span>: {{ summarizeNested(children) }}
          </p>
        </div>
      </div>
    </template>

    <template v-else-if="comp.type === 'Image'">
      <div
        class="rounded-md overflow-hidden flex items-center justify-center"
        :class="t.imageBg"
        :style="{
          aspectRatio: String(comp['aspect-ratio'] || 1),
          maxHeight: comp.height ? `${comp.height}px` : undefined,
        }"
      >
        <img
          v-if="comp.src"
          :src="imagePreviewSrc(comp.src)"
          :alt="comp['alt-text'] || 'Flow image'"
          class="w-full h-full"
          :class="comp['scale-type'] === 'cover' ? 'object-cover' : 'object-contain'"
        />
        <Image v-else class="h-8 w-8 my-8" :class="t.muted" />
      </div>
    </template>

    <template v-else-if="comp.type === 'EmbeddedLink'">
      <span class="text-sm underline" :class="t.link">{{ comp.text || 'Learn more' }}</span>
    </template>

    <template v-else-if="comp.type === 'Footer'">
      <button
        type="button"
        class="w-full h-11 rounded-full text-sm font-semibold text-white bg-[#00a884] hover:bg-[#008f72] transition-colors"
      >
        {{ comp.label || 'Continue' }}
      </button>
    </template>

    <template v-else-if="comp.type === 'Form'">
      <div class="space-y-1 text-xs" :class="t.body">
        <p class="font-medium flex items-center gap-1.5">
          <Tags class="h-3.5 w-3.5" />
          Form{{ comp.name ? ` · ${comp.name}` : '' }}
        </p>
        <p :class="t.muted">{{ summarizeNested(comp.children) }}</p>
      </div>
    </template>

    <template v-else>
      <div class="flex items-center gap-2 text-sm" :class="t.muted">
        <Type class="h-4 w-4" />
        {{ labelForType(comp.type) }}
      </div>
    </template>
  </div>
</template>
