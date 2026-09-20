<script setup lang="ts">
import { RotateCcw } from 'lucide-vue-next'
import BaseNode from '@/components/calling/nodes/BaseNode.vue'

defineOptions({ inheritAttrs: false })

defineProps<{ data: any }>()
</script>

<template>
  <BaseNode :label="data?.label || 'Reset'" header-class="bg-orange-700" :has-input="!data?.isEntryNode">
    <template #icon><RotateCcw class="w-4 h-4" /></template>
    <p class="text-[11px] text-muted-foreground">Clear variables & continue</p>
  </BaseNode>
</template>
