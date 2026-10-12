<script setup lang="ts">
import { computed } from 'vue'
import { Zap } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: Record<string, any> }>()

const summary = computed(() => {
  const c = props.data?.config || {}
  return c.url ? `${c.method || 'POST'} ${c.url}` : 'Fire-and-forget webhook'
})

const outputHandles = [
  { id: 'default', label: 'next' },
]
</script>

<template>
  <BaseNode :label="data?.label || 'Webhook'" header-class="bg-orange-800" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><Zap class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
