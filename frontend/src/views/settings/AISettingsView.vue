<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import { Switch } from '@/components/ui/switch'
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from '@/components/ui/select'
import { PageHeader, AuditLogPanel } from '@/components/shared'
import { toast } from 'vue-sonner'
import { Brain, Loader2 } from 'lucide-vue-next'
import { chatbotService, aiProvidersService, type OpenRouterModel } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { useAuthStore } from '@/stores/auth'

const { t } = useI18n()
const authStore = useAuthStore()

const orgID = computed(
  () => localStorage.getItem('selected_organization_id') || authStore.organizationId,
)

const aiLogKey = ref(0)
const isSubmitting = ref(false)
const isLoading = ref(true)
const savedProvider = ref('')

const aiSettings = ref({
  ai_enabled: false,
  ai_provider: '',
  ai_api_key: '',
  ai_model: '',
  ai_max_tokens: 500,
  ai_system_prompt: '',
  ai_handoff_summary: true,
  voice_provider: 'piper',
  openai_tts_voice: 'alloy',
  elevenlabs_voice_id: '',
  elevenlabs_api_key: '',
  has_elevenlabs_key: false,
  transcribe_provider: '',
  transcribe_model: '',
  transcribe_api_key: '',
  has_transcribe_key: false,
})
const savedTranscribeProvider = ref('')

const isAIEnabled = ref(false)

const transcribeProviders = [
  { value: 'openai', label: 'OpenAI (Whisper)', defaultModel: 'whisper-1' },
  { value: 'google', label: 'Google Gemini', defaultModel: 'gemini-flash-latest' },
  { value: 'openrouter', label: 'OpenRouter', defaultModel: 'google/gemini-3.5-flash-lite' },
]
const transcribeProviderChoice = computed({
  get: () => aiSettings.value.transcribe_provider || 'same',
  set: (v: string) => {
    aiSettings.value.transcribe_provider = v === 'same' ? '' : v
  },
})
const effectiveTranscribeProvider = computed(() => aiSettings.value.transcribe_provider || aiSettings.value.ai_provider)
const transcribeUnsupported = computed(() => !transcribeProviders.some((p) => p.value === effectiveTranscribeProvider.value))
const transcribeUsesOwnKey = computed(
  () => !!aiSettings.value.transcribe_provider && aiSettings.value.transcribe_provider !== aiSettings.value.ai_provider,
)
const transcribeKeySaved = computed(
  () => aiSettings.value.has_transcribe_key && aiSettings.value.transcribe_provider === savedTranscribeProvider.value,
)
const transcribeModelPlaceholder = computed(() => {
  const p = effectiveTranscribeProvider.value
  if (p === 'google' && aiSettings.value.ai_provider === 'google' && aiSettings.value.ai_model) return aiSettings.value.ai_model
  return transcribeProviders.find((x) => x.value === p)?.defaultModel || ''
})

const aiProviders = [
  { value: 'openai', label: 'OpenAI', models: ['gpt-4o', 'gpt-4o-mini', 'gpt-4-turbo', 'gpt-3.5-turbo'] },
  { value: 'anthropic', label: 'Anthropic', models: ['claude-3-5-sonnet-latest', 'claude-3-5-haiku-latest', 'claude-3-opus-latest'] },
  {
    value: 'google',
    label: 'Google AI',
    models: [
      'gemini-3.8-flash',
      'gemini-3.7-flash',
      'gemini-3.6-flash',
      'gemini-3.5-flash',
      'gemini-3.5-flash-lite',
      'gemini-3.1-pro-preview',
      'gemini-3.1-flash-lite',
      'gemini-3-flash-preview',
      'gemini-flash-latest',
      'gemini-flash-lite-latest',
      'gemini-pro-latest',
    ],
  },
  { value: 'openrouter', label: 'OpenRouter', models: [] as string[] },
]

const availableModels = computed(() => {
  const provider = aiProviders.find((p) => p.value === aiSettings.value.ai_provider)
  return provider?.models || []
})

const isOpenRouter = computed(() => aiSettings.value.ai_provider === 'openrouter')
const openRouterModels = ref<OpenRouterModel[]>([])
const isLoadingOpenRouterModels = ref(false)
const openRouterModelsError = ref('')
const recommendedOpenRouterModels = computed(() => openRouterModels.value.filter((m) => m.recommended))
const otherOpenRouterModels = computed(() => openRouterModels.value.filter((m) => !m.recommended))

