<script setup lang="ts">
import { computed } from 'vue'
import { Sheet } from 'lucide-vue-next'
import BaseNode from '@/components/calling/nodes/BaseNode.vue'

defineOptions({ inheritAttrs: false })
const props = defineProps<{ data: any }>()
const summary = computed(() => props.data?.config?.action || 'sheets_append')
</script>

<template>
  <BaseNode :label="data?.label || 'Google'" header-class="bg-yellow-600" :has-input="!data?.isEntryNode">
    <template #icon><Sheet class="w-4 h-4" /></template>
    <p>{{ summary }}</p>
  </BaseNode>
</template>
