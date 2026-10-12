<script setup lang="ts">
import { computed } from 'vue'
import { Variable } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: Record<string, any> }>()

const summary = computed(() => {
  const c = props.data?.config || {}
  const assignments = Array.isArray(c.assignments) ? c.assignments : []
  if (c.name) return `${c.name} = ${c.value ?? ''}`
  if (assignments.length === 1) return `${assignments[0]?.name || '?'} = ${assignments[0]?.value ?? ''}`
  if (assignments.length > 1) return `${assignments.length} variables`
  return 'Set variables'
})

const outputHandles = [
  { id: 'default', label: 'next' },
]
</script>

<template>
  <BaseNode :label="data?.label || 'Set Variable'" header-class="bg-fuchsia-600" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><Variable class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
