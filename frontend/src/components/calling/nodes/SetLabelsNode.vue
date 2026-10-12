<script setup lang="ts">
import { computed } from 'vue'
import { Tag } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: Record<string, any> }>()

const summary = computed(() => {
  const c = props.data?.config || {}
  const add = Array.isArray(c.add) ? c.add.length : 0
  const remove = Array.isArray(c.remove) ? c.remove.length : 0
  return `+${add} / -${remove} labels`
})

const outputHandles = [
  { id: 'default', label: 'next' },
  { id: 'error', label: 'Err', title: 'Failed to update contact' },
]
</script>

<template>
  <BaseNode :label="data?.label || 'Set Labels'" header-class="bg-pink-600" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><Tag class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
