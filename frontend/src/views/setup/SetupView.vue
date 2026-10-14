<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessageSquare, Loader2, Check, ShieldCheck, Database, UserRound, AlertTriangle, XCircle, Circle } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { setupService, type SetupDatabase, type SetupStatus } from '@/services/api'
import { afterSetup, defaultSQLitePath, SETUP_STEPS, stepsFor, type AdminForm, type SetupStep } from '@/lib/setup'
import SetupChecks from './SetupChecks.vue'
import SetupDatabaseStep from './SetupDatabase.vue'
import SetupAdmin from './SetupAdmin.vue'
import SetupFooter from './SetupFooter.vue'

const { t } = useI18n()

const status = ref<SetupStatus | null>(null)
const step = ref<SetupStep>('welcome')
const port = ref(4747)
const configPath = ref('')
const database = ref<SetupDatabase>({ driver: 'sqlite', path: '' })
const admin = ref<AdminForm>({ organization: '', fullName: '', email: '', password: '', confirm: '' })
const phase = ref<'form' | 'saving' | 'starting'>('form')
const error = ref('')
const loadFailed = ref(false)
const startTimedOut = ref(false)
const START_TIMEOUT_MS = 2 * 60 * 1000

const candidates = computed(() => status.value?.candidates ?? [])
const dataDir = computed(() => candidates.value.find(c => c.path === configPath.value)?.data_dir ?? '')
const stepIndex = computed(() => stepsFor('fresh').indexOf(step.value))
const plainHttp = window.location.protocol === 'http:'

const finishing = computed(() => phase.value !== 'form')
const stepState = (i: number) =>
  finishing.value || i < stepIndex.value ? 'done' : i === stepIndex.value ? 'current' : 'upcoming'
const stepSubtitle = computed(() => t(step.value === 'welcome' ? 'setup.welcome.body' : `setup.${step.value}.subtitle`))

const welcomeTiles = [
  { key: 'checks', icon: ShieldCheck },
  { key: 'database', icon: Database },
  { key: 'admin', icon: UserRound },
] as const

type PhaseState = 'pending' | 'running' | 'done' | 'failed'
const phases = computed<{ label: string; state: PhaseState }[]>(() => [
  { label: t('setup.finish.saving'), state: phase.value === 'saving' ? 'running' : 'done' },
  {
    label: t('setup.finish.starting'),
    state: phase.value === 'saving' ? 'pending' : startTimedOut.value ? 'failed' : 'running',
  },
])
const phaseIcon = { pending: Circle, running: Loader2, done: Check, failed: XCircle }
const phaseColor = {
  pending: 'text-white/25 light:text-gray-300',
  running: 'text-emerald-400 animate-spin light:text-emerald-600',
  done: 'text-emerald-400 light:text-emerald-600',
  failed: 'text-red-400 light:text-red-600',
}

// Keep the SQLite file in the chosen data folder unless the user typed a path.
watch(dataDir, (dir, old) => {
  const db = database.value
  if (dir && db.driver === 'sqlite' && (!db.path || db.path === defaultSQLitePath(old))) {
    database.value = { ...db, path: defaultSQLitePath(dir) }
  }
})

onMounted(async () => {
  try {
    const res = await setupService.status()
    status.value = res.data.data
    port.value = status.value.listen_port ?? 4747
    configPath.value = status.value.config_path ?? ''
  } catch {
    loadFailed.value = true
  }
})

function reload() {
  window.location.reload()
}

function go(next: SetupStep) {
  error.value = ''
  step.value = next
}

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms))

function loginPath(): string {
  const base = (((window as any).__BASE_PATH__ as string | undefined) ?? '').replace(/\/$/, '')
  return `${base}/login`
}

async function waitForOrigin(origin: string, deadline: number): Promise<boolean> {
  while (Date.now() < deadline) {
    try {
      await fetch(`${origin}/health`, { mode: 'no-cors', cache: 'no-store' })
      return true
    } catch {
      await sleep(1000)
    }
  }
  return false
}

async function waitForApp() {
  const deadline = Date.now() + START_TIMEOUT_MS
  while (Date.now() < deadline) {
    try {
      const next = afterSetup((await setupService.status()).data.data, window.location.origin)
      if (next.kind === 'login') {
        window.location.assign(loginPath())
        return
      }
      if (next.kind === 'moved') {
        if (await waitForOrigin(next.url, deadline)) {
          window.location.assign(`${next.url}/login`)
          return
        }
        break
      }
    } catch {
      // the server is switching from setup to the app
    }
    await sleep(1000)
  }
  startTimedOut.value = true
}

