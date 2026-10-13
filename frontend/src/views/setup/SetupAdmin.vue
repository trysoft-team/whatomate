<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Building2, UserRound, Eye, EyeOff } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { adminErrors, passwordStrength, type AdminForm } from '@/lib/setup'
import SetupFooter from './SetupFooter.vue'

const form = defineModel<AdminForm>({ required: true })
const emit = defineEmits<{ back: []; finish: [] }>()
const { t } = useI18n()

const submitted = ref(false)
const showPassword = ref(false)
const errors = computed(() => adminErrors(form.value))
const strength = computed(() => passwordStrength(form.value.password))
const strengthKeys = ['tooShort', 'weak', 'fair', 'strong'] as const
const strengthColor = ['bg-red-500', 'bg-red-500', 'bg-amber-500', 'bg-emerald-500']
const strengthText = ['text-red-400', 'text-red-400', 'text-amber-400', 'text-emerald-400']

function finish() {
  submitted.value = true
  if (Object.keys(errors.value).length === 0) emit('finish')
}

type Field = { key: keyof AdminForm; type: string; autocomplete: string; span?: string }
const account: Field[] = [
  { key: 'fullName', type: 'text', autocomplete: 'name' },
  { key: 'email', type: 'email', autocomplete: 'email' },
  { key: 'password', type: 'password', autocomplete: 'new-password' },
  { key: 'confirm', type: 'password', autocomplete: 'new-password' },
]
const inputType = (f: Field) => (f.type === 'password' && showPassword.value ? 'text' : f.type)
const invalid = (key: keyof AdminForm) => submitted.value && !!errors.value[key]
</script>

<template>
  <form class="flex flex-1 flex-col" novalidate @submit.prevent="finish">
    <div class="flex-1 space-y-7 px-6 pb-8 sm:px-10">
      <section class="space-y-3">
        <h4 class="flex items-center gap-2 text-xs font-semibold uppercase tracking-wider text-white/45 light:text-gray-500">
          <Building2 class="h-3.5 w-3.5" />{{ t('setup.admin.orgSection') }}
        </h4>
        <div class="space-y-2">
          <Label for="admin-organization" class="text-white/70 light:text-gray-700">{{ t('setup.admin.organization') }}</Label>
          <Input
            id="admin-organization"
            v-model="form.organization"
            autocomplete="organization"
            data-testid="setup-admin-organization"
            :class="invalid('organization') ? 'border-red-500/60 light:border-red-400' : ''"
          />
          <p v-if="invalid('organization')" class="text-xs text-red-400 light:text-red-600">{{ t(errors.organization!) }}</p>
        </div>
      </section>

      <section class="space-y-3">
        <div class="flex items-center justify-between">
          <h4 class="flex items-center gap-2 text-xs font-semibold uppercase tracking-wider text-white/45 light:text-gray-500">
            <UserRound class="h-3.5 w-3.5" />{{ t('setup.admin.accountSection') }}
          </h4>
          <button
            type="button"
            class="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs text-white/55 transition-colors hover:bg-white/[0.06] hover:text-white light:text-gray-500 light:hover:bg-gray-100 light:hover:text-gray-900"
            :aria-pressed="showPassword"
            @click="showPassword = !showPassword"
          >
            <EyeOff v-if="showPassword" class="h-3.5 w-3.5" />
            <Eye v-else class="h-3.5 w-3.5" />
            {{ showPassword ? t('setup.admin.hidePassword') : t('setup.admin.showPassword') }}
          </button>
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <div v-for="f in account" :key="f.key" class="space-y-2">
            <Label :for="`admin-${f.key}`" class="text-white/70 light:text-gray-700">{{ t(`setup.admin.${f.key}`) }}</Label>
            <Input
              :id="`admin-${f.key}`"
              v-model="form[f.key]"
              :type="inputType(f)"
              :autocomplete="f.autocomplete"
              :data-testid="`setup-admin-${f.key}`"
              :class="invalid(f.key) ? 'border-red-500/60 light:border-red-400' : ''"
            />
            <div v-if="f.key === 'password' && form.password" class="flex items-center gap-2 pt-0.5">
              <div class="flex flex-1 gap-1">
                <span
                  v-for="n in 4"
                  :key="n"
                  class="h-1 flex-1 rounded-full transition-colors duration-300"
                  :class="n <= strength + 1 ? strengthColor[strength] : 'bg-white/[0.08] light:bg-gray-200'"
                />
              </div>
              <span class="w-14 text-right text-xs font-medium" :class="strengthText[strength]">{{ t(`setup.admin.strength.${strengthKeys[strength]}`) }}</span>
            </div>
            <p v-if="invalid(f.key)" class="text-xs text-red-400 light:text-red-600">{{ t(errors[f.key]!) }}</p>
          </div>
        </div>
      </section>
    </div>

    <SetupFooter>
      <Button type="button" variant="ghost" @click="emit('back')">{{ t('setup.back') }}</Button>
      <Button type="submit" data-testid="setup-finish">{{ t('setup.admin.finish') }}</Button>
    </SetupFooter>
  </form>
</template>
