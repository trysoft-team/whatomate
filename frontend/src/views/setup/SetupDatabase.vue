<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Loader2 } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { setupService, type SetupDatabase, type SetupDatabaseResult, type SetupDriver } from '@/services/api'
import { databaseReady, defaultDatabasePort, defaultSQLitePath } from '@/lib/setup'

// The parent keeps the SQLite path in step with dataDir.
const props = defineProps<{ dataDir: string }>()
const db = defineModel<SetupDatabase>({ required: true })
const emit = defineEmits<{ back: []; next: [] }>()
const { t } = useI18n()

const drivers: SetupDriver[] = ['sqlite', 'postgres', 'mysql']
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
  <div class="space-y-5">
    <div>
      <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ t('setup.database.title') }}</h3>
      <p class="text-sm text-white/50 light:text-gray-500">{{ t('setup.database.subtitle') }}</p>
    </div>

    <div class="grid grid-cols-3 gap-2">
      <Button
        v-for="d in drivers"
        :key="d"
        :variant="db.driver === d ? 'default' : 'outline'"
        :data-testid="`setup-driver-${d}`"
        @click="choose(d)"
      >
        {{ t(`setup.database.drivers.${d}`) }}
      </Button>
    </div>

    <div v-if="db.driver === 'sqlite'" class="space-y-2">
      <Label for="db-path" class="text-white/70 light:text-gray-700">{{ t('setup.database.path') }}</Label>
      <Input id="db-path" v-model.trim="db.path" class="font-mono text-xs" />
    </div>
    <div v-else class="grid grid-cols-1 sm:grid-cols-2 gap-4">
      <div class="space-y-2">
        <Label for="db-host" class="text-white/70 light:text-gray-700">{{ t('setup.database.host') }}</Label>
        <Input id="db-host" v-model.trim="db.host" />
      </div>
      <div class="space-y-2">
        <Label for="db-port" class="text-white/70 light:text-gray-700">{{ t('setup.database.port') }}</Label>
        <Input id="db-port" v-model.number="db.port" type="number" min="1" max="65535" />
      </div>
      <div class="space-y-2">
        <Label for="db-user" class="text-white/70 light:text-gray-700">{{ t('setup.database.user') }}</Label>
        <Input id="db-user" v-model.trim="db.user" autocomplete="off" />
      </div>
      <div class="space-y-2">
        <Label for="db-password" class="text-white/70 light:text-gray-700">{{ t('setup.database.password') }}</Label>
        <Input id="db-password" v-model="db.password" type="password" autocomplete="new-password" />
      </div>
      <div class="space-y-2">
        <Label for="db-name" class="text-white/70 light:text-gray-700">{{ t('setup.database.name') }}</Label>
        <Input id="db-name" v-model.trim="db.name" />
      </div>
      <div v-if="db.driver === 'postgres'" class="space-y-2">
        <Label for="db-ssl" class="text-white/70 light:text-gray-700">{{ t('setup.database.sslMode') }}</Label>
        <select id="db-ssl" v-model="db.ssl_mode" class="h-9 w-full rounded-md border border-white/[0.1] bg-transparent px-3 text-sm light:border-gray-200">
          <option v-for="m in ['disable', 'require', 'verify-ca', 'verify-full']" :key="m" :value="m">{{ m }}</option>
        </select>
      </div>
    </div>

    <div class="flex flex-wrap items-center gap-3">
      <Button variant="outline" :disabled="busy !== ''" data-testid="setup-db-test" @click="test">
        <Loader2 v-if="busy === 'test'" class="mr-2 h-4 w-4 animate-spin" />
        {{ busy === 'test' ? t('setup.database.testing') : t('setup.database.test') }}
      </Button>
      <Button v-if="showCreate" :disabled="busy !== ''" data-testid="setup-db-create" @click="create">
        <Loader2 v-if="busy === 'create'" class="mr-2 h-4 w-4 animate-spin" />
        {{ busy === 'create' ? t('setup.database.creating') : t('setup.database.create') }}
      </Button>
    </div>
    <p v-if="result?.ok && (result.database_exists || db.driver === 'sqlite')" class="text-sm text-emerald-500">{{ t('setup.database.ok') }}</p>
    <p v-else-if="showCreate" class="text-sm text-amber-500">{{ t('setup.database.missing', { name: db.name }) }}</p>
    <p v-else-if="result && !result.ok" class="text-sm text-red-500 break-words">{{ t('setup.database.failed', { error: result.error }) }}</p>

    <div class="flex justify-between">
      <Button variant="ghost" @click="emit('back')">{{ t('setup.back') }}</Button>
      <Button :disabled="!ready" data-testid="setup-db-next" @click="emit('next')">{{ t('setup.next') }}</Button>
    </div>
  </div>
</template>
