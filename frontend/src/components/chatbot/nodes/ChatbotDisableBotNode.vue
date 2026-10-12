<script setup lang="ts">
import { computed } from 'vue'
import { Ban } from 'lucide-vue-next'
import BaseNode from '@/components/calling/nodes/BaseNode.vue'

defineOptions({ inheritAttrs: false })
const props = defineProps<{ data: any }>()
const summary = computed(() => {
  const h = Number(props.data?.config?.hours || 0)
  const m = Number(props.data?.config?.minutes || 0)
  return `Mute ${h}h ${m}m`
})
</script>

<template>
  <BaseNode :label="data?.label || 'Disable Bot'" header-class="bg-stone-600" :has-input="!data?.isEntryNode">
    <template #icon><Ban class="w-4 h-4" /></template>
    <p>{{ summary }}</p>
  </BaseNode>
</template>
