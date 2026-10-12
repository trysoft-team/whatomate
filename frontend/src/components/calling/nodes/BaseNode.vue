<script setup lang="ts">
import { computed } from 'vue'
import { Handle, Position } from '@vue-flow/core'

const props = withDefaults(
  defineProps<{
    label: string
    headerClass: string
    hasInput?: boolean
    outputHandles?: { id: string; label: string; title?: string }[]
  }>(),
  { hasInput: true },
)

const gradientMap: Record<string, string> = {
  'bg-blue-600': 'from-blue-600 to-blue-500',
  'bg-purple-600': 'from-purple-600 to-purple-500',
  'bg-orange-600': 'from-orange-600 to-amber-500',
  'bg-green-600': 'from-green-600 to-emerald-500',
  'bg-amber-600': 'from-amber-600 to-yellow-500',
  'bg-red-600': 'from-red-600 to-rose-500',
  'bg-cyan-600': 'from-cyan-600 to-cyan-500',
  'bg-teal-600': 'from-teal-600 to-teal-500',
  'bg-violet-700': 'from-violet-700 to-violet-500',
  'bg-orange-700': 'from-orange-700 to-amber-600',
  'bg-emerald-800': 'from-emerald-800 to-emerald-600',
}

const headerGradient = computed(() => gradientMap[props.headerClass] || props.headerClass)

const handleCount = computed(() => props.outputHandles?.length ?? 0)

/** Widen the node when many bottom ports so handles don't crowd. */
const nodeMinWidthClass = computed(() => {
  const n = handleCount.value
  if (n >= 6) return 'min-w-[17rem]'
  if (n >= 5) return 'min-w-[15rem]'
  if (n >= 4) return 'min-w-[13rem]'
  return 'min-w-48'
})

function handleLeftPercent(idx: number): string {
  const n = handleCount.value
  if (n <= 1) return '50%'
  return `${((idx + 1) / (n + 1)) * 100}%`
}
</script>

<template>
  <div
    :class="[
      'base-node relative bg-background border rounded-lg shadow-md hover:shadow-lg w-max max-w-sm overflow-visible transition-shadow duration-200',
      nodeMinWidthClass,
    ]"
  >
    <!-- Input handle (top) -->
    <Handle
      v-if="hasInput !== false"
      id="input"
      type="target"
      :position="Position.Top"
      class="!w-3.5 !h-3.5 !rounded-full !bg-slate-400 !border-2 !border-background hover:!bg-slate-300 !transition-colors"
      style="z-index: 10;"
    />

    <!-- Header -->
    <div :class="['px-3 py-2 rounded-t-lg text-white text-xs font-semibold flex items-center gap-2 overflow-hidden bg-gradient-to-r', headerGradient]">
      <slot name="icon" />
      <span class="truncate">{{ label }}</span>
    </div>

    <!-- Body -->
    <div class="px-3 py-2.5 text-xs text-muted-foreground">
      <slot />
    </div>

    <!-- Multi-output: labels sit above ports so animated edges don't cross them -->
    <template v-if="outputHandles && outputHandles.length > 0">
      <div class="relative border-t border-border/50 h-7">
        <span
          v-for="(handle, idx) in outputHandles"
          :key="'lbl-' + handle.id"
          class="absolute top-1 -translate-x-1/2 inline-flex items-center justify-center h-4 min-w-[1rem] px-1 rounded text-[9px] font-semibold tabular-nums text-muted-foreground bg-muted/90 leading-none pointer-events-none select-none z-[5]"
          :title="handle.title || handle.label || handle.id"
          :style="{ left: handleLeftPercent(idx) }"
        >{{ idx + 1 }}</span>
        <Handle
          v-for="(handle, idx) in outputHandles"
          :key="handle.id"
          type="source"
          :id="handle.id"
          :position="Position.Bottom"
          :title="handle.title || handle.label"
          :style="{
            left: handleLeftPercent(idx),
            zIndex: 10,
          }"
          class="!w-3.5 !h-3.5 !rounded-full !bg-primary !border-2 !border-background hover:!bg-primary/80 !transition-colors"
        />
      </div>
    </template>
    <template v-else-if="!outputHandles">
      <Handle
        id="default"
        type="source"
        :position="Position.Bottom"
        class="!w-3.5 !h-3.5 !rounded-full !bg-primary !border-2 !border-background hover:!bg-primary/80 !transition-colors"
        style="z-index: 10;"
      />
    </template>
  </div>
</template>
