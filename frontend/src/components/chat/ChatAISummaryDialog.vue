<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { chatAIService, type ChatAISummary } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { Loader2, Copy, RotateCw } from 'lucide-vue-next'

const props = defineProps<{
  open: boolean
  contactId: string
}>()

const emit = defineEmits<{
  (e: 'update:open', value: boolean): void
}>()

const { t } = useI18n()
const loading = ref(false)
const summary = ref<ChatAISummary | null>(null)
const error = ref('')
let loadedFor = ''

const sentimentClass = computed(() => {
  switch (summary.value?.sentiment) {
    case 'positive': return 'bg-emerald-500/15 text-emerald-500'
    case 'frustrated': return 'bg-amber-500/15 text-amber-500'
    case 'angry': return 'bg-red-500/15 text-red-500'
    default: return 'bg-muted text-muted-foreground'
  }
})

watch(() => props.open, (open) => {
  if (open && loadedFor !== props.contactId) load()
})

watch(() => props.contactId, () => {
  summary.value = null
  loadedFor = ''
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    const res = await chatAIService.assist<ChatAISummary>(props.contactId, { mode: 'summarize' })
    summary.value = res.data.data
    loadedFor = props.contactId
  } catch (e) {
    error.value = getErrorMessage(e, t('chat.ai.failed'))
  } finally {
    loading.value = false
  }
}

function asText(): string {
  const s = summary.value
  if (!s) return ''
  const parts = [s.summary]
  if (s.key_points.length) parts.push(`${t('chat.ai.keyPoints')}:\n` + s.key_points.map(p => `• ${p}`).join('\n'))
  if (s.open_questions.length) parts.push(`${t('chat.ai.openQuestions')}:\n` + s.open_questions.map(p => `• ${p}`).join('\n'))
  return parts.join('\n\n')
}

async function copy() {
  try {
    await navigator.clipboard.writeText(asText())
    toast.success(t('chat.ai.copied'))
  } catch {
    toast.error(t('chat.ai.copyFailed'))
  }
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="max-w-lg">
      <DialogHeader>
        <DialogTitle class="flex items-center gap-2">
          {{ $t('chat.ai.summaryTitle') }}
          <Badge v-if="summary" :class="['text-[10px] capitalize', sentimentClass]">
            {{ $t(`chat.ai.sentiment.${summary.sentiment}`) }}
          </Badge>
        </DialogTitle>
        <DialogDescription>{{ $t('chat.ai.summaryDesc') }}</DialogDescription>
      </DialogHeader>

      <div v-if="loading" class="flex items-center gap-2 py-8 justify-center text-sm text-muted-foreground">
        <Loader2 class="h-4 w-4 animate-spin" />
        {{ $t('chat.ai.summarizing') }}
      </div>
      <p v-else-if="error" class="text-sm text-destructive py-4">{{ error }}</p>
      <div v-else-if="summary" class="space-y-4 text-sm">
        <p class="leading-relaxed">{{ summary.summary }}</p>
        <div v-if="summary.key_points.length">
          <p class="text-xs font-medium uppercase tracking-wide text-muted-foreground mb-1.5">{{ $t('chat.ai.keyPoints') }}</p>
          <ul class="list-disc pl-5 space-y-1">
            <li v-for="(p, i) in summary.key_points" :key="i">{{ p }}</li>
          </ul>
        </div>
        <div v-if="summary.open_questions.length">
          <p class="text-xs font-medium uppercase tracking-wide text-muted-foreground mb-1.5">{{ $t('chat.ai.openQuestions') }}</p>
          <ul class="list-disc pl-5 space-y-1">
            <li v-for="(p, i) in summary.open_questions" :key="i">{{ p }}</li>
          </ul>
        </div>
      </div>

      <div class="flex justify-end gap-2 pt-2">
        <Button variant="outline" size="sm" :disabled="loading" @click="load">
          <RotateCw class="h-3.5 w-3.5 mr-1.5" />
          {{ $t('chat.ai.regenerate') }}
        </Button>
        <Button size="sm" :disabled="loading || !summary" @click="copy">
          <Copy class="h-3.5 w-3.5 mr-1.5" />
          {{ $t('chat.ai.copy') }}
        </Button>
      </div>
    </DialogContent>
  </Dialog>
</template>
