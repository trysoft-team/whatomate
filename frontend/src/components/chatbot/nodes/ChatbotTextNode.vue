<script setup lang="ts">
import { computed } from 'vue'
import { MessageSquare, Image, MapPin } from 'lucide-vue-next'
import BaseNode from '@/components/calling/nodes/BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: any }>()

const mediaType = computed(() => (props.data?.config?.media_type as string) || 'text')

const message = computed(() => {
  const cfg = props.data?.config || {}
  if (mediaType.value === 'location') {
    const name = cfg.name || cfg.location_name || ''
    return name || `${cfg.latitude ?? '?'}, ${cfg.longitude ?? '?'}`
  }
  if (mediaType.value !== 'text') {
    const cap = cfg.caption || cfg.media_url || cfg.link || ''
    return cap.length > 60 ? cap.slice(0, 60) + '...' : cap || mediaType.value
  }
  const msg = cfg.message || cfg.body || ''
  return msg.length > 60 ? msg.slice(0, 60) + '...' : msg || 'No message'
})

const inputType = computed(() => props.data?.config?.input_type)
const waitForReply = computed(() => !!props.data?.config?.wait_for_reply)
</script>

<template>
  <BaseNode :label="data?.label || 'Text'" header-class="bg-blue-600" :has-input="!data?.isEntryNode">
    <template #icon>
      <MapPin v-if="mediaType === 'location'" class="w-4 h-4" />
      <Image v-else-if="mediaType !== 'text'" class="w-4 h-4" />
      <MessageSquare v-else class="w-4 h-4" />
    </template>
    <p class="truncate" :title="message">{{ message }}</p>
    <div class="flex flex-wrap gap-1 mt-1">
      <span v-if="mediaType !== 'text'" class="inline-block px-1.5 py-0.5 bg-blue-100 dark:bg-blue-900/40 text-blue-700 dark:text-blue-300 rounded text-[10px] font-medium">
        {{ mediaType }}
      </span>
      <span v-if="inputType && inputType !== 'none'" class="inline-block px-1.5 py-0.5 bg-blue-100 dark:bg-blue-900/40 text-blue-700 dark:text-blue-300 rounded text-[10px] font-medium">
        {{ inputType }}
      </span>
      <span v-if="waitForReply" class="inline-block px-1.5 py-0.5 bg-amber-100 dark:bg-amber-900/40 text-amber-700 dark:text-amber-300 rounded text-[10px] font-medium">
        wait
      </span>
    </div>
  </BaseNode>
</template>
