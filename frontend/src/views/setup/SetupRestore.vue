<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Upload, HardDrive, Loader2, CheckCircle2, AlertTriangle, XCircle, KeyRound, FileArchive } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Progress } from '@/components/ui/progress'
import SetupFooter from './SetupFooter.vue'
import { setupService, type BackupInfo, type SetupBackupSummary, type SetupDatabase, type SetupRestoreChoice } from '@/services/api'
import { formatBytes, uploadInChunks, UploadAbortedError } from '@/lib/backup'
import { keyState, restoreReady } from '@/lib/setup'
import { formatDateTime } from '@/lib/utils'

const props = defineProps<{ configPath: string; database: SetupDatabase }>()
const choice = defineModel<SetupRestoreChoice>({ required: true })
const emit = defineEmits<{ back: []; finish: [] }>()
const { t } = useI18n()

const tab = ref<'upload' | 'server'>('upload')
const dir = ref('')
const backups = ref<BackupInfo[]>([])
const typedPath = ref('')
const uploadPercent = ref<number | null>(null)
const summary = ref<SetupBackupSummary | null>(null)
const checking = ref(false)
const error = ref('')

const message = (e: any) => e?.response?.data?.message ?? String(e)

async function loadList() {
  try {
    const res = (await setupService.backups(props.configPath)).data.data
    dir.value = res.dir
    backups.value = res.backups
  } catch (e) {
    error.value = message(e)
  }
}
onMounted(loadList)

let seq = 0
async function inspect(keepSummary: boolean) {
  const backup = choice.value.backup
  if (!keepSummary) summary.value = null
  if (!backup) return
  const mine = ++seq
  checking.value = true
  error.value = ''
  try {
    const res = (await setupService.inspectBackup({
      config_path: props.configPath, database: props.database, backup, encryption_key: choice.value.encryptionKey.trim(),
    })).data.data
    if (mine === seq) summary.value = res
  } catch (e) {
    if (mine === seq) {
      summary.value = null
      error.value = message(e)
    }
  } finally {
    if (mine === seq) checking.value = false
  }
}

let keyTimer: ReturnType<typeof setTimeout> | undefined
watch(() => choice.value.backup, () => inspect(false), { deep: true, immediate: true })
watch(() => choice.value.encryptionKey, () => {
  clearTimeout(keyTimer)
  checking.value = !!choice.value.backup
  keyTimer = setTimeout(() => inspect(true), 400)
})

function pick(name: string) {
  choice.value = { ...choice.value, backup: { name } }
}

function usePath() {
  const p = typedPath.value.trim()
  if (p) choice.value = { ...choice.value, backup: { path: p } }
}

let uploadAbort: AbortController | null = null
const cancelling = ref(false)

async function onFile(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  uploadPercent.value = 0
  error.value = ''
  const ctrl = new AbortController()
  uploadAbort = ctrl
  try {
    const info = (await uploadInChunks(file, {
      start: async (name, size) => (await setupService.startUpload(props.configPath, name, size)).data.data,
      put: async (id, offset, chunk) => (await setupService.putChunk(id, offset, chunk)).data.data,
      finish: async (id) => (await setupService.finishUpload(id)).data.data,
      cancel: (id) => setupService.cancelUpload(id),
    }, (sent, total) => { uploadPercent.value = Math.round((sent / total) * 100) }, undefined, { signal: ctrl.signal })) as BackupInfo
    await loadList()
    pick(info.name)
  } catch (err) {
    if (!(err instanceof UploadAbortedError)) error.value = message(err)
  } finally {
    uploadAbort = null
    cancelling.value = false
    uploadPercent.value = null
  }
}

// The upload stops after the chunk in flight, then the server drops the partial file.
function cancelUpload() {
  cancelling.value = true
  uploadAbort?.abort()
}
onBeforeUnmount(() => uploadAbort?.abort())

const state = computed(() => (summary.value ? keyState(summary.value, choice.value.encryptionKey) : null))
const ready = computed(() => !checking.value && uploadPercent.value === null && restoreReady(summary.value, choice.value.encryptionKey))
const rows = computed(() => summary.value?.manifest.tables.reduce((n, tb) => n + tb.rows, 0) ?? 0)
const files = computed(() => Object.values(summary.value?.manifest.files ?? {}).reduce(
  (acc, f) => ({ count: acc.count + f.count, bytes: acc.bytes + f.bytes }), { count: 0, bytes: 0 }))
