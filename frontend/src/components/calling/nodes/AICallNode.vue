<script setup lang="ts">
import { computed } from 'vue'
import { Sparkles } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })
const props = defineProps<{ data: Record<string, any> }>()

const GEMINI_VOICE_GENDER: Record<string, string> = {
  Puck: 'Male', Kore: 'Female', Aoede: 'Female', Charon: 'Male', Fenrir: 'Male',
  Leda: 'Female', Orus: 'Male', Zephyr: 'Female', Autonoe: 'Female', Enceladus: 'Male',
  Iapetus: 'Male', Umbriel: 'Male', Algieba: 'Male', Despina: 'Female', Erinome: 'Female',
  Algenib: 'Male', Rasalgethi: 'Male', Laomedeia: 'Female', Achernar: 'Female', Alnilam: 'Male',
  Schedar: 'Male', Gacrux: 'Female', Pulcherrima: 'Female', Achird: 'Male',
  Zubenelgenubi: 'Male', Vindemiatrix: 'Female', Sadachbia: 'Male', Sadaltager: 'Male',
  Sulafat: 'Female', Callirrhoe: 'Female',
}

const summary = computed(() => {
  const c = props.data?.config || {}
  const tools = Array.isArray(c.tools) ? c.tools.length : 0
  const voice = c.voice_id || c.openai_voice || 'Puck'
  const gender = GEMINI_VOICE_GENDER[voice] || ''
  const voiceLabel = gender ? `${voice} · ${gender}` : voice
  return tools > 0 ? `Live · ${voiceLabel} · ${tools} tools` : `Live · ${voiceLabel}`
})

/** Session outcomes + one handle per function (fn:<name>) for linked subroutines. */
const outputHandles = computed(() => {
  const handles: { id: string; label: string; title?: string }[] = [
    { id: 'completed', label: 'Done', title: 'AI Call finished' },
    { id: 'transfer', label: 'Xfer', title: 'Transfer requested' },
    { id: 'max_turns', label: 'Max', title: 'Max turns reached' },
    { id: 'error', label: 'Err', title: 'Error' },
  ]
  const tools = Array.isArray(props.data?.config?.tools) ? props.data.config.tools : []
  for (const t of tools) {
    const name = String(t?.name || '').trim()
    if (!name) continue
    // Only Nodes handler exposes a canvas fn: edge (Return to call does not).
    if (String(t?.handler || '') !== 'nodes') continue
    handles.push({
      id: `fn:${name}`,
      label: name.length > 8 ? name.slice(0, 7) + '…' : name,
      title: `Function ${name} → linked nodes (runs mid-call, then resumes AI Call)`,
    })
  }
  return handles
})
</script>
<template>
  <BaseNode :label="data?.label || 'AI Call'" header-class="bg-violet-700" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><Sparkles class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
