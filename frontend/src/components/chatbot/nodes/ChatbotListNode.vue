<script setup lang="ts">
import { computed } from 'vue'
import { List } from 'lucide-vue-next'
import BaseNode from '@/components/calling/nodes/BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: any }>()

const sections = computed(() => props.data?.config?.sections || [])

const rows = computed(() => {
  const out: { id: string; title: string }[] = []
  for (const sec of sections.value) {
    for (const row of sec.rows || []) {
      if (row?.id) out.push({ id: row.id, title: row.title || row.id })
    }
  }
  return out
})

const outputHandles = computed(() => {
  const handles = rows.value.map((r) => ({
    id: `button:${r.id}`,
    label: r.title || '—',
    title: r.title || '—',
  }))
  handles.push({ id: 'unmatched', label: 'Unmatched', title: 'Free text / unknown' })
  return handles
})
</script>

<template>
  <BaseNode :label="data?.label || 'List'" header-class="bg-violet-600" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><List class="w-4 h-4" /></template>
    <p class="truncate text-[11px] text-muted-foreground mb-0.5" :title="data?.config?.body || ''">
      {{ data?.config?.body || 'No body' }}
    </p>
    <div v-if="rows.length > 0" class="space-y-0.5">
      <div v-for="(row, idx) in rows.slice(0, 4)" :key="row.id" class="flex gap-1 truncate" :title="row.title">
        <span class="font-mono font-bold">{{ Number(idx) + 1 }}:</span>
        <span class="truncate">{{ row.title || '—' }}</span>
      </div>
      <p v-if="rows.length > 4" class="text-[10px] text-muted-foreground">+{{ rows.length - 4 }} more</p>
    </div>
    <p v-else class="text-muted-foreground italic">No rows</p>
  </BaseNode>
</template>
