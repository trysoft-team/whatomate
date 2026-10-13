<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { CheckCircle2, AlertTriangle, XCircle, RefreshCw, UserRound, Server, FileCog } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { setupService, type SetupCandidate, type SetupCheck } from '@/services/api'
import { hasFailedCheck, validPort } from '@/lib/setup'
import SetupFooter from './SetupFooter.vue'

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
const tone = {
  pass: { icon: 'bg-emerald-500/10 text-emerald-400 ring-emerald-500/20 light:text-emerald-600', tile: 'border-white/[0.07] bg-white/[0.02] light:border-gray-200 light:bg-white', pill: 'bg-emerald-500/10 text-emerald-300 light:text-emerald-700' },
  warn: { icon: 'bg-amber-500/10 text-amber-400 ring-amber-500/20 light:text-amber-600', tile: 'border-amber-500/20 bg-amber-500/[0.04] light:bg-amber-50', pill: 'bg-amber-500/10 text-amber-300 light:text-amber-700' },
  fail: { icon: 'bg-red-500/10 text-red-400 ring-red-500/25 light:text-red-600', tile: 'border-red-500/30 bg-red-500/[0.05] light:bg-red-50', pill: 'bg-red-500/10 text-red-300 light:text-red-700' },
}
const scopeIcon = { user: UserRound, system: Server, fixed: FileCog }
const statuses = ['pass', 'warn', 'fail'] as const
const counts = computed(() => ({
  pass: checks.value.filter(c => c.status === 'pass').length,
  warn: checks.value.filter(c => c.status === 'warn').length,
  fail: checks.value.filter(c => c.status === 'fail').length,
}))
</script>

