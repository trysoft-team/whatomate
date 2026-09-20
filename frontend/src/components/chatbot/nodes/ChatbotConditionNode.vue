<script setup lang="ts">
import { computed } from 'vue'
import { GitBranch } from 'lucide-vue-next'
import BaseNode from '@/components/calling/nodes/BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: any }>()

const isRulesMode = computed(() => {
  const cfg = props.data?.config || {}
  if (cfg.mode === 'rules') return true
  if (cfg.mode === 'expression') return false
  return Array.isArray(cfg.rules) && cfg.rules.length > 0
})

const summary = computed(() => {
  const cfg = props.data?.config || {}
  if (isRulesMode.value) {
    const n = (cfg.rules || []).length
    return n ? `${n} rule${n === 1 ? '' : 's'}` : 'No rules'
  }
  const expression = (cfg.expression || cfg.input_config?.expression) as string | undefined
  if (!expression) return 'No expression'
  return expression.length > 60 ? expression.slice(0, 60) + '…' : expression
})

const outputHandles = computed(() => {
  if (!isRulesMode.value) {
    return [
      { id: 'true', label: 'True' },
      { id: 'false', label: 'False' },
    ]
  }
  const rules = props.data?.config?.rules || []
  const handles = rules.map((r: any, i: number) => {
    const id = r.handle || slug(r.name) || `rule_${i}`
    return { id, label: r.name || id, title: r.name || id }
  })
  handles.push({ id: 'else', label: 'Else', title: 'Else' })
  return handles
})

function slug(name: string) {
  return String(name || '')
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_|_$/g, '')
}
</script>

<template>
  <BaseNode
    :label="data?.label || 'Condition'"
    header-class="bg-indigo-600"
    :output-handles="outputHandles"
    :has-input="!data?.isEntryNode"
  >
    <template #icon><GitBranch class="w-4 h-4" /></template>
    <p class="truncate" :title="summary">{{ summary }}</p>
  </BaseNode>
</template>
