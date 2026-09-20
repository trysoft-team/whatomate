<script setup lang="ts">
import { computed } from 'vue'
import { Timer } from 'lucide-vue-next'
import BaseNode from '@/components/calling/nodes/BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: any }>()
const seconds = computed(() => Number(props.data?.config?.seconds ?? 0))
</script>

<template>
  <BaseNode :label="data?.label || 'Delay'" header-class="bg-sky-600" :has-input="!data?.isEntryNode">
    <template #icon><Timer class="w-4 h-4" /></template>
    <p>{{ seconds > 0 ? `${seconds}s` : 'No delay' }}</p>
  </BaseNode>
</template>
