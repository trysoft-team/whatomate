<script setup lang="ts">
import { computed } from 'vue'
import { Mic } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })
const props = defineProps<{ data: Record<string, any> }>()
const summary = computed(() => {
  const c = props.data?.config || {}
  return `${c.max_seconds || 60}s → ${c.store_as || 'voicemail_key'}`
})
const outputHandles = [
  { id: 'default', label: 'next' },
  { id: 'timeout', label: 'T/O' },
  { id: 'error', label: 'Err' },
]
</script>
<template>
  <BaseNode :label="data?.label || 'Voicemail'" header-class="bg-rose-700" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><Mic class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