async function finish() {
  phase.value = 'saving'
  error.value = ''
  try {
    await setupService.complete({
      config_path: configPath.value,
      port: port.value,
      database: database.value,
      organization: admin.value.organization.trim(),
      admin: { full_name: admin.value.fullName.trim(), email: admin.value.email.trim(), password: admin.value.password },
    })
  } catch (e: any) {
    phase.value = 'form'
    // A conflict is usually a database that already has users, which only the database step can fix.
    if (e?.response?.status === 409) step.value = 'database'
    error.value = e?.response?.data?.message ?? t('setup.finish.failed')
    return
  }
  phase.value = 'starting'
  await waitForApp()
}
</script>

<template>
  <div class="relative min-h-screen overflow-clip bg-[#0a0a0b] light:bg-gray-50 flex items-center justify-center p-4 sm:p-8">
    <div aria-hidden="true" class="pointer-events-none absolute inset-0">
      <div class="absolute -top-48 left-1/2 h-[520px] w-[900px] -translate-x-1/2 rounded-full bg-emerald-500/[0.12] blur-[120px] light:bg-emerald-300/30" />
      <div class="absolute -bottom-40 -right-24 h-[380px] w-[480px] rounded-full bg-teal-500/[0.07] blur-[110px] light:bg-teal-200/30" />
      <div class="absolute inset-0 bg-[linear-gradient(to_right,rgba(255,255,255,0.035)_1px,transparent_1px),linear-gradient(to_bottom,rgba(255,255,255,0.035)_1px,transparent_1px)] bg-[size:56px_56px] [mask-image:radial-gradient(ellipse_at_center,black_30%,transparent_75%)] light:bg-[linear-gradient(to_right,rgba(0,0,0,0.04)_1px,transparent_1px),linear-gradient(to_bottom,rgba(0,0,0,0.04)_1px,transparent_1px)]" />
    </div>

    <div class="relative w-full max-w-5xl overflow-hidden rounded-3xl border border-white/[0.08] bg-[#0f0f11]/85 shadow-2xl shadow-black/50 backdrop-blur-xl md:grid md:grid-cols-[288px_minmax(0,1fr)] light:border-gray-200 light:bg-white/90 light:shadow-gray-300/40">
      <aside class="relative flex flex-col border-b border-white/[0.06] bg-gradient-to-b from-emerald-500/[0.08] via-transparent to-transparent p-6 md:border-b-0 md:border-r md:p-8 light:border-gray-200 light:from-emerald-50">
        <div class="flex items-center gap-3">
          <div class="h-10 w-10 rounded-xl bg-gradient-to-br from-emerald-400 to-green-600 flex items-center justify-center shadow-lg shadow-emerald-500/25">
            <MessageSquare class="h-5 w-5 text-white" />
          </div>
          <div>
            <p class="text-sm font-semibold text-white light:text-gray-900">whatomate</p>
            <p class="text-xs text-white/45 light:text-gray-500">{{ t('setup.title') }}</p>
          </div>
        </div>

        <ol class="mt-10 hidden md:block" :aria-label="t('setup.title')">
          <li v-for="(s, i) in SETUP_STEPS" :key="s" class="relative flex gap-4 pb-9 last:pb-0" :aria-current="stepState(i) === 'current' ? 'step' : undefined">
            <span
              v-if="i < SETUP_STEPS.length - 1"
              aria-hidden="true"
              class="absolute left-4 top-10 bottom-1 w-px -translate-x-1/2 transition-colors duration-300"
              :class="stepState(i) === 'done' ? 'bg-emerald-500/60' : 'bg-white/[0.08] light:bg-gray-200'"
            />
            <span
              class="relative z-10 flex h-8 w-8 shrink-0 items-center justify-center rounded-full text-xs font-semibold transition-all duration-300"
              :class="{
                'bg-emerald-500 text-white shadow-md shadow-emerald-500/30': stepState(i) === 'done',
                'bg-emerald-500/10 text-emerald-300 ring-2 ring-emerald-500/70 light:text-emerald-700': stepState(i) === 'current',
                'bg-white/[0.04] text-white/40 ring-1 ring-white/[0.1] light:bg-white light:text-gray-400 light:ring-gray-200': stepState(i) === 'upcoming',
              }"
            >
              <Check v-if="stepState(i) === 'done'" class="h-4 w-4" />
              <template v-else>{{ i + 1 }}</template>
            </span>
            <div class="pt-1">
              <p class="text-sm font-medium" :class="stepState(i) === 'upcoming' ? 'text-white/45 light:text-gray-400' : 'text-white light:text-gray-900'">
                {{ t(`setup.steps.${s}`) }}
              </p>
              <p class="mt-0.5 text-xs text-white/40 light:text-gray-500">{{ t(`setup.stepHints.${s}`) }}</p>
            </div>
          </li>
        </ol>

        <ol class="mt-5 flex items-center md:hidden" :aria-label="t('setup.title')">
          <li v-for="(s, i) in SETUP_STEPS" :key="s" class="flex items-center" :class="{ 'flex-1': i < SETUP_STEPS.length - 1 }">
            <span
              class="flex h-7 w-7 shrink-0 items-center justify-center rounded-full text-xs font-semibold"
              :class="{
                'bg-emerald-500 text-white': stepState(i) === 'done',
                'bg-emerald-500/10 text-emerald-300 ring-2 ring-emerald-500/70 light:text-emerald-700': stepState(i) === 'current',
                'bg-white/[0.04] text-white/40 ring-1 ring-white/[0.1] light:bg-white light:text-gray-400 light:ring-gray-200': stepState(i) === 'upcoming',
              }"
              :title="t(`setup.steps.${s}`)"
            >
              <Check v-if="stepState(i) === 'done'" class="h-3.5 w-3.5" />
              <template v-else>{{ i + 1 }}</template>
            </span>
            <span v-if="i < SETUP_STEPS.length - 1" aria-hidden="true" class="mx-2 h-px flex-1" :class="stepState(i) === 'done' ? 'bg-emerald-500/60' : 'bg-white/[0.08] light:bg-gray-200'" />
          </li>
        </ol>

        <p v-if="status" class="mt-auto hidden pt-10 text-xs text-white/30 md:block light:text-gray-400">
          {{ t('setup.welcome.version', { version: status.version }) }}
        </p>
      </aside>

      <main class="flex min-h-[600px] min-w-0 flex-col">
        <div v-if="loadFailed" class="flex flex-1 flex-col items-center justify-center gap-4 p-10 text-center" data-testid="setup-load-error">
          <div class="flex h-12 w-12 items-center justify-center rounded-2xl bg-red-500/10 ring-1 ring-red-500/30">
            <AlertTriangle class="h-6 w-6 text-red-400" />
          </div>
          <p class="max-w-sm text-sm text-white/70 light:text-gray-600">{{ t('setup.loadFailed') }}</p>
          <Button variant="outline" @click="reload">{{ t('setup.reload') }}</Button>
        </div>

        <div v-else-if="!status" class="flex flex-1 items-center justify-center">
          <Loader2 class="h-6 w-6 animate-spin text-white/40 light:text-gray-400" />
        </div>

        <div v-else-if="finishing" class="flex flex-1 flex-col items-center justify-center px-6 py-14 text-center sm:px-10" data-testid="setup-progress">
          <div class="flex h-14 w-14 items-center justify-center rounded-2xl bg-emerald-500/10 ring-1 ring-emerald-500/30">
            <Loader2 v-if="!startTimedOut" class="h-7 w-7 animate-spin text-emerald-400 light:text-emerald-600" />
            <AlertTriangle v-else class="h-7 w-7 text-red-400" />
          </div>
          <h3 class="mt-5 text-xl font-semibold text-white light:text-gray-900">{{ t('setup.finish.title') }}</h3>
          <p v-if="phase === 'saving'" class="mt-1 text-sm text-white/50 light:text-gray-500">{{ t('setup.finish.slow') }}</p>

          <ul class="mt-8 w-full max-w-md space-y-2 text-left">
            <li
              v-for="p in phases"
              :key="p.label"
              class="flex items-center gap-3 rounded-xl border px-4 py-3 text-sm transition-colors"
              :class="p.state === 'pending' ? 'border-white/[0.06] text-white/40 light:border-gray-100 light:text-gray-400' : 'border-white/[0.1] bg-white/[0.03] text-white/85 light:border-gray-200 light:bg-gray-50 light:text-gray-800'"
            >
              <component :is="phaseIcon[p.state]" class="h-4 w-4 shrink-0" :class="phaseColor[p.state]" />
              {{ p.label }}
            </li>
          </ul>

          <p v-if="startTimedOut" class="mt-6 max-w-md text-sm text-red-400 light:text-red-600" data-testid="setup-start-timeout">{{ t('setup.finish.timeout') }}</p>
          <div v-if="plainHttp" class="mt-6 flex max-w-md gap-2 rounded-xl border border-amber-500/20 bg-amber-500/[0.06] px-4 py-3 text-left text-xs text-amber-300/90 light:text-amber-700">
            <AlertTriangle class="mt-0.5 h-3.5 w-3.5 shrink-0" />
            <span>{{ t('setup.finish.httpNote') }}</span>
          </div>
        </div>

        <Transition
          v-else
          mode="out-in"
          enter-active-class="transition duration-300 ease-out motion-reduce:transition-none"
          enter-from-class="opacity-0 translate-y-3"
          leave-active-class="transition duration-150 ease-in motion-reduce:transition-none"
          leave-to-class="opacity-0 -translate-y-1"
        >
          <div :key="step" class="flex flex-1 flex-col">
            <header class="px-6 pt-8 pb-6 sm:px-10 sm:pt-10">
              <p class="text-xs font-medium uppercase tracking-wider text-emerald-400/90 light:text-emerald-600">
                {{ t('setup.stepOf', { n: stepIndex + 1, total: SETUP_STEPS.length }) }}
              </p>
              <h1 class="mt-2 text-2xl font-semibold tracking-tight text-white light:text-gray-900">{{ t(`setup.${step}.title`) }}</h1>
              <p class="mt-1.5 max-w-xl text-sm leading-relaxed text-white/55 light:text-gray-500">{{ stepSubtitle }}</p>
              <div v-if="error" class="mt-4 flex gap-2 rounded-xl border border-red-500/25 bg-red-500/[0.06] px-4 py-3 text-sm text-red-300 light:text-red-700" data-testid="setup-error">
                <XCircle class="mt-0.5 h-4 w-4 shrink-0" />
                <span class="break-words">{{ error }}</span>
              </div>
            </header>

            <div v-if="step === 'welcome'" class="flex flex-1 flex-col">
              <div class="flex-1 px-6 pb-8 sm:px-10">
                <div class="grid gap-3 sm:grid-cols-3">
                  <div
                    v-for="tile in welcomeTiles"
                    :key="tile.key"
                    class="rounded-2xl border border-white/[0.07] bg-white/[0.02] p-4 light:border-gray-200 light:bg-white"
                  >
                    <div class="flex h-9 w-9 items-center justify-center rounded-xl bg-emerald-500/10 text-emerald-400 ring-1 ring-emerald-500/20 light:text-emerald-600">
                      <component :is="tile.icon" class="h-[18px] w-[18px]" />
                    </div>
                    <p class="mt-3 text-sm font-medium text-white light:text-gray-900">{{ t(`setup.steps.${tile.key}`) }}</p>
                    <p class="mt-1 text-xs leading-relaxed text-white/45 light:text-gray-500">{{ t(`setup.welcome.tiles.${tile.key}`) }}</p>
                  </div>
                </div>
                <p class="mt-6 text-xs text-white/30 md:hidden light:text-gray-400">{{ t('setup.welcome.version', { version: status.version }) }}</p>
              </div>
              <SetupFooter>
                <span />
                <Button data-testid="setup-start" @click="go('checks')">
                  {{ t('setup.welcome.start') }}
                </Button>
              </SetupFooter>
            </div>

            <SetupChecks
              v-else-if="step === 'checks'"
              v-model:port="port"
              v-model:config-path="configPath"
              :candidates="candidates"
              :fixed="!!status.config_path_fixed"
              @back="go('welcome')"
              @next="go('database')"
            />

            <SetupDatabaseStep
              v-else-if="step === 'database'"
              v-model="database"
              :data-dir="dataDir"
              @back="go('checks')"
              @next="go('admin')"
            />

            <SetupAdmin
              v-else
              v-model="admin"
              @back="go('database')"
              @finish="finish"
            />
          </div>
        </Transition>
      </main>
    </div>
  </div>
</template>

