<script setup lang="ts">
import { computed } from 'vue'
import { Timer } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: Record<string, any> }>()

const summary = computed(() => {
  const c = props.data?.config || {}
  const secs = c.seconds ?? 3
  const mode = c.mode || 'silence'
  return `${secs}s ${mode}`
})

const outputHandles = [
  { id: 'default', label: 'next' },
]
</script>

<template>
  <BaseNode :label="data?.label || 'Delay'" header-class="bg-sky-600" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><Timer class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
