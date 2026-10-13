<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { CheckCircle2, AlertTriangle, XCircle, Loader2, RefreshCw } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { setupService, type SetupCandidate, type SetupCheck } from '@/services/api'
import { hasFailedCheck, validPort } from '@/lib/setup'

const props = defineProps<{ candidates: SetupCandidate[]; fixed: boolean }>()
const port = defineModel<number>('port', { required: true })
const configPath = defineModel<string>('configPath', { required: true })
const emit = defineEmits<{ back: []; next: [] }>()
const { t } = useI18n()

const checks = ref<SetupCheck[]>([])
const loading = ref(false)
const failed = ref('')
let timer: ReturnType<typeof setTimeout> | undefined

const dataDir = computed(() => props.candidates.find(c => c.path === configPath.value)?.data_dir ?? '')
const canContinue = computed(() => !loading.value && checks.value.length > 0 && !hasFailedCheck(checks.value) && validPort(port.value))

async function run() {
  if (!validPort(port.value) || !configPath.value) return
  loading.value = true
  failed.value = ''
  try {
    const res = await setupService.checks({ port: port.value, config_path: configPath.value })
    checks.value = res.data.data.checks
  } catch (e: any) {
    failed.value = e?.response?.data?.message ?? String(e)
  } finally {
    loading.value = false
  }
}

watch([port, configPath], () => {
  clearTimeout(timer)
  timer = setTimeout(run, 400)
})
onMounted(run)

const icon = { pass: CheckCircle2, warn: AlertTriangle, fail: XCircle }
const color = { pass: 'text-emerald-500', warn: 'text-amber-500', fail: 'text-red-500' }
</script>

<template>
  <div class="space-y-5">
    <div>
      <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ t('setup.checks.title') }}</h3>
      <p class="text-sm text-white/50 light:text-gray-500">{{ t('setup.checks.subtitle') }}</p>
    </div>

    <div class="grid grid-cols-1 sm:grid-cols-3 gap-4">
      <div class="space-y-2">
        <Label for="setup-port" class="text-white/70 light:text-gray-700">{{ t('setup.checks.port') }}</Label>
        <Input id="setup-port" v-model.number="port" type="number" min="1" max="65535" data-testid="setup-port" />
        <p class="text-xs text-white/40 light:text-gray-500">{{ t('setup.checks.portHint') }}</p>
      </div>
      <div class="space-y-2 sm:col-span-2">
        <Label class="text-white/70 light:text-gray-700">{{ t('setup.checks.location') }}</Label>
        <div class="space-y-1">
          <label
            v-for="c in candidates"
            :key="c.path"
            class="flex items-start gap-2 rounded-lg border border-white/[0.08] light:border-gray-200 p-2 text-sm cursor-pointer"
            :class="{ 'opacity-50 cursor-not-allowed': !c.writable && !fixed }"
          >
            <input v-model="configPath" type="radio" :value="c.path" :disabled="!c.writable && !fixed" class="mt-1" />
            <span class="min-w-0">
              <span class="block font-mono text-xs break-all text-white/80 light:text-gray-800">{{ c.path }}</span>
              <span class="text-xs text-white/40 light:text-gray-500">
                {{ t(`setup.checks.scope.${c.scope}`) }}<template v-if="!c.writable"> · {{ t('setup.checks.notWritable') }}</template>
              </span>
            </span>
          </label>
        </div>
        <p v-if="dataDir" class="text-xs text-white/40 light:text-gray-500">{{ t('setup.checks.locationHint', { dir: dataDir }) }}</p>
      </div>
    </div>

    <ul class="divide-y divide-white/[0.06] light:divide-gray-100 rounded-lg border border-white/[0.08] light:border-gray-200">
      <li v-for="c in checks" :key="c.id" class="flex items-start gap-3 p-3" :data-testid="`check-${c.id}`">
        <component :is="icon[c.status]" class="h-5 w-5 shrink-0" :class="color[c.status]" />
        <div class="min-w-0">
          <p class="text-sm font-medium text-white light:text-gray-900">{{ t(`setup.checks.items.${c.id}`) }}</p>
          <p class="text-xs text-white/50 light:text-gray-500 break-words">{{ c.detail }}</p>
        </div>
      </li>
      <li v-if="loading && checks.length === 0" class="flex items-center gap-2 p-3 text-sm text-white/50">
        <Loader2 class="h-4 w-4 animate-spin" />
      </li>
    </ul>
    <p v-if="failed" class="text-sm text-red-500">{{ failed }}</p>

    <div class="flex justify-between">
      <Button variant="ghost" @click="emit('back')">{{ t('setup.back') }}</Button>
      <div class="flex gap-2">
        <Button variant="outline" :disabled="loading" @click="run">
          <RefreshCw class="mr-2 h-4 w-4" :class="{ 'animate-spin': loading }" />{{ t('setup.checks.rerun') }}
        </Button>
        <Button :disabled="!canContinue" data-testid="setup-checks-next" @click="emit('next')">{{ t('setup.next') }}</Button>
      </div>
    </div>
  </div>
</template>
