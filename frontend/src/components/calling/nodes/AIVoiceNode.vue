<script setup lang="ts">
import { computed } from 'vue'
import { Bot } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })
const props = defineProps<{ data: Record<string, any> }>()
const summary = computed(() => {
  const c = props.data?.config || {}
  return c.store_as ? `→ ${c.store_as}` : 'AI voice turn'
})
const outputHandles = [
  { id: 'default', label: 'next' },
  { id: 'error', label: 'Err' },
]
</script>
<template>
  <BaseNode :label="data?.label || 'AI Voice'" header-class="bg-emerald-800" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><Bot class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
