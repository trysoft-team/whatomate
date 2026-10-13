<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { adminErrors, passwordStrength, type AdminForm } from '@/lib/setup'

const form = defineModel<AdminForm>({ required: true })
const emit = defineEmits<{ back: []; finish: [] }>()
const { t } = useI18n()

const submitted = ref(false)
const errors = computed(() => adminErrors(form.value))
const strength = computed(() => passwordStrength(form.value.password))
const strengthKeys = ['tooShort', 'weak', 'fair', 'strong'] as const
const strengthColor = ['bg-red-500', 'bg-red-500', 'bg-amber-500', 'bg-emerald-500']

function finish() {
  submitted.value = true
  if (Object.keys(errors.value).length === 0) emit('finish')
}

const fields: { key: keyof AdminForm; type: string; autocomplete: string }[] = [
  { key: 'organization', type: 'text', autocomplete: 'organization' },
  { key: 'fullName', type: 'text', autocomplete: 'name' },
  { key: 'email', type: 'email', autocomplete: 'email' },
  { key: 'password', type: 'password', autocomplete: 'new-password' },
  { key: 'confirm', type: 'password', autocomplete: 'new-password' },
]
</script>

<template>
  <form class="space-y-5" novalidate @submit.prevent="finish">
    <div>
      <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ t('setup.admin.title') }}</h3>
      <p class="text-sm text-white/50 light:text-gray-500">{{ t('setup.admin.subtitle') }}</p>
    </div>

    <div v-for="f in fields" :key="f.key" class="space-y-2">
      <Label :for="`admin-${f.key}`" class="text-white/70 light:text-gray-700">{{ t(`setup.admin.${f.key}`) }}</Label>
      <Input :id="`admin-${f.key}`" v-model="form[f.key]" :type="f.type" :autocomplete="f.autocomplete" :data-testid="`setup-admin-${f.key}`" />
      <div v-if="f.key === 'password' && form.password" class="flex items-center gap-2">
        <div class="h-1.5 flex-1 rounded bg-white/[0.08] light:bg-gray-200 overflow-hidden">
          <div class="h-full transition-all" :class="strengthColor[strength]" :style="{ width: `${(strength / 3) * 100}%` }" />
        </div>
        <span class="text-xs text-white/50 light:text-gray-500">{{ t(`setup.admin.strength.${strengthKeys[strength]}`) }}</span>
      </div>
      <p v-if="submitted && errors[f.key]" class="text-xs text-red-500">{{ t(errors[f.key]!) }}</p>
    </div>

    <div class="flex justify-between">
      <Button type="button" variant="ghost" @click="emit('back')">{{ t('setup.back') }}</Button>
      <Button type="submit" data-testid="setup-finish">{{ t('setup.admin.finish') }}</Button>
    </div>
  </form>
</template>
