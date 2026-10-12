<script setup lang="ts">
import { computed } from 'vue'
import { BookUser } from 'lucide-vue-next'
import BaseNode from '@/components/calling/nodes/BaseNode.vue'

defineOptions({ inheritAttrs: false })
const props = defineProps<{ data: any }>()
const summary = computed(() => {
  const action = props.data?.config?.action || 'add'
  return `${action}`
})
</script>

<template>
  <BaseNode :label="data?.label || 'Phonebook'" header-class="bg-lime-700" :has-input="!data?.isEntryNode">
    <template #icon><BookUser class="w-4 h-4" /></template>
    <p>{{ summary }}</p>
  </BaseNode>
</template>
