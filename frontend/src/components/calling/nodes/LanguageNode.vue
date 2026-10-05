<script setup lang="ts">
import { computed } from 'vue'
import { Languages } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: Record<string, any> }>()

const summary = computed(() => {
  const c = props.data?.config || {}
  return c.locale || c.language || 'Set locale'
})

const outputHandles = [
  { id: 'default', label: 'next' },
]
</script>

<template>
  <BaseNode :label="data?.label || 'Language'" header-class="bg-violet-600" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><Languages class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
