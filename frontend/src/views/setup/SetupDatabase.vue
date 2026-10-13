<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Loader2, HardDrive, Database, Server, CheckCircle2, AlertTriangle, XCircle, Plug } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import SetupFooter from './SetupFooter.vue'
import { setupService, type SetupDatabase, type SetupDatabaseResult, type SetupDriver } from '@/services/api'
import { databaseReady, defaultDatabasePort, defaultSQLitePath } from '@/lib/setup'

// The parent keeps the SQLite path in step with dataDir.
const props = defineProps<{ dataDir: string }>()
const db = defineModel<SetupDatabase>({ required: true })
const emit = defineEmits<{ back: []; next: [] }>()
const { t } = useI18n()

const drivers: SetupDriver[] = ['sqlite', 'postgres', 'mysql']
const driverIcon = { sqlite: HardDrive, postgres: Database, mysql: Server }
const sslModes = ['disable', 'require', 'verify-ca', 'verify-full']
const result = ref<SetupDatabaseResult | null>(null)
const busy = ref<'' | 'test' | 'create'>('')

function choose(driver: SetupDriver) {
  db.value = driver === 'sqlite'
    ? { driver, path: defaultSQLitePath(props.dataDir) }
    : { driver, host: 'localhost', port: defaultDatabasePort(driver), user: '', password: '', name: 'whatomate', ...(driver === 'postgres' ? { ssl_mode: 'disable' } : {}) }
}

watch(db, () => { result.value = null }, { deep: true })

const ready = computed(() => databaseReady(db.value, result.value))
const showCreate = computed(() => db.value.driver !== 'sqlite' && result.value?.ok && !result.value.database_exists)

async function test() {
  busy.value = 'test'
  try {
    result.value = (await setupService.testDatabase(db.value)).data.data
  } catch (e: any) {
    result.value = { ok: false, database_exists: false, error: e?.response?.data?.message ?? String(e) }
  } finally {
    busy.value = ''
  }
}

async function create() {
  busy.value = 'create'
  try {
    const res = (await setupService.createDatabase(db.value)).data.data
    if (!res.ok) {
      result.value = res
      return
    }
  } catch (e: any) {
    result.value = { ok: false, database_exists: false, error: e?.response?.data?.message ?? String(e) }
    return
  } finally {
    busy.value = ''
  }
  await test()
}
</script>

