<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessageSquare, Loader2 } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { setupService, type SetupDatabase, type SetupStatus } from '@/services/api'
import { afterSetup, defaultSQLitePath, SETUP_STEPS, type AdminForm, type SetupStep } from '@/lib/setup'
import SetupChecks from './SetupChecks.vue'
import SetupDatabaseStep from './SetupDatabase.vue'
import SetupAdmin from './SetupAdmin.vue'

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
const stepIndex = computed(() => SETUP_STEPS.indexOf(step.value))
const plainHttp = window.location.protocol === 'http:'

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
  <div class="min-h-screen flex items-center justify-center bg-[#0a0a0b] light:bg-gradient-to-br light:from-gray-50 light:to-gray-100 p-4">
    <div class="w-full max-w-2xl rounded-2xl border border-white/[0.08] bg-white/[0.02] backdrop-blur light:bg-white light:border-gray-200 light:shadow-xl">
      <div class="p-8 pb-4 flex items-center gap-3">
        <div class="h-10 w-10 rounded-xl bg-gradient-to-br from-emerald-500 to-green-600 flex items-center justify-center shadow-lg shadow-emerald-500/20">
          <MessageSquare class="h-6 w-6 text-white" />
        </div>
        <h2 class="text-xl font-bold text-white light:text-gray-900">{{ t('setup.title') }}</h2>
      </div>

      <ol v-if="phase === 'form'" class="px-8 flex gap-2">
        <li v-for="(s, i) in SETUP_STEPS" :key="s" class="flex-1">
          <div class="h-1 rounded" :class="i <= stepIndex ? 'bg-emerald-500' : 'bg-white/[0.08] light:bg-gray-200'" />
          <p class="mt-1 text-xs" :class="i === stepIndex ? 'text-white light:text-gray-900' : 'text-white/40 light:text-gray-500'">{{ t(`setup.steps.${s}`) }}</p>
        </li>
      </ol>

      <div class="p-8">
        <div v-if="loadFailed" class="space-y-4 py-6 text-center" data-testid="setup-load-error">
          <p class="text-sm text-red-500">{{ t('setup.loadFailed') }}</p>
          <Button variant="outline" @click="reload">{{ t('setup.reload') }}</Button>
        </div>

        <div v-else-if="!status" class="flex justify-center py-8"><Loader2 class="h-6 w-6 animate-spin text-white/50" /></div>

        <div v-else-if="phase !== 'form'" class="space-y-4 py-6 text-center" data-testid="setup-progress">
          <p v-if="startTimedOut" class="text-sm text-red-500 max-w-md mx-auto" data-testid="setup-start-timeout">{{ t('setup.finish.timeout') }}</p>
          <template v-else>
            <Loader2 class="mx-auto h-8 w-8 animate-spin text-emerald-500" />
            <p class="text-white light:text-gray-900">{{ phase === 'saving' ? t('setup.finish.saving') : t('setup.finish.starting') }}</p>
          </template>
          <p v-if="phase === 'saving'" class="text-sm text-white/50 light:text-gray-500">{{ t('setup.finish.slow') }}</p>
          <p v-if="plainHttp" class="text-xs text-amber-500 max-w-md mx-auto">{{ t('setup.finish.httpNote') }}</p>
        </div>

        <template v-else>
          <div v-if="step === 'welcome'" class="space-y-4">
            <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ t('setup.welcome.title') }}</h3>
            <p class="text-white/60 light:text-gray-600">{{ t('setup.welcome.body') }}</p>
            <p class="text-xs text-white/40 light:text-gray-500">{{ t('setup.welcome.version', { version: status.version }) }}</p>
            <div class="flex justify-end">
              <Button data-testid="setup-start" @click="go('checks')">{{ t('setup.welcome.start') }}</Button>
            </div>
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

          <p v-if="error" class="mt-4 text-sm text-red-500 break-words" data-testid="setup-error">{{ error }}</p>
        </template>
      </div>
    </div>
  </div>
</template>
