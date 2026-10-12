<script setup lang="ts">
import { computed } from 'vue'
import { FileText } from 'lucide-vue-next'
import BaseNode from '@/components/calling/nodes/BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: any }>()

const summary = computed(() => {
  const cfg = props.data?.config || {}
  return cfg.template_name || cfg.name || 'Pick a template'
})
</script>

<template>
  <BaseNode :label="data?.label || 'WA Template'" header-class="bg-rose-600" :has-input="!data?.isEntryNode">
    <template #icon><FileText class="w-4 h-4" /></template>
    <p class="truncate" :title="summary">{{ summary }}</p>
  </BaseNode>
</template>
