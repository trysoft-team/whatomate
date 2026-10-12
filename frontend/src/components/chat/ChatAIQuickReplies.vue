<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { chatAIService } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { Sparkles, Loader2, RotateCw, X } from 'lucide-vue-next'

const props = defineProps<{
  contactId: string
  // Suggestions are cleared whenever the latest message changes.
  lastMessageId?: string
}>()

const emit = defineEmits<{
  (e: 'use', text: string): void
}>()

const { t } = useI18n()
const replies = ref<string[]>([])
const loading = ref(false)

watch(() => [props.contactId, props.lastMessageId], () => {
  replies.value = []
})

async function load() {
  if (loading.value) return
  loading.value = true
  try {
    const res = await chatAIService.assist<{ replies: string[] }>(props.contactId, { mode: 'quick_replies' })
    replies.value = res.data.data?.replies || []
    if (replies.value.length === 0) toast.info(t('chat.ai.noQuickReplies'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('chat.ai.failed')))
  } finally {
    loading.value = false
  }
}

function use(text: string) {
  emit('use', text)
  replies.value = []
}
</script>

<template>
  <div class="flex items-center gap-1.5 mb-2 min-h-[26px] overflow-x-auto" data-testid="chat-ai-quick-replies">
    <template v-if="replies.length">
      <button
        v-for="(reply, i) in replies"
        :key="i"
        type="button"
        class="shrink-0 max-w-[280px] truncate rounded-full border border-violet-500/30 bg-violet-500/10 px-3 py-1 text-xs text-violet-200 hover:bg-violet-500/20 light:text-violet-700 light:bg-violet-50 light:border-violet-200 light:hover:bg-violet-100"
        :title="reply"
        @click="use(reply)"
      >
        {{ reply }}
      </button>
      <button
        type="button"
        class="shrink-0 h-6 w-6 rounded-full flex items-center justify-center text-white/40 hover:text-white hover:bg-white/[0.08] light:text-gray-400 light:hover:text-gray-700 light:hover:bg-gray-100"
        :title="$t('chat.ai.refresh')"
        :disabled="loading"
        @click="load"
      >
        <Loader2 v-if="loading" class="h-3.5 w-3.5 animate-spin" />
        <RotateCw v-else class="h-3.5 w-3.5" />
      </button>
      <button
        type="button"
        class="shrink-0 h-6 w-6 rounded-full flex items-center justify-center text-white/40 hover:text-white hover:bg-white/[0.08] light:text-gray-400 light:hover:text-gray-700 light:hover:bg-gray-100"
        :title="$t('common.close')"
        @click="replies = []"
      >
        <X class="h-3.5 w-3.5" />
      </button>
    </template>
    <button
      v-else
      type="button"
      class="shrink-0 inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs text-white/50 hover:text-violet-300 hover:bg-violet-500/10 light:text-gray-500 light:hover:text-violet-700 light:hover:bg-violet-50 disabled:opacity-60"
      :disabled="loading"
      @click="load"
    >
      <Loader2 v-if="loading" class="h-3.5 w-3.5 animate-spin" />
      <Sparkles v-else class="h-3.5 w-3.5" />
      {{ loading ? $t('chat.ai.thinking') : $t('chat.ai.quickReplies') }}
    </button>
  </div>
</template>
