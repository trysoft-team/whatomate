<script setup lang="ts">
import { computed } from 'vue'
import { Variable } from 'lucide-vue-next'
import BaseNode from '@/components/calling/nodes/BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: any }>()

const summary = computed(() => {
  const cfg = props.data?.config || {}
  const setKeys = Object.keys(cfg.set || {})
  const paths = cfg.paths || []
  const n = setKeys.length + paths.length
  if (!n) return 'No variables'
  return `${n} assignment${n === 1 ? '' : 's'}`
})
</script>

<template>
  <BaseNode :label="data?.label || 'Set Variable'" header-class="bg-fuchsia-600" :has-input="!data?.isEntryNode">
    <template #icon><Variable class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
