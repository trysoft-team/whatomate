<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { KeyRound, Loader2, XCircle } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { parseSetupToken } from '@/lib/setup'

defineProps<{ rejected: boolean; checking: boolean }>()
const emit = defineEmits<{ submit: [token: string] }>()
const { t } = useI18n()

const value = ref('')
const token = computed(() => parseSetupToken(value.value))
</script>

<template>
  <form class="flex flex-1 flex-col items-center justify-center gap-4 p-10 text-center" novalidate data-testid="setup-key" @submit.prevent="token && emit('submit', token)">
    <div class="flex h-12 w-12 items-center justify-center rounded-2xl bg-emerald-500/10 ring-1 ring-emerald-500/30">
      <KeyRound class="h-6 w-6 text-emerald-400 light:text-emerald-600" />
    </div>
    <h1 class="text-xl font-semibold text-white light:text-gray-900">{{ t('setup.key.title') }}</h1>
    <p class="max-w-md text-sm leading-relaxed text-white/60 light:text-gray-600">{{ t('setup.key.body') }}</p>
    <div class="w-full max-w-md space-y-2 text-left">
      <Label for="setup-key-input" class="text-white/70 light:text-gray-700">{{ t('setup.key.label') }}</Label>
      <Input id="setup-key-input" v-model="value" autocomplete="off" class="font-mono" :placeholder="t('setup.key.placeholder')" data-testid="setup-key-input" />
      <p v-if="rejected" class="flex items-center gap-1.5 text-xs text-red-400 light:text-red-600" data-testid="setup-key-rejected">
        <XCircle class="h-3.5 w-3.5 shrink-0" />{{ t('setup.key.rejected') }}
      </p>
    </div>
    <Button type="submit" :disabled="!token || checking" data-testid="setup-key-continue">
      <Loader2 v-if="checking" class="mr-2 h-4 w-4 animate-spin" />{{ t('setup.key.continue') }}
    </Button>
  </form>
</template>
