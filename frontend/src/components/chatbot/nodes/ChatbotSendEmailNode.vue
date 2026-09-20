<script setup lang="ts">
import { computed } from 'vue'
import { Mail } from 'lucide-vue-next'
import BaseNode from '@/components/calling/nodes/BaseNode.vue'

defineOptions({ inheritAttrs: false })
const props = defineProps<{ data: any }>()
const summary = computed(() => props.data?.config?.subject || props.data?.config?.to || 'Email')
</script>

<template>
  <BaseNode :label="data?.label || 'Email'" header-class="bg-red-600" :has-input="!data?.isEntryNode">
    <template #icon><Mail class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