<template>
  <div class="flex flex-1 flex-col">
    <div class="flex-1 space-y-7 px-6 pb-8 sm:px-10">
      <div class="grid gap-2.5 sm:grid-cols-3" role="radiogroup">
        <button
          v-for="d in drivers"
          :key="d"
          type="button"
          role="radio"
          :aria-checked="db.driver === d"
          class="relative flex flex-col items-start rounded-2xl border p-4 text-left transition-all duration-200 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-500/50"
          :class="db.driver === d
            ? 'border-emerald-500/60 bg-emerald-500/[0.07] ring-1 ring-emerald-500/30 light:bg-emerald-50'
            : 'border-white/[0.08] bg-white/[0.015] hover:border-white/20 light:border-gray-200 light:bg-white light:hover:border-gray-300'"
          :data-testid="`setup-driver-${d}`"
          @click="choose(d)"
        >
          <span
            class="flex h-9 w-9 items-center justify-center rounded-xl ring-1"
            :class="db.driver === d ? 'bg-emerald-500/15 text-emerald-300 ring-emerald-500/30 light:text-emerald-700' : 'bg-white/[0.04] text-white/55 ring-white/[0.08] light:bg-gray-50 light:text-gray-500 light:ring-gray-200'"
          >
            <component :is="driverIcon[d]" class="h-[18px] w-[18px]" />
          </span>
          <span
            v-if="d === 'sqlite'"
            class="absolute right-3 top-3 rounded-full bg-emerald-500/15 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-emerald-300 light:text-emerald-700"
          >
            {{ t('setup.database.recommended') }}
          </span>
          <span class="mt-3 text-sm font-medium text-white light:text-gray-900">{{ t(`setup.database.drivers.${d}`) }}</span>
          <span class="mt-0.5 text-xs leading-relaxed text-white/45 light:text-gray-500">{{ t(`setup.database.driverHints.${d}`) }}</span>
        </button>
      </div>

      <div v-if="db.driver === 'sqlite'" class="space-y-2">
        <Label for="db-path" class="text-white/70 light:text-gray-700">{{ t('setup.database.path') }}</Label>
        <Input id="db-path" v-model.trim="db.path" class="font-mono text-xs" />
      </div>
      <div v-else class="grid gap-4 sm:grid-cols-6">
        <div class="space-y-2 sm:col-span-4">
          <Label for="db-host" class="text-white/70 light:text-gray-700">{{ t('setup.database.host') }}</Label>
          <Input id="db-host" v-model.trim="db.host" />
        </div>
        <div class="space-y-2 sm:col-span-2">
          <Label for="db-port" class="text-white/70 light:text-gray-700">{{ t('setup.database.port') }}</Label>
          <Input id="db-port" v-model.number="db.port" type="number" min="1" max="65535" class="font-mono" />
        </div>
        <div class="space-y-2 sm:col-span-3">
          <Label for="db-user" class="text-white/70 light:text-gray-700">{{ t('setup.database.user') }}</Label>
          <Input id="db-user" v-model.trim="db.user" autocomplete="off" />
        </div>
        <div class="space-y-2 sm:col-span-3">
          <Label for="db-password" class="text-white/70 light:text-gray-700">{{ t('setup.database.password') }}</Label>
          <Input id="db-password" v-model="db.password" type="password" autocomplete="new-password" />
        </div>
        <div class="space-y-2" :class="db.driver === 'postgres' ? 'sm:col-span-3' : 'sm:col-span-6'">
          <Label for="db-name" class="text-white/70 light:text-gray-700">{{ t('setup.database.name') }}</Label>
          <Input id="db-name" v-model.trim="db.name" />
        </div>
        <div v-if="db.driver === 'postgres'" class="space-y-2 sm:col-span-3">
          <Label for="db-ssl" class="text-white/70 light:text-gray-700">{{ t('setup.database.sslMode') }}</Label>
          <Select v-model="db.ssl_mode">
            <SelectTrigger id="db-ssl" class="bg-white/[0.04] border-white/[0.1] text-white/80 light:bg-white light:border-gray-200 light:text-gray-800">
              <SelectValue />
            </SelectTrigger>
            <SelectContent class="bg-[#141414] border-white/[0.08] light:bg-white light:border-gray-200">
              <SelectItem v-for="m in sslModes" :key="m" :value="m">{{ m }}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>

      <div class="space-y-3">
        <div class="flex flex-wrap items-center gap-2">
          <Button variant="outline" :disabled="busy !== ''" data-testid="setup-db-test" @click="test">
            <Loader2 v-if="busy === 'test'" class="mr-2 h-4 w-4 animate-spin" />
            <Plug v-else class="mr-2 h-4 w-4" />
            {{ busy === 'test' ? t('setup.database.testing') : t('setup.database.test') }}
          </Button>
          <Button v-if="showCreate" :disabled="busy !== ''" data-testid="setup-db-create" @click="create">
            <Loader2 v-if="busy === 'create'" class="mr-2 h-4 w-4 animate-spin" />
            {{ busy === 'create' ? t('setup.database.creating') : t('setup.database.create') }}
          </Button>
        </div>

        <div
          v-if="result?.ok && (result.database_exists || db.driver === 'sqlite')"
          class="flex items-center gap-2 rounded-xl border border-emerald-500/25 bg-emerald-500/[0.06] px-4 py-3 text-sm text-emerald-300 light:text-emerald-700"
        >
          <CheckCircle2 class="h-4 w-4 shrink-0" />{{ t('setup.database.ok') }}
        </div>
        <div v-else-if="showCreate" class="flex gap-2 rounded-xl border border-amber-500/25 bg-amber-500/[0.06] px-4 py-3 text-sm text-amber-300 light:text-amber-700">
          <AlertTriangle class="mt-0.5 h-4 w-4 shrink-0" />{{ t('setup.database.missing', { name: db.name }) }}
        </div>
        <div v-else-if="result && !result.ok" class="flex gap-2 rounded-xl border border-red-500/25 bg-red-500/[0.06] px-4 py-3 text-sm text-red-300 light:text-red-700">
          <XCircle class="mt-0.5 h-4 w-4 shrink-0" />
          <span class="break-words">{{ t('setup.database.failed', { error: result.error }) }}</span>
        </div>
      </div>
    </div>

    <SetupFooter>
      <Button variant="ghost" @click="emit('back')">{{ t('setup.back') }}</Button>
      <Button :disabled="!ready" data-testid="setup-db-next" @click="emit('next')">{{ t('setup.next') }}</Button>
    </SetupFooter>
  </div>
</template>

