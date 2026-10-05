<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { chatAIService, contactsService, type ChatAITagsFieldsResult } from '@/services/api'
import type { Contact } from '@/stores/contacts'
import { getErrorMessage } from '@/lib/api-utils'
import { Loader2, ArrowRight } from 'lucide-vue-next'

const props = defineProps<{
  open: boolean
  contact: Contact
}>()

const emit = defineEmits<{
  (e: 'update:open', value: boolean): void
  (e: 'applied', patch: Partial<Contact>): void
}>()

const { t } = useI18n()
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const result = ref<ChatAITagsFieldsResult | null>(null)
const selectedTags = ref<Set<string>>(new Set())
const selectedFields = ref<Set<string>>(new Set())

const hasSuggestions = computed(() => !!result.value && (result.value.tags.length > 0 || result.value.fields.length > 0))
const selectedCount = computed(() => selectedTags.value.size + selectedFields.value.size)

watch(() => props.open, (open) => {
  if (open) load()
})

async function load() {
  loading.value = true
  error.value = ''
  result.value = null
  try {
    const res = await chatAIService.assist<ChatAITagsFieldsResult>(props.contact.id, { mode: 'suggest_tags_fields' })
    result.value = res.data.data
    selectedTags.value = new Set(result.value.tags)
    selectedFields.value = new Set(result.value.fields.map(f => f.key))
  } catch (e) {
    error.value = getErrorMessage(e, t('chat.ai.failed'))
  } finally {
    loading.value = false
  }
}

function toggle(set: Set<string>, key: string, checked: boolean | 'indeterminate') {
  const next = new Set(set)
  if (checked === true) next.add(key)
  else next.delete(key)
  return next
}

function fieldLabel(key: string) {
  return t(`chat.ai.field.${key}`)
}

async function apply() {
  if (!result.value || selectedCount.value === 0) return
  saving.value = true
  try {
    const body: Record<string, any> = {}
    const patch: Partial<Contact> = {}
    if (selectedTags.value.size) {
      const tags = [...(props.contact.tags || []), ...result.value.tags.filter(tg => selectedTags.value.has(tg))]
      body.tags = tags
      patch.tags = tags
    }
    const metadata: Record<string, any> = { ...(props.contact.metadata || {}) }
    let metaChanged = false
    for (const f of result.value.fields) {
      if (!selectedFields.value.has(f.key)) continue
      if (f.key === 'profile_name') {
        body.profile_name = f.value
        patch.profile_name = f.value
      } else {
        metadata[f.key] = f.value
        metaChanged = true
      }
    }
    if (metaChanged) {
      body.metadata = metadata
      patch.metadata = metadata
    }
    await contactsService.update(props.contact.id, body)
    emit('applied', patch)
    toast.success(t('chat.ai.contactUpdated'))
    emit('update:open', false)
  } catch (e) {
    toast.error(getErrorMessage(e, t('chat.ai.failed')))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="max-w-md">
      <DialogHeader>
        <DialogTitle>{{ $t('chat.ai.tagsTitle') }}</DialogTitle>
        <DialogDescription>{{ $t('chat.ai.tagsDesc') }}</DialogDescription>
      </DialogHeader>

      <div v-if="loading" class="flex items-center gap-2 py-8 justify-center text-sm text-muted-foreground">
        <Loader2 class="h-4 w-4 animate-spin" />
        {{ $t('chat.ai.thinking') }}
      </div>
      <p v-else-if="error" class="text-sm text-destructive py-4">{{ error }}</p>
      <p v-else-if="result && !hasSuggestions" class="text-sm text-muted-foreground py-4">{{ $t('chat.ai.noTagSuggestions') }}</p>
      <div v-else-if="result" class="space-y-4 text-sm">
        <div v-if="result.tags.length">
          <p class="text-xs font-medium uppercase tracking-wide text-muted-foreground mb-2">{{ $t('chat.ai.tags') }}</p>
          <label v-for="tag in result.tags" :key="tag" class="flex items-center gap-2.5 py-1 cursor-pointer">
            <Checkbox :checked="selectedTags.has(tag)" @update:checked="selectedTags = toggle(selectedTags, tag, $event)" />
            <span>{{ tag }}</span>
          </label>
        </div>
        <div v-if="result.fields.length">
          <p class="text-xs font-medium uppercase tracking-wide text-muted-foreground mb-2">{{ $t('chat.ai.fields') }}</p>
          <label v-for="f in result.fields" :key="f.key" class="flex items-start gap-2.5 py-1 cursor-pointer">
            <Checkbox class="mt-0.5" :checked="selectedFields.has(f.key)" @update:checked="selectedFields = toggle(selectedFields, f.key, $event)" />
            <span class="min-w-0">
              <span class="block font-medium">{{ fieldLabel(f.key) }}</span>
              <span class="flex items-center gap-1.5 text-xs text-muted-foreground break-all">
                <span v-if="f.current" class="line-through">{{ f.current }}</span>
                <ArrowRight v-if="f.current" class="h-3 w-3 shrink-0" />
                <span class="text-foreground">{{ f.value }}</span>
              </span>
            </span>
          </label>
        </div>
      </div>

      <div class="flex justify-end gap-2 pt-2">
        <Button variant="outline" size="sm" @click="emit('update:open', false)">{{ $t('common.cancel') }}</Button>
        <Button size="sm" :disabled="loading || saving || selectedCount === 0" @click="apply">
          <Loader2 v-if="saving" class="h-3.5 w-3.5 mr-1.5 animate-spin" />
          {{ $t('chat.ai.applySelected', { count: selectedCount }) }}
        </Button>
      </div>
    </DialogContent>
  </Dialog>
</template>
