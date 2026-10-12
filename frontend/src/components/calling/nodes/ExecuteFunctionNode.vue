<script setup lang="ts">
import { computed } from 'vue'
import { Code2 } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })
const props = defineProps<{ data: Record<string, any> }>()
const summary = computed(() => {
  const c = props.data?.config || {}
  return c.name || c.url || 'HTTP function'
})
const outputHandles = [
  { id: 'default', label: 'OK' },
  { id: 'error', label: 'Err' },
]
</script>
<template>
  <BaseNode :label="data?.label || 'Execute Function'" header-class="bg-orange-700" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><Code2 class="w-4 h-4" /></template>
    <p class="truncate font-mono text-[10px]">{{ summary }}</p>
  </BaseNode>
</template>