async function loadOpenRouterModels() {
  if (isLoadingOpenRouterModels.value || openRouterModels.value.length) return
  isLoadingOpenRouterModels.value = true
  openRouterModelsError.value = ''
  try {
    const res = await aiProvidersService.openRouterFreeModels()
    openRouterModels.value = res.data.data?.models || []
  } catch (e) {
    openRouterModelsError.value = getErrorMessage(e, t('chatbotSettings.openRouterModelsFailed'))
  } finally {
    isLoadingOpenRouterModels.value = false
  }
}

function formatContext(tokens: number) {
  if (!tokens) return ''
  return tokens >= 1_000_000 ? `${Math.round(tokens / 100_000) / 10}M` : `${Math.round(tokens / 1000)}K`
}

watch(isAIEnabled, (newValue) => {
  aiSettings.value.ai_enabled = newValue
})

watch(() => aiSettings.value.ai_provider, (provider, previous) => {
  if (provider !== 'openrouter') return
  loadOpenRouterModels()
  if (!aiSettings.value.ai_model || (previous && previous !== 'openrouter')) {
    aiSettings.value.ai_model = 'openrouter/free'
  }
})

onMounted(async () => {
  try {
    const chatbotResponse = await chatbotService.getSettings()
    const chatbotData = chatbotResponse.data.data || chatbotResponse.data
    if (chatbotData.settings) {
      const aiEnabledValue = chatbotData.settings.ai_enabled === true
      isAIEnabled.value = aiEnabledValue
      savedProvider.value = chatbotData.settings.ai_provider || ''
      aiSettings.value = {
        ai_enabled: aiEnabledValue,
        ai_provider: chatbotData.settings.ai_provider || '',
        ai_api_key: '',
        ai_model: chatbotData.settings.ai_model || '',
        ai_max_tokens: chatbotData.settings.ai_max_tokens || 500,
        ai_system_prompt: chatbotData.settings.ai_system_prompt || '',
        ai_handoff_summary: chatbotData.settings.ai_handoff_summary !== false,
        voice_provider: chatbotData.settings.voice_provider || 'piper',
        openai_tts_voice: chatbotData.settings.openai_tts_voice || 'alloy',
        elevenlabs_voice_id: chatbotData.settings.elevenlabs_voice_id || '',
        elevenlabs_api_key: '',
        has_elevenlabs_key: !!chatbotData.settings.has_elevenlabs_key,
        transcribe_provider: chatbotData.settings.transcribe_provider || '',
        transcribe_model: chatbotData.settings.transcribe_model || '',
        transcribe_api_key: '',
        has_transcribe_key: !!chatbotData.settings.has_transcribe_key,
      }
      savedTranscribeProvider.value = aiSettings.value.transcribe_provider
    }
  } catch (error) {
    console.error('Failed to load AI settings:', error)
    toast.error(t('chatbotSettings.aiLoadFailed', 'Failed to load AI settings'))
  } finally {
    isLoading.value = false
  }
})

