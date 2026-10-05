<script setup lang="ts">
import { computed } from 'vue'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Type,
  ChevronDown,
  Calendar,
  CalendarRange,
  Image,
  Camera,
  FileUp,
  GitBranch,
  Split,
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
  'data-source'?: any[]
  'on-click-action'?: any
  'input-type'?: string
  'helper-text'?: string
  'aspect-ratio'?: number
  'scale-type'?: string
  'alt-text'?: string
  height?: number
  [key: string]: any
}

const props = withDefaults(defineProps<{
  comp: PreviewComponent
  compact?: boolean
  playground?: boolean
  dark?: boolean
  labelForType?: (type: string) => string
}>(), {
  compact: false,
  playground: false,
  dark: false,
  labelForType: (type: string) => type,
})

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
}))

function imagePreviewSrc(src?: string): string {
  if (!src) return ''
  if (src.startsWith('data:image/')) return src
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
</script>

<template>
  <div :class="compact ? 'space-y-1' : 'space-y-1.5'">
    <template v-if="comp.type === 'TextHeading'">
      <h2 class="text-xl font-bold" :class="t.heading">{{ comp.text }}</h2>
    </template>
    <template v-else-if="comp.type === 'TextSubheading'">
      <h3 class="text-lg font-semibold" :class="t.heading">{{ comp.text }}</h3>
    </template>
    <template v-else-if="comp.type === 'TextBody'">
      <p class="text-sm whitespace-pre-wrap" :class="t.body">{{ comp.text }}</p>
    </template>
    <template v-else-if="comp.type === 'TextCaption'">
      <p class="text-xs whitespace-pre-wrap" :class="t.muted">{{ comp.text }}</p>
    </template>
    <template v-else-if="comp.type === 'RichText'">
      <div
        class="text-sm whitespace-pre-wrap border-l-2 pl-2"
        :class="[t.body, dark ? 'border-zinc-600' : 'border-zinc-300']"
      >
        {{ comp.text }}
      </div>
    </template>

    <template v-else-if="comp.type === 'TextInput' || comp.type === 'TextArea'">
      <Label class="text-sm font-medium" :class="t.label">
        {{ comp.label }}
        <span v-if="comp.required" class="text-red-500">*</span>
      </Label>
      <Input
        v-if="comp.type === 'TextInput'"
        disabled
        :type="comp['input-type'] === 'password' || comp['input-type'] === 'passcode' ? 'password' : 'text'"
        :placeholder="comp.label"
        class="mt-1.5"
        :class="t.surface"
      />
      <textarea
        v-else
        disabled
        :placeholder="comp.label"
        class="mt-1.5 w-full p-2.5 rounded-lg border text-sm min-h-[80px]"
        :class="[t.surface, t.body]"
        rows="3"
      />
    </template>

    <template v-else-if="comp.type === 'Dropdown'">
      <Label class="text-sm" :class="t.body">
        {{ comp.label }}
        <span v-if="comp.required" class="text-red-500">*</span>
      </Label>
      <div
        class="mt-1 p-2 rounded-md border text-sm flex items-center justify-between"
        :class="[t.surface, t.muted]"
      >
        <span>Select...</span>
        <ChevronDown class="h-4 w-4" />
      </div>
    </template>

    <template v-else-if="comp.type === 'RadioButtonsGroup'">
      <Label class="text-sm mb-2 block font-medium" :class="t.label">
        {{ comp.label }}
        <span v-if="comp.required" class="text-red-500">*</span>
      </Label>
      <div
        :class="playground
          ? ['space-y-0 divide-y rounded-lg border overflow-hidden', t.divide]
          : 'space-y-2'"
      >
        <div
          v-for="opt in comp['data-source']"
          :key="opt.id"
          :class="playground ? ['flex items-center gap-3 px-3 py-2.5', t.optionRow] : 'flex items-start gap-2'"
        >
          <div
            :class="playground
              ? ['h-5 w-5 rounded-full border-2 flex-shrink-0', dark ? 'border-zinc-500' : 'border-zinc-400']
              : ['mt-0.5 h-4 w-4 rounded-full border-2 flex-shrink-0', dark ? 'border-zinc-500' : 'border-zinc-400']"
          />
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <img v-if="opt.image" :src="imagePreviewSrc(opt.image)" class="h-6 w-6 rounded object-cover" alt="" />
              <span class="text-sm" :class="t.heading">{{ opt.title }}</span>
            </div>
            <p v-if="opt.description" class="text-xs" :class="t.muted">{{ opt.description }}</p>
          </div>
        </div>
      </div>
    </template>

    <template v-else-if="comp.type === 'CheckboxGroup'">
      <Label class="text-sm mb-2 block font-medium" :class="t.label">
        {{ comp.label }}
        <span v-if="comp.required" class="text-red-500">*</span>
      </Label>
      <div
        :class="playground
          ? ['space-y-0 divide-y rounded-lg border overflow-hidden', t.divide]
          : 'space-y-2'"
      >
        <div
          v-for="opt in comp['data-source']"
          :key="opt.id"
          :class="playground ? ['flex items-center gap-3 px-3 py-2.5', t.optionRow] : 'flex items-start gap-2'"
        >
          <div
            :class="playground
              ? ['h-5 w-5 rounded border-2 flex-shrink-0', dark ? 'border-zinc-500' : 'border-zinc-400']
              : ['mt-0.5 h-4 w-4 rounded border flex-shrink-0', dark ? 'border-zinc-500' : 'border-zinc-400']"
          />
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <img v-if="opt.image" :src="imagePreviewSrc(opt.image)" class="h-6 w-6 rounded object-cover" alt="" />
              <span class="text-sm" :class="t.heading">{{ opt.title }}</span>
            </div>
            <p v-if="opt.description" class="text-xs" :class="t.muted">{{ opt.description }}</p>
          </div>
        </div>
      </div>
    </template>

    <template v-else-if="comp.type === 'OptIn'">
      <div class="flex items-start gap-2">
        <div
          class="mt-0.5 h-4 w-4 rounded border flex-shrink-0"
          :class="dark ? 'border-zinc-500' : 'border-zinc-400'"
        />
        <div class="text-sm" :class="t.body">
          <span>{{ comp.label }}</span>
          <span v-if="comp.required" class="text-red-500">*</span>
          <span v-if="comp['on-click-action']" class="block text-xs underline mt-1" :class="t.link">Read more</span>
        </div>
      </div>
    </template>

    <template v-else-if="comp.type === 'DatePicker'">
      <Label class="text-sm" :class="t.body">
        {{ comp.label }}
        <span v-if="comp.required" class="text-red-500">*</span>
      </Label>
      <div
        class="mt-1 p-2 rounded-md border text-sm flex items-center justify-between"
        :class="[t.surface, t.muted]"
      >
        <span>Select date...</span>
        <Calendar class="h-4 w-4" />
      </div>
      <p v-if="comp['helper-text']" class="text-xs mt-1" :class="t.muted">{{ comp['helper-text'] }}</p>
    </template>

    <template v-else-if="comp.type === 'CalendarPicker'">
      <Label class="text-sm" :class="t.body">
        {{ comp.label }}
        <span v-if="comp.required" class="text-red-500">*</span>
      </Label>
      <div
        class="mt-1 p-2 rounded-md border text-sm flex items-center justify-between"
        :class="[t.surface, t.muted]"
      >
        <span>{{ comp.mode === 'range' ? 'Select date range...' : 'Select date...' }}</span>
        <CalendarRange class="h-4 w-4" />
      </div>
    </template>

    <template v-else-if="comp.type === 'PhotoPicker'">
      <Label class="text-sm" :class="t.body">{{ comp.label }}</Label>
      <p v-if="comp.description" class="text-xs mt-0.5" :class="t.muted">{{ comp.description }}</p>
      <div
        class="mt-2 border border-dashed rounded-md p-4 flex flex-col items-center gap-1"
        :class="t.dashed"
      >
        <Camera class="h-6 w-6" />
        <span class="text-xs">Photo upload</span>
      </div>
    </template>

    <template v-else-if="comp.type === 'DocumentPicker'">
      <Label class="text-sm" :class="t.body">{{ comp.label }}</Label>
      <p v-if="comp.description" class="text-xs mt-0.5" :class="t.muted">{{ comp.description }}</p>
      <div
        class="mt-2 border border-dashed rounded-md p-4 flex flex-col items-center gap-1"
        :class="t.dashed"
      >
        <FileUp class="h-6 w-6" />
        <span class="text-xs">Document upload</span>
      </div>
    </template>

    <template v-else-if="comp.type === 'If'">
      <div class="space-y-2 text-xs" :class="t.body">
        <div class="flex items-center gap-1.5 font-medium">
          <GitBranch class="h-3.5 w-3.5" />
          If
        </div>
        <code class="block text-[10px] px-1.5 py-1 rounded break-all" :class="t.code">{{ comp.condition }}</code>
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
        <code class="block text-[10px] px-1.5 py-1 rounded break-all" :class="t.code">{{ comp.value }}</code>
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
      <span class="text-sm underline" :class="t.link">{{ comp.text }}</span>
    </template>

    <template v-else-if="comp.type === 'Footer'">
      <button
        type="button"
        class="w-full h-11 rounded-full text-sm font-semibold text-white bg-[#00a884] hover:bg-[#008f72] transition-colors"
      >
        {{ comp.label }}
      </button>
    </template>

    <template v-else>
      <div class="flex items-center gap-2 text-sm" :class="t.muted">
        <Type class="h-4 w-4" />
        {{ labelForType(comp.type) }}
      </div>
    </template>
  </div>
</template>