<template>
  <div class="flex flex-1 flex-col">
    <div class="flex-1 space-y-8 px-6 pb-8 sm:px-10">
      <section class="grid grid-cols-1 gap-6">
        <div class="max-w-xs space-y-2">
          <Label for="setup-port" class="text-white/70 light:text-gray-700">{{ t('setup.checks.port') }}</Label>
          <Input id="setup-port" v-model.number="port" type="number" min="1" max="65535" class="font-mono" data-testid="setup-port" />
          <p class="text-xs leading-relaxed text-white/40 light:text-gray-500">{{ t('setup.checks.portHint') }}</p>
        </div>

        <div class="min-w-0 space-y-2">
          <Label class="text-white/70 light:text-gray-700">{{ t('setup.checks.location') }}</Label>
          <div class="grid grid-cols-1 gap-2" role="radiogroup">
            <label
              v-for="c in candidates"
              :key="c.path"
              class="group flex items-center gap-3 rounded-xl border px-3.5 py-3 transition-all duration-200"
              :class="[
                configPath === c.path
                  ? 'border-emerald-500/60 bg-emerald-500/[0.06] ring-1 ring-emerald-500/30 light:bg-emerald-50'
                  : 'border-white/[0.08] bg-white/[0.015] hover:border-white/20 light:border-gray-200 light:bg-white light:hover:border-gray-300',
                !c.writable && !fixed ? 'cursor-not-allowed opacity-45' : 'cursor-pointer',
              ]"
            >
              <input v-model="configPath" type="radio" class="sr-only" :value="c.path" :disabled="!c.writable && !fixed" />
              <span
                class="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg ring-1"
                :class="configPath === c.path ? 'bg-emerald-500/15 text-emerald-300 ring-emerald-500/30 light:text-emerald-700' : 'bg-white/[0.04] text-white/50 ring-white/[0.08] light:bg-gray-50 light:text-gray-500 light:ring-gray-200'"
              >
                <component :is="scopeIcon[c.scope]" class="h-4 w-4" />
              </span>
              <span class="min-w-0 flex-1">
                <span class="block break-all font-mono text-xs leading-relaxed text-white/85 light:text-gray-800">{{ c.path }}</span>
                <span class="mt-1 flex flex-wrap items-center gap-1.5">
                  <span class="rounded-md bg-white/[0.06] px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-white/55 light:bg-gray-100 light:text-gray-600">
                    {{ t(`setup.checks.scope.${c.scope}`) }}
                  </span>
                  <span v-if="!c.writable" class="rounded-md bg-red-500/10 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-red-300 light:text-red-600">
                    {{ t('setup.checks.notWritable') }}
                  </span>
                </span>
              </span>
              <span
                class="flex h-4 w-4 shrink-0 items-center justify-center rounded-full border"
                :class="configPath === c.path ? 'border-emerald-400 light:border-emerald-600' : 'border-white/20 light:border-gray-300'"
              >
                <span v-if="configPath === c.path" class="h-2 w-2 rounded-full bg-emerald-400 light:bg-emerald-600" />
              </span>
            </label>
          </div>
          <p v-if="dataDir" class="break-words text-xs leading-relaxed text-white/40 light:text-gray-500">{{ t('setup.checks.locationHint', { dir: dataDir }) }}</p>
        </div>
      </section>

      <section class="space-y-3">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="flex flex-wrap items-center gap-1.5 text-xs">
            <template v-if="checks.length > 0">
              <span v-for="s in statuses" :key="s" class="rounded-full px-2.5 py-1 font-medium" :class="tone[s].pill">
                {{ t(`setup.checks.summary.${s}`, { n: counts[s] }) }}
              </span>
            </template>
            <span v-else-if="loading" class="text-white/45 light:text-gray-500">{{ t('setup.checks.checking') }}</span>
          </div>
          <Button variant="ghost" size="sm" :disabled="loading" @click="run">
            <RefreshCw class="mr-1.5 h-3.5 w-3.5" :class="{ 'animate-spin': loading }" />{{ t('setup.checks.rerun') }}
          </Button>
        </div>

        <ul v-if="checks.length > 0" class="grid gap-2.5 sm:grid-cols-2" :class="{ 'opacity-60 transition-opacity': loading }">
          <li v-for="c in checks" :key="c.id" class="flex items-start gap-3 rounded-xl border p-3.5" :class="tone[c.status].tile" :data-testid="`check-${c.id}`">
            <span class="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg ring-1" :class="tone[c.status].icon">
              <component :is="icon[c.status]" class="h-4 w-4" />
            </span>
            <div class="min-w-0">
              <p class="text-sm font-medium text-white light:text-gray-900">{{ t(`setup.checks.items.${c.id}`) }}</p>
              <p class="mt-0.5 text-xs leading-relaxed text-white/50 break-words light:text-gray-500">{{ c.detail }}</p>
            </div>
          </li>
        </ul>
        <ul v-else-if="loading" class="grid gap-2.5 sm:grid-cols-2" aria-hidden="true">
          <li v-for="n in 8" :key="n" class="flex items-center gap-3 rounded-xl border border-white/[0.06] p-3.5 light:border-gray-100">
            <span class="h-8 w-8 animate-pulse rounded-lg bg-white/[0.06] light:bg-gray-100" />
            <span class="flex-1 space-y-2">
              <span class="block h-3 w-1/3 animate-pulse rounded bg-white/[0.07] light:bg-gray-100" />
              <span class="block h-2.5 w-2/3 animate-pulse rounded bg-white/[0.05] light:bg-gray-100" />
            </span>
          </li>
        </ul>

        <div v-if="failed" class="flex gap-2 rounded-xl border border-red-500/25 bg-red-500/[0.06] px-4 py-3 text-sm text-red-300 light:text-red-700">
          <XCircle class="mt-0.5 h-4 w-4 shrink-0" />
          <span class="break-words">{{ failed }}</span>
        </div>
      </section>
    </div>

    <SetupFooter>
      <Button variant="ghost" @click="emit('back')">{{ t('setup.back') }}</Button>
      <Button :disabled="!canContinue" data-testid="setup-checks-next" @click="emit('next')">{{ t('setup.next') }}</Button>
    </SetupFooter>
  </div>
</template>

