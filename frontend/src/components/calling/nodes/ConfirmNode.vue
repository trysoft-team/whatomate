<script setup lang="ts">
import { computed } from 'vue'
import { CheckCircle2 } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: Record<string, any> }>()

const summary = computed(() => {
  const c = props.data?.config || {}
  return c.variable ? `Confirm {{${c.variable}}}` : 'Confirm input'
})

const outputHandles = [
  { id: 'confirmed', label: 'OK', title: 'Caller confirmed' },
  { id: 'retry', label: 'Retry', title: 'Caller wants to re-enter' },
  { id: 'timeout', label: 'T/O', title: 'Timed out' },
  { id: 'max_retries', label: 'Max', title: 'Max retries exceeded' },
]
</script>

<template>
  <BaseNode :label="data?.label || 'Confirm'" header-class="bg-emerald-600" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><CheckCircle2 class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
