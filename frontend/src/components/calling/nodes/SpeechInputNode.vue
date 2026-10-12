<script setup lang="ts">
import { computed } from 'vue'
import { AudioLines } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })
const props = defineProps<{ data: Record<string, any> }>()
const summary = computed(() => {
  const c = props.data?.config || {}
  return `STT → ${c.store_as || 'speech_text'}`
})
const outputHandles = [
  { id: 'default', label: 'next' },
  { id: 'timeout', label: 'T/O' },
  { id: 'error', label: 'Err' },
]
</script>
<template>
  <BaseNode :label="data?.label || 'Speech'" header-class="bg-blue-800" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><AudioLines class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
