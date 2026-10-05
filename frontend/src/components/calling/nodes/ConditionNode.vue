<script setup lang="ts">
import { computed } from 'vue'
import { GitBranch } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: Record<string, any> }>()

const summary = computed(() => {
  const c = props.data?.config || {}
  const field = c.field || c.variable || '?'
  const op = c.operator || 'equals'
  const value = c.value ?? ''
  return `${field} ${op} ${value}`.trim()
})

const outputHandles = [
  { id: 'true', label: 'True', title: 'Condition matched' },
  { id: 'false', label: 'False', title: 'Condition did not match' },
]
</script>

<template>
  <BaseNode :label="data?.label || 'Condition'" header-class="bg-indigo-600" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><GitBranch class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
