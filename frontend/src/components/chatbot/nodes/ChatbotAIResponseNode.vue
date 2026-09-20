<script setup lang="ts">
import { computed } from 'vue'
import { Bot } from 'lucide-vue-next'
import BaseNode from '@/components/calling/nodes/BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: any }>()
const summary = computed(() => {
  const p = props.data?.config?.prompt_template || props.data?.config?.prompt || ''
  if (!p) return 'Org AI settings'
  return p.length > 50 ? p.slice(0, 50) + '…' : p
})
</script>

<template>
  <BaseNode :label="data?.label || 'AI Response'" header-class="bg-emerald-700" :has-input="!data?.isEntryNode">
    <template #icon><Bot class="w-4 h-4" /></template>
    <p class="truncate" :title="summary">{{ summary }}</p>
  </BaseNode>
</template>
