<script setup lang="ts">
import { computed } from 'vue'
import { PhoneForwarded } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })
const props = defineProps<{ data: Record<string, any> }>()
const summary = computed(() => {
  const c = props.data?.config || {}
  return c.phone || 'Dial out'
})
const outputHandles = [
  { id: 'completed', label: 'Done' },
  { id: 'no_answer', label: 'No ans' },
  { id: 'error', label: 'Err' },
]
</script>
<template>
  <BaseNode :label="data?.label || 'Dial Out'" header-class="bg-amber-800" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><PhoneForwarded class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
