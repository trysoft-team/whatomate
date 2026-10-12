<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { chatAIService, type ChatAIRewriteTone, type ChatAITextResult } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { Sparkles, Loader2, Wand2, Languages, FileText, Tags } from 'lucide-vue-next'

const props = defineProps<{
  contactId: string
  draft: string
  canEditContact?: boolean
}>()

const emit = defineEmits<{
  (e: 'apply', text: string, note?: string): void
  (e: 'summarize'): void
  (e: 'suggest-tags'): void
}>()

const { t } = useI18n()
const open = ref(false)
const busy = ref<string | null>(null)

const tones: ChatAIRewriteTone[] = ['friendly', 'professional', 'shorter', 'grammar', 'whatsapp']

async function run(key: string, body: Parameters<typeof chatAIService.assist>[1], note?: (r: ChatAITextResult) => string | undefined) {
  if (busy.value) return
  busy.value = key
  try {
    const res = await chatAIService.assist<ChatAITextResult>(props.contactId, body)
    const result = res.data.data
    if (!result?.text) throw new Error(t('chat.ai.emptyResult'))
    emit('apply', result.text, note?.(result))
    open.value = false
  } catch (e) {
    toast.error(getErrorMessage(e, t('chat.ai.failed')))
  } finally {
    busy.value = null
  }
}

function suggestReply() {
  run('suggest', { mode: 'suggest_reply' })
}

function rewrite(tone: ChatAIRewriteTone) {
  run(`rewrite:${tone}`, { mode: 'rewrite', tone, draft: props.draft })
}

function translateToCustomer() {
  run('translate', { mode: 'translate', target_language: 'customer', draft: props.draft }, r =>
    r.target_language ? t('chat.ai.translatedTo', { language: r.target_language }) : undefined)
}

function pick(event: 'summarize' | 'suggest-tags') {
  open.value = false
  if (event === 'summarize') emit('summarize')
  else emit('suggest-tags')
}
</script>

<template>
  <Popover v-model:open="open">
    <PopoverTrigger as-child>
      <button
        type="button"
        class="w-9 h-9 rounded-lg hover:bg-white/[0.08] light:hover:bg-gray-200 flex items-center justify-center transition-colors"
        :aria-label="$t('chat.ai.menu')"
        data-testid="chat-ai-menu"
      >
        <Loader2 v-if="busy" class="w-[18px] h-[18px] text-violet-400 animate-spin" />
        <Sparkles v-else class="w-[18px] h-[18px] text-violet-400 light:text-violet-500" />
      </button>
    </PopoverTrigger>
    <PopoverContent side="top" align="start" class="w-72 p-1.5">
      <button
        type="button"
        class="w-full flex items-start gap-2.5 rounded-md px-2.5 py-2 text-left hover:bg-accent disabled:opacity-50"
        :disabled="!!busy"
        @click="suggestReply"
      >
        <Loader2 v-if="busy === 'suggest'" class="h-4 w-4 mt-0.5 animate-spin text-violet-500" />
        <Sparkles v-else class="h-4 w-4 mt-0.5 text-violet-500" />
        <span class="min-w-0">
          <span class="block text-sm font-medium">{{ $t('chat.ai.suggestReply') }}</span>
          <span class="block text-xs text-muted-foreground">{{ $t('chat.ai.suggestReplyHint') }}</span>
        </span>
      </button>

      <div class="px-2.5 pt-2 pb-1">
        <div class="flex items-center gap-2.5 text-sm font-medium" :class="!draft.trim() && 'opacity-50'">
          <Wand2 class="h-4 w-4 text-violet-500" />
          {{ $t('chat.ai.rewrite') }}
        </div>
        <p v-if="!draft.trim()" class="text-xs text-muted-foreground mt-1 ml-[26px]">{{ $t('chat.ai.typeFirst') }}</p>
        <div v-else class="flex flex-wrap gap-1 mt-1.5 ml-[26px]">
          <button
            v-for="tone in tones"
            :key="tone"
            type="button"
            class="inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs hover:bg-accent disabled:opacity-50"
            :disabled="!!busy"
            @click="rewrite(tone)"
          >
            <Loader2 v-if="busy === `rewrite:${tone}`" class="h-3 w-3 animate-spin" />
            {{ $t(`chat.ai.tone.${tone}`) }}
          </button>
        </div>
      </div>

      <button
        type="button"
        class="w-full flex items-start gap-2.5 rounded-md px-2.5 py-2 text-left hover:bg-accent disabled:opacity-50"
        :disabled="!!busy || !draft.trim()"
        @click="translateToCustomer"
      >
        <Loader2 v-if="busy === 'translate'" class="h-4 w-4 mt-0.5 animate-spin text-violet-500" />
        <Languages v-else class="h-4 w-4 mt-0.5 text-violet-500" />
        <span class="min-w-0">
          <span class="block text-sm font-medium">{{ $t('chat.ai.translateDraft') }}</span>
          <span class="block text-xs text-muted-foreground">{{ $t('chat.ai.translateDraftHint') }}</span>
        </span>
      </button>

      <div class="my-1 h-px bg-border" />

      <button
        type="button"
        class="w-full flex items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-sm hover:bg-accent disabled:opacity-50"
        :disabled="!!busy"
        @click="pick('summarize')"
      >
        <FileText class="h-4 w-4 text-violet-500" />
        {{ $t('chat.ai.summarize') }}
      </button>
      <button
        v-if="canEditContact"
        type="button"
        class="w-full flex items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-sm hover:bg-accent disabled:opacity-50"
        :disabled="!!busy"
        @click="pick('suggest-tags')"
      >
        <Tags class="h-4 w-4 text-violet-500" />
        {{ $t('chat.ai.suggestTags') }}
      </button>
    </PopoverContent>
  </Popover>
</template>