async function saveAISettings() {
  const providerChanged = !!savedProvider.value && aiSettings.value.ai_provider !== savedProvider.value
  if (aiSettings.value.ai_enabled && providerChanged && !aiSettings.value.ai_api_key) {
    toast.error(t('chatbotSettings.apiKeyRequiredForProvider'))
    return
  }
  if (aiSettings.value.ai_enabled && transcribeUsesOwnKey.value && !transcribeKeySaved.value && !aiSettings.value.transcribe_api_key) {
    toast.error(t('chatbotSettings.transcribeApiKeyRequired'))
    return
  }
  isSubmitting.value = true
  try {
    const payload: Record<string, unknown> = {
      ai_enabled: aiSettings.value.ai_enabled,
      ai_provider: aiSettings.value.ai_provider,
      ai_model: aiSettings.value.ai_model,
      ai_max_tokens: aiSettings.value.ai_max_tokens,
      ai_system_prompt: aiSettings.value.ai_system_prompt,
      ai_handoff_summary: aiSettings.value.ai_handoff_summary,
      voice_provider: aiSettings.value.voice_provider,
      openai_tts_voice: aiSettings.value.openai_tts_voice,
      elevenlabs_voice_id: aiSettings.value.elevenlabs_voice_id,
      transcribe_provider: aiSettings.value.transcribe_provider,
      transcribe_model: aiSettings.value.transcribe_model,
    }
    if (aiSettings.value.ai_api_key) {
      payload.ai_api_key = aiSettings.value.ai_api_key
    }
    if (aiSettings.value.transcribe_api_key) {
      payload.transcribe_api_key = aiSettings.value.transcribe_api_key
    }
    if (aiSettings.value.elevenlabs_api_key) {
      payload.elevenlabs_api_key = aiSettings.value.elevenlabs_api_key
    }
    await chatbotService.updateSettings(payload)
    toast.success(t('chatbotSettings.aiSettingsSaved'))
    savedProvider.value = aiSettings.value.ai_provider
    if (payload.transcribe_api_key) {
      aiSettings.value.has_transcribe_key = true
    } else if (aiSettings.value.transcribe_provider !== savedTranscribeProvider.value) {
      aiSettings.value.has_transcribe_key = false
    }
    savedTranscribeProvider.value = aiSettings.value.transcribe_provider
    aiSettings.value.transcribe_api_key = ''
    aiSettings.value.ai_api_key = ''
    aiSettings.value.elevenlabs_api_key = ''
    if (payload.elevenlabs_api_key) {
      aiSettings.value.has_elevenlabs_key = true
    }
    setTimeout(() => {
      aiLogKey.value++
    }, 500)
  } catch {
    toast.error(t('chatbotSettings.aiSaveFailed'))
  } finally {
    isSubmitting.value = false
  }
}
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader
      :title="$t('chatbotSettings.aiSettings')"
      :subtitle="$t('chatbotSettings.aiSettingsDesc')"
      :icon="Brain"
      icon-gradient="bg-gradient-to-br from-violet-500 to-fuchsia-600 shadow-violet-500/20"
      back-link="/settings"
      :breadcrumbs="[
        { label: $t('nav.settings'), href: '/settings' },
        { label: $t('nav.aiSettings', 'AI') },
      ]"
    />
    <ScrollArea class="flex-1">
      <div class="p-6 space-y-4 max-w-4xl mx-auto">
        <div v-if="isLoading" class="flex justify-center py-16">
          <Loader2 class="h-8 w-8 animate-spin text-muted-foreground" />
        </div>
        <template v-else>
          <Card>
            <CardHeader>
              <CardTitle>{{ $t('chatbotSettings.aiSettings') }}</CardTitle>
              <CardDescription>{{ $t('chatbotSettings.aiSettingsDesc') }}</CardDescription>
            </CardHeader>
            <CardContent class="space-y-4">
              <div class="flex items-center justify-between">
                <div>
                  <p class="font-medium">{{ $t('chatbotSettings.enableAI') }}</p>
                  <p class="text-sm text-muted-foreground">{{ $t('chatbotSettings.enableAIDesc') }}</p>
                </div>
                <Switch
                  :checked="isAIEnabled"
                  @update:checked="(val: boolean) => (isAIEnabled = val)"
                />
              </div>

              <p class="text-xs text-muted-foreground">
                {{ $t('chatbotSettings.aiResponsesMovedHint') }}
              </p>

              <div v-if="isAIEnabled" class="space-y-4 pt-2">
                <Separator />

                <div class="grid grid-cols-2 gap-4">
                  <div class="space-y-2">
                    <Label>{{ $t('chatbotSettings.aiProvider') }}</Label>
                    <Select v-model="aiSettings.ai_provider">
                      <SelectTrigger>
                        <SelectValue :placeholder="$t('chatbotSettings.selectProvider') + '...'" />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem v-for="provider in aiProviders" :key="provider.value" :value="provider.value">
                          {{ provider.label }}
                        </SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                  <div v-if="isOpenRouter" class="space-y-2">
                    <Label>{{ $t('chatbotSettings.model') }}</Label>
                    <Select v-model="aiSettings.ai_model" :disabled="isLoadingOpenRouterModels">
                      <SelectTrigger data-testid="openrouter-model-select">
                        <SelectValue :placeholder="isLoadingOpenRouterModels ? $t('chatbotSettings.loadingModels') : $t('chatbotSettings.selectModel') + '...'" />
                      </SelectTrigger>
                      <SelectContent class="max-h-80">
                        <SelectGroup v-if="recommendedOpenRouterModels.length">
                          <SelectLabel>{{ $t('chatbotSettings.recommendedFreeModels') }}</SelectLabel>
                          <SelectItem v-for="m in recommendedOpenRouterModels" :key="m.id" :value="m.id">
                            {{ m.name }}<span v-if="m.context_length" class="ml-1.5 text-xs text-muted-foreground">{{ formatContext(m.context_length) }}</span>
                          </SelectItem>
                        </SelectGroup>
                        <SelectGroup v-if="otherOpenRouterModels.length">
                          <SelectLabel>{{ $t('chatbotSettings.otherFreeModels') }}</SelectLabel>
                          <SelectItem v-for="m in otherOpenRouterModels" :key="m.id" :value="m.id">
                            {{ m.name }}<span v-if="m.context_length" class="ml-1.5 text-xs text-muted-foreground">{{ formatContext(m.context_length) }}</span>
                          </SelectItem>
                        </SelectGroup>
                        <SelectItem
                          v-if="aiSettings.ai_model && !openRouterModels.some((m) => m.id === aiSettings.ai_model)"
                          :value="aiSettings.ai_model"
                        >
                          {{ aiSettings.ai_model }}
                        </SelectItem>
                      </SelectContent>
                    </Select>
                    <p v-if="openRouterModelsError" class="text-xs text-destructive">{{ openRouterModelsError }}</p>
                  </div>
                  <div v-else class="space-y-2">
                    <Label>{{ $t('chatbotSettings.model') }}</Label>
                    <Select v-model="aiSettings.ai_model" :disabled="!aiSettings.ai_provider">
                      <SelectTrigger>
                        <SelectValue :placeholder="$t('chatbotSettings.selectModel') + '...'" />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem v-for="model in availableModels" :key="model" :value="model">
                          {{ model }}
                        </SelectItem>
                        <SelectItem
                          v-if="aiSettings.ai_model && !availableModels.includes(aiSettings.ai_model)"
                          :value="aiSettings.ai_model"
                        >
                          {{ aiSettings.ai_model }}
                        </SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                </div>

                <div class="space-y-2">
                  <Label>{{ $t('chatbotSettings.apiKey') }}</Label>
                  <Input
                    v-model="aiSettings.ai_api_key"
                    type="password"
                    :placeholder="$t('chatbotSettings.apiKeyPlaceholder') + '...'"
                  />
                  <p class="text-xs text-muted-foreground">{{ $t('chatbotSettings.apiKeyHint') }}</p>
                </div>

                <div v-if="isOpenRouter" class="space-y-2">
                  <Label>{{ $t('chatbotSettings.customModelId') }}</Label>
                  <Input v-model.trim="aiSettings.ai_model" class="font-mono" placeholder="openrouter/free" />
                  <p class="text-xs text-muted-foreground">
                    {{ $t('chatbotSettings.openRouterHint') }}
                    <a href="https://openrouter.ai/keys" target="_blank" rel="noopener noreferrer" class="underline">openrouter.ai/keys</a>
                  </p>
                </div>

                <div class="space-y-2">
                  <Label>{{ $t('chatbotSettings.maxTokens') }}</Label>
                  <Input v-model.number="aiSettings.ai_max_tokens" type="number" min="100" max="4000" class="w-32" />
                </div>

                <div class="space-y-2">
                  <Label>{{ $t('chatbotSettings.systemPrompt') }}</Label>
                  <Textarea
                    v-model="aiSettings.ai_system_prompt"
                    :placeholder="$t('chatbotSettings.systemPromptPlaceholder') + '...'"
                    :rows="3"
                  />
                </div>

                <div class="flex items-center justify-between">
                  <div>
                    <p class="font-medium">{{ $t('chatbotSettings.handoffSummary') }}</p>
                    <p class="text-sm text-muted-foreground">{{ $t('chatbotSettings.handoffSummaryDesc') }}</p>
                  </div>
                  <Switch
                    :checked="aiSettings.ai_handoff_summary"
                    @update:checked="(val: boolean) => (aiSettings.ai_handoff_summary = val)"
                  />
                </div>

                <Separator />
                <div class="space-y-4" data-testid="transcribe-settings">
                  <div>
                    <p class="font-medium">{{ $t('chatbotSettings.transcription') }}</p>
                    <p class="text-sm text-muted-foreground">{{ $t('chatbotSettings.transcriptionDesc') }}</p>
                  </div>
                  <div class="grid grid-cols-2 gap-4">
                    <div class="space-y-2">
                      <Label>{{ $t('chatbotSettings.transcribeProvider') }}</Label>
                      <Select v-model="transcribeProviderChoice">
                        <SelectTrigger data-testid="transcribe-provider-select">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="same">{{ $t('chatbotSettings.sameAsAIProvider') }}</SelectItem>
                          <SelectItem v-for="p in transcribeProviders" :key="p.value" :value="p.value">
                            {{ p.label }}
                          </SelectItem>
                        </SelectContent>
                      </Select>
                    </div>
                    <div class="space-y-2">
                      <Label>{{ $t('chatbotSettings.model') }}</Label>
                      <Input
                        v-model.trim="aiSettings.transcribe_model"
                        class="font-mono"
                        :placeholder="transcribeModelPlaceholder"
                        :disabled="transcribeUnsupported"
                      />
                    </div>
                  </div>
                  <div v-if="transcribeUsesOwnKey" class="space-y-2">
                    <Label>{{ $t('chatbotSettings.transcribeApiKey') }}</Label>
                    <Input
                      v-model="aiSettings.transcribe_api_key"
                      type="password"
                      :placeholder="transcribeKeySaved ? $t('chatbotSettings.apiKeyPlaceholder') + '...' : $t('chatbotSettings.transcribeApiKeyPlaceholder')"
                    />
                  </div>
                  <p v-if="transcribeUnsupported" class="text-xs text-destructive">{{ $t('chatbotSettings.transcribeUnsupported') }}</p>
                  <p v-else-if="effectiveTranscribeProvider === 'openrouter'" class="text-xs text-muted-foreground">
                    {{ $t('chatbotSettings.transcribeOpenRouterHint') }}
                  </p>
                  <p v-else class="text-xs text-muted-foreground">{{ $t('chatbotSettings.transcribeHint') }}</p>
                </div>
              </div>

              <Separator class="my-4" />
              <div class="space-y-4">
                <div>
                  <p class="font-medium">{{ $t('chatbotSettings.voiceTts', 'Voice / TTS') }}</p>
                  <p class="text-sm text-muted-foreground">{{ $t('chatbotSettings.voiceTtsDesc', 'Default voice provider for call flow greetings and AI Call nodes.') }}</p>
                </div>
                <div class="grid grid-cols-2 gap-4">
                  <div class="space-y-2">
                    <Label>{{ $t('chatbotSettings.voiceProvider', 'Provider') }}</Label>
                    <Select v-model="aiSettings.voice_provider">
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="piper">Piper (local)</SelectItem>
                        <SelectItem value="openai">OpenAI TTS</SelectItem>
                        <SelectItem value="elevenlabs">ElevenLabs</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                  <div v-if="aiSettings.voice_provider === 'openai'" class="space-y-2">
                    <Label>OpenAI voice</Label>
                    <Select v-model="aiSettings.openai_tts_voice">
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="alloy">alloy</SelectItem>
                        <SelectItem value="echo">echo</SelectItem>
                        <SelectItem value="fable">fable</SelectItem>
                        <SelectItem value="onyx">onyx</SelectItem>
                        <SelectItem value="nova">nova</SelectItem>
                        <SelectItem value="shimmer">shimmer</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                  <div v-if="aiSettings.voice_provider === 'elevenlabs'" class="space-y-2">
                    <Label>ElevenLabs voice ID</Label>
                    <Input v-model="aiSettings.elevenlabs_voice_id" class="font-mono" placeholder="21m00Tcm4TlvDq8ikWAM" />
                  </div>
                </div>
                <div v-if="aiSettings.voice_provider === 'elevenlabs'" class="space-y-2">
                  <Label>ElevenLabs API key</Label>
                  <Input
                    v-model="aiSettings.elevenlabs_api_key"
                    type="password"
                    :placeholder="aiSettings.has_elevenlabs_key ? '•••••••• (leave blank to keep)' : 'xi-…'"
                  />
                </div>
                <p v-if="aiSettings.voice_provider === 'openai'" class="text-xs text-muted-foreground">
                  Uses the OpenAI API key above (or server ai.openai_key).
                </p>
              </div>

              <div class="flex justify-end pt-2">
                <Button @click="saveAISettings" :disabled="isSubmitting">
                  <Loader2 v-if="isSubmitting" class="mr-2 h-4 w-4 animate-spin" />
                  {{ $t('chatbotSettings.saveChanges') }}
                </Button>
              </div>
            </CardContent>
          </Card>
          <div v-if="orgID" class="mt-4">
            <AuditLogPanel :key="aiLogKey" resource-type="settings.chatbot.ai" :resource-id="orgID" />
          </div>
        </template>
      </div>
    </ScrollArea>
  </div>
</template>
