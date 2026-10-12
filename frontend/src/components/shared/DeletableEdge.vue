<script setup lang="ts">
import { computed } from 'vue'
import {
  BaseEdge,
  EdgeLabelRenderer,
  getBezierPath,
  getSmoothStepPath,
  useVueFlow,
  type Position,
} from '@vue-flow/core'
import { X } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  id: string
  type?: string
  sourceX: number
  sourceY: number
  targetX: number
  targetY: number
  sourcePosition: Position
  targetPosition: Position
  markerEnd?: string
  style?: Record<string, unknown>
  label?: string | number
  labelStyle?: Record<string, unknown>
  selected?: boolean
  animated?: boolean
}>()

const { t } = useI18n()
const { removeEdges } = useVueFlow()

const path = computed(() => {
  const params = {
    sourceX: props.sourceX,
    sourceY: props.sourceY,
    targetX: props.targetX,
    targetY: props.targetY,
    sourcePosition: props.sourcePosition,
    targetPosition: props.targetPosition,
  }
  return props.type === 'smoothstep' ? getSmoothStepPath(params) : getBezierPath(params)
})

const labelText = computed(() => {
  if (props.label === undefined || props.label === null) return ''
  const text = String(props.label).trim()
  if (!text || text.toLowerCase() === 'default') return ''
  return text
})

const extraTransform = computed(() => {
  const transform = props.labelStyle?.transform
  return typeof transform === 'string' ? transform : ''
})

function onRemove(event: MouseEvent) {
  event.preventDefault()
  event.stopPropagation()
  removeEdges(props.id)
}
</script>

<template>
  <BaseEdge
    :id="id"
    :path="path[0]"
    :marker-end="markerEnd"
    :style="style"
    :animated="animated"
    :interaction-width="20"
  />
  <EdgeLabelRenderer>
    <div
      class="nodrag nopan pointer-events-auto absolute z-10 flex flex-col items-center gap-0.5"
      :style="{
        transform: `translate(-50%, -50%) translate(${path[1]}px, ${path[2]}px) ${extraTransform}`,
      }"
    >
      <span
        v-if="labelText"
        class="max-w-[120px] truncate rounded bg-background/90 px-1 text-[10px] font-medium leading-4 text-muted-foreground"
      >{{ labelText }}</span>
      <button
        type="button"
        class="flex h-5 w-5 items-center justify-center rounded-full border border-border bg-background text-muted-foreground shadow-sm transition-colors hover:border-destructive hover:bg-destructive hover:text-destructive-foreground"
        :class="{ 'border-primary text-primary': selected }"
        :aria-label="t('common.remove')"
        :title="t('common.remove')"
        @pointerdown.stop
        @mousedown.stop
        @click="onRemove"
      >
        <X class="h-3 w-3" />
      </button>
    </div>
  </EdgeLabelRenderer>
</template>
