<script setup lang="ts">
import { computed } from 'vue'
import { Tag } from 'lucide-vue-next'
import BaseNode from '@/components/calling/nodes/BaseNode.vue'

defineOptions({ inheritAttrs: false })
const props = defineProps<{ data: any }>()
const summary = computed(() => {
  const add = props.data?.config?.add || []
  const remove = props.data?.config?.remove || []
  return `+${add.length} / -${remove.length}`
})
</script>

<template>
  <BaseNode :label="data?.label || 'Labels'" header-class="bg-pink-600" :has-input="!data?.isEntryNode">
    <template #icon><Tag class="w-4 h-4" /></template>
    <p>{{ summary }}</p>
  </BaseNode>
</template>
