<script setup lang="ts">
import { computed } from 'vue'
import { MessageSquare } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: Record<string, any> }>()

const summary = computed(() => {
  const c = props.data?.config || {}
  const to = (c.to || c.phone || '').trim()
  const body = (c.body || '').trim()
  const dest = to || 'caller'
  if (body) return `→ ${dest} · ${body.slice(0, 28)}`
  return `→ ${dest}`
})

const outputHandles = [
  { id: 'default', label: 'next' },
  { id: 'error', label: 'Err', title: 'Send failed' },
]
</script>

<template>
  <BaseNode :label="data?.label || 'Send Message'" header-class="bg-green-700" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><MessageSquare class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