const selected = computed(() => choice.value.backup?.name ?? '')
const tabClass = (active: boolean) => active
  ? 'bg-emerald-500/10 text-emerald-300 ring-1 ring-emerald-500/40 light:text-emerald-700'
  : 'text-white/55 hover:bg-white/[0.04] light:text-gray-500 light:hover:bg-gray-100'
</script>

<template>
  <form class="flex flex-1 flex-col" novalidate @submit.prevent="ready && emit('finish')">
    <div class="flex-1 space-y-6 px-6 pb-8 sm:px-10">
      <div class="inline-flex gap-1 rounded-xl border border-white/[0.08] p-1 light:border-gray-200" role="tablist">
        <button type="button" role="tab" :aria-selected="tab === 'upload'" class="inline-flex items-center gap-2 rounded-lg px-3 py-1.5 text-sm transition-colors" :class="tabClass(tab === 'upload')" data-testid="setup-restore-tab-upload" @click="tab = 'upload'">
          <Upload class="h-4 w-4" />{{ t('setup.restore.tabUpload') }}
        </button>
        <button type="button" role="tab" :aria-selected="tab === 'server'" class="inline-flex items-center gap-2 rounded-lg px-3 py-1.5 text-sm transition-colors" :class="tabClass(tab === 'server')" data-testid="setup-restore-tab-server" @click="tab = 'server'">
          <HardDrive class="h-4 w-4" />{{ t('setup.restore.tabServer') }}
        </button>
      </div>

      <div v-if="tab === 'upload'" class="space-y-3">
        <label class="flex cursor-pointer items-center justify-center gap-2 rounded-2xl border border-dashed border-white/[0.15] px-4 py-8 text-sm text-white/70 transition-colors hover:border-emerald-500/50 light:border-gray-300 light:text-gray-600" :class="uploadPercent !== null ? 'pointer-events-none opacity-60' : ''">
          <FileArchive class="h-5 w-5" />{{ t('setup.restore.chooseFile') }}
          <input type="file" accept=".zip,application/zip" class="sr-only" data-testid="setup-restore-file" @change="onFile" />
        </label>
        <div v-if="uploadPercent !== null" class="flex items-center gap-3">
          <div class="min-w-0 flex-1 space-y-1.5">
            <Progress :model-value="uploadPercent" class="h-2" />
            <p class="text-xs text-white/50 light:text-gray-500">{{ t('setup.restore.uploading', { percent: uploadPercent }) }}</p>
          </div>
          <Button type="button" variant="outline" size="sm" :disabled="cancelling" data-testid="setup-restore-upload-cancel" @click="cancelUpload">
            {{ t('setup.restore.cancelUpload') }}
          </Button>
        </div>
      </div>

      <div v-else class="space-y-4">
        <div class="space-y-2">
          <p class="break-all text-xs text-white/45 light:text-gray-500">{{ t('setup.restore.folder', { dir }) }}</p>
          <p v-if="!backups.length" class="text-sm text-white/50 light:text-gray-500">{{ t('setup.restore.none') }}</p>
          <ul v-else class="max-h-56 space-y-1 overflow-y-auto">
            <li v-for="b in backups" :key="b.name">
              <button
                type="button"
                class="flex w-full items-center justify-between gap-3 rounded-xl border px-3 py-2 text-left text-sm transition-colors"
                :class="selected === b.name ? 'border-emerald-500/50 bg-emerald-500/[0.06]' : 'border-white/[0.08] hover:bg-white/[0.03] light:border-gray-200 light:hover:bg-gray-50'"
                :disabled="!!b.error"
                :data-testid="`setup-restore-backup-${b.name}`"
                @click="pick(b.name)"
              >
                <span class="min-w-0">
                  <span class="block truncate font-mono text-xs">{{ b.name }}</span>
                  <span class="block text-xs text-white/45 light:text-gray-500">{{ b.manifest ? formatDateTime(b.manifest.created_at) : b.error }}</span>
                </span>
                <span class="shrink-0 text-xs text-white/50 light:text-gray-500">{{ formatBytes(b.size) }}</span>
              </button>
            </li>
          </ul>
        </div>
        <div class="space-y-2">
          <Label for="setup-restore-path" class="text-white/70 light:text-gray-700">{{ t('setup.restore.pathLabel') }}</Label>
          <div class="flex gap-2">
            <Input id="setup-restore-path" v-model="typedPath" class="font-mono" :placeholder="t('setup.restore.pathPlaceholder')" data-testid="setup-restore-path" @keydown.enter.prevent="usePath" />
            <Button type="button" variant="outline" :disabled="!typedPath.trim()" @click="usePath">{{ t('setup.restore.usePath') }}</Button>
          </div>
        </div>
      </div>

      <div v-if="error" class="flex gap-2 rounded-xl border border-red-500/25 bg-red-500/[0.06] px-4 py-3 text-sm text-red-300 light:text-red-700" data-testid="setup-restore-error">
        <XCircle class="mt-0.5 h-4 w-4 shrink-0" /><span class="break-words">{{ error }}</span>
      </div>

      <p v-if="checking && !summary" class="flex items-center gap-2 text-sm text-white/55 light:text-gray-500">
        <Loader2 class="h-4 w-4 animate-spin" />{{ t('setup.restore.checking') }}
      </p>

      <div v-if="summary" class="space-y-4" data-testid="setup-restore-summary">
        <div class="space-y-1 rounded-xl border border-white/[0.08] bg-white/[0.02] p-4 text-sm light:border-gray-200 light:bg-gray-50">
          <p class="font-medium">{{ summary.name }}</p>
          <p class="text-white/60 light:text-gray-600">{{ t('setup.restore.move', { from: summary.manifest.source_driver, to: summary.target_driver }) }}</p>
          <p class="text-white/60 light:text-gray-600">{{ t('setup.restore.made', { date: formatDateTime(summary.manifest.created_at), version: summary.manifest.app_version || '?' }) }}</p>
          <p class="text-white/60 light:text-gray-600">{{ t('setup.restore.rows', { rows: rows.toLocaleString(), tables: summary.manifest.tables.length }) }}</p>
          <p class="text-white/60 light:text-gray-600">
            {{ summary.manifest.include_files ? t('setup.restore.files', { count: files.count, size: formatBytes(files.bytes) }) : t('setup.restore.noFiles') }}
          </p>
        </div>

        <div v-if="state !== 'not-needed'" class="space-y-2">
          <Label for="setup-restore-key" class="flex items-center gap-2 text-white/70 light:text-gray-700"><KeyRound class="h-3.5 w-3.5" />{{ t('setup.restore.keyLabel') }}</Label>
          <Input id="setup-restore-key" v-model="choice.encryptionKey" type="password" autocomplete="off" class="font-mono" data-testid="setup-restore-key" />
          <p class="text-xs text-white/45 light:text-gray-500">{{ t('setup.restore.keyHint') }}</p>
          <p v-if="checking" class="flex items-center gap-1.5 text-xs text-white/50"><Loader2 class="h-3 w-3 animate-spin" />{{ t('setup.restore.checking') }}</p>
          <p v-else-if="state === 'match'" class="flex items-center gap-1.5 text-xs text-emerald-400 light:text-emerald-600" data-testid="setup-restore-key-match"><CheckCircle2 class="h-3.5 w-3.5" />{{ t('setup.restore.keyMatch') }}</p>
          <p v-else-if="state === 'mismatch'" class="flex items-center gap-1.5 text-xs text-red-400 light:text-red-600" data-testid="setup-restore-key-mismatch"><XCircle class="h-3.5 w-3.5" />{{ t('setup.restore.keyMismatch') }}</p>
          <p v-else class="flex gap-2 rounded-xl border border-amber-500/20 bg-amber-500/[0.06] px-3 py-2 text-xs text-amber-300/90 light:text-amber-700"><AlertTriangle class="mt-0.5 h-3.5 w-3.5 shrink-0" />{{ t('setup.restore.keyBlank') }}</p>
        </div>
        <p v-else class="text-xs text-white/45 light:text-gray-500">{{ t('setup.restore.keyNotNeeded') }}</p>

        <p class="text-xs text-white/55 light:text-gray-500">{{ t('setup.restore.signInNote') }}</p>
      </div>
    </div>

    <SetupFooter>
      <Button type="button" variant="ghost" :disabled="uploadPercent !== null" @click="emit('back')">{{ t('setup.back') }}</Button>
      <Button type="submit" :disabled="!ready" data-testid="setup-restore-finish">{{ t('setup.restore.finish') }}</Button>
    </SetupFooter>
  </form>
</template>
