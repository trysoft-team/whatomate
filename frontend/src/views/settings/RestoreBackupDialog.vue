<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { AlertTriangle, CheckCircle2, Loader2, XCircle } from 'lucide-vue-next'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Progress } from '@/components/ui/progress'
import { backupsService, type BackupSummary, type MaintenanceStatus } from '@/services/api'
import { RESTORE_WORD, canConfirmRestore, formatBytes, jobPercent, nextRestoreStage, type RestoreStage } from '@/lib/backup'
import { getErrorMessage } from '@/lib/api-utils'
import { formatDateTime } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth'

const props = defineProps<{ open: boolean; name: string | null }>()
const emit = defineEmits<{ 'update:open': [boolean]; finished: [] }>()
const { t } = useI18n()
const router = useRouter()
const authStore = useAuthStore()

type Stage = 'loading' | 'confirm' | RestoreStage
const stage = ref<Stage>('loading')
const summary = ref<BackupSummary | null>(null)
const error = ref('')
const typed = ref('')
const starting = ref(false)
const status = ref<MaintenanceStatus['restore'] | null>(null)
let timer: ReturnType<typeof setInterval> | null = null
let missing = 0

const rows = computed(() => summary.value?.manifest.tables.reduce((n, tb) => n + tb.rows, 0) ?? 0)
const files = computed(() => {
  const f = summary.value?.manifest.files ?? {}
  return Object.values(f).reduce((acc, s) => ({ count: acc.count + s.count, bytes: acc.bytes + s.bytes }), { count: 0, bytes: 0 })
})
const percent = computed(() => (status.value ? jobPercent(status.value) : 0))

function stopPolling() {
  if (timer) clearInterval(timer)
  timer = null
}

watch(() => [props.open, props.name], async ([open]) => {
  stopPolling()
  if (!open || !props.name) return
  stage.value = 'loading'
  summary.value = null
  error.value = ''
  typed.value = ''
  try {
    const res = await backupsService.inspect(props.name)
    summary.value = res.data.data
    stage.value = 'confirm'
  } catch (e) {
    error.value = getErrorMessage(e, t('backup.restoreDialog.failedTitle'))
    stage.value = 'failed'
  }
}, { immediate: true })

async function poll() {
  let r: MaintenanceStatus['restore']
  try {
    r = (await backupsService.maintenance()).data.data.restore
  } catch {
    return // The server is busy restoring; keep polling.
  }
  const next = nextRestoreStage(r, missing)
  missing = next.missing
  if (r) status.value = r
  if (next.stage === 'running') return
  stopPolling()
  // A partial restore signed everyone out, so /admin/backup-job would bounce
  // to the login page; the backup page shows its error after signing in.
  if (next.stage === 'failed') {
    try {
      const job = await backupsService.job()
      error.value = job.data.data.error || t('backup.restoreDialog.failedBody')
    } catch {
      error.value = t('backup.restoreDialog.failedBody')
    }
  }
  stage.value = next.stage
}

async function start() {
  if (starting.value || !props.name || !canConfirmRestore(typed.value)) return
  starting.value = true
  try {
    await backupsService.restore(props.name)
    stage.value = 'running'
    missing = 0
    timer = setInterval(poll, 1000)
  } catch (e) {
    error.value = getErrorMessage(e, t('backup.restoreDialog.failedTitle'))
    stage.value = 'failed'
  } finally {
    starting.value = false
  }
}

async function signIn() {
  await authStore.logout().catch(() => {})
  emit('update:open', false)
  router.push('/login')
}

function close(open: boolean) {
  if (stage.value === 'running') return
  if (!open && (stage.value === 'done' || stage.value === 'partial')) {
    signIn()
    return
  }
  if (!open && (stage.value === 'failed' || stage.value === 'lost')) emit('finished')
  emit('update:open', open)
}

onBeforeUnmount(stopPolling)
</script>

<template>
  <Dialog :open="open" @update:open="close">
    <DialogContent class="sm:max-w-lg" @interact-outside="(e: Event) => stage === 'running' && e.preventDefault()">
      <DialogHeader>
        <DialogTitle>{{ $t('backup.restoreDialog.title', { name: name ?? '' }) }}</DialogTitle>
        <DialogDescription v-if="stage === 'loading'">{{ $t('backup.restoreDialog.checking') }}</DialogDescription>
      </DialogHeader>

      <div v-if="stage === 'loading'" class="flex justify-center py-8"><Loader2 class="h-6 w-6 animate-spin text-white/40 light:text-gray-400" /></div>

      <div v-else-if="stage === 'confirm' && summary" class="space-y-4 text-sm">
        <div class="space-y-1 rounded-lg border border-white/[0.08] bg-white/[0.02] p-3 light:border-gray-200 light:bg-gray-50">
          <p class="font-medium">{{ $t('backup.restoreDialog.move', { from: summary.manifest.source_driver, to: summary.target_driver }) }}</p>
          <p class="text-white/60 light:text-gray-600">{{ $t('backup.restoreDialog.made', { date: formatDateTime(summary.manifest.created_at), version: summary.manifest.app_version || '?' }) }}</p>
          <p class="text-white/60 light:text-gray-600">{{ $t('backup.restoreDialog.rows', { rows: rows.toLocaleString(), tables: summary.manifest.tables.length }) }}</p>
          <p class="text-white/60 light:text-gray-600">
            {{ summary.manifest.include_files ? $t('backup.restoreDialog.files', { count: files.count, size: formatBytes(files.bytes) }) : $t('backup.restoreDialog.noFiles') }}
          </p>
        </div>
        <div v-if="!summary.key_matches" class="flex gap-2 rounded-lg border border-red-500/30 bg-red-500/10 p-3 text-red-300 light:text-red-700">
          <XCircle class="mt-0.5 h-4 w-4 shrink-0" />{{ $t('backup.restoreDialog.keyWarning') }}
        </div>
        <ul class="space-y-2 text-amber-300 light:text-amber-700">
          <li class="flex gap-2"><AlertTriangle class="mt-0.5 h-4 w-4 shrink-0" />{{ $t('backup.restoreDialog.replaceWarning') }}</li>
          <li class="flex gap-2"><AlertTriangle class="mt-0.5 h-4 w-4 shrink-0" />{{ $t('backup.restoreDialog.workersWarning') }}</li>
          <li class="flex gap-2"><AlertTriangle class="mt-0.5 h-4 w-4 shrink-0" />{{ $t('backup.restoreDialog.signOutWarning') }}</li>
        </ul>
        <div class="space-y-1.5">
          <label for="restore-confirm" class="text-white/70 light:text-gray-700">{{ $t('backup.restoreDialog.typeToConfirm', { word: RESTORE_WORD }) }}</label>
          <Input id="restore-confirm" v-model="typed" autocomplete="off" class="font-mono" data-testid="restore-confirm" />
        </div>
      </div>

      <div v-else-if="stage === 'running'" class="space-y-3 py-2 text-sm">
        <p class="flex items-center gap-2"><Loader2 class="h-4 w-4 animate-spin" />{{ $t('backup.restoreDialog.running') }}</p>
        <Progress :model-value="percent" class="h-2" />
        <p class="text-white/50 light:text-gray-500">{{ status ? $t(`backup.phase.${status.phase}`, status.phase) : '' }}<span v-if="status?.table"> · {{ status.table }}</span></p>
      </div>

      <div v-else-if="stage === 'done'" class="flex gap-3 py-2 text-sm">
        <CheckCircle2 class="h-5 w-5 shrink-0 text-emerald-400" />
        <div><p class="font-medium">{{ $t('backup.restoreDialog.doneTitle') }}</p><p class="text-white/60 light:text-gray-600">{{ $t('backup.restoreDialog.doneBody') }}</p></div>
      </div>

      <div v-else-if="stage === 'partial'" class="flex gap-3 py-2 text-sm" data-testid="restore-partial">
        <AlertTriangle class="h-5 w-5 shrink-0 text-amber-400" />
        <div class="space-y-1">
          <p class="font-medium">{{ $t('backup.restoreDialog.partialTitle') }}</p>
          <p class="text-white/60 light:text-gray-600">{{ $t('backup.restoreDialog.partialBody') }}</p>
        </div>
      </div>

      <div v-else-if="stage === 'lost'" class="flex gap-3 py-2 text-sm" data-testid="restore-lost">
        <XCircle class="h-5 w-5 shrink-0 text-red-400" />
        <div><p class="font-medium">{{ $t('backup.restoreDialog.lostTitle') }}</p><p class="text-white/60 light:text-gray-600">{{ $t('backup.restoreDialog.lostBody') }}</p></div>
      </div>

      <div v-else class="flex gap-3 py-2 text-sm">
        <XCircle class="h-5 w-5 shrink-0 text-red-400" />
        <div><p class="font-medium">{{ $t('backup.restoreDialog.failedTitle') }}</p><p class="break-words text-white/60 light:text-gray-600">{{ error }}</p></div>
      </div>

      <DialogFooter>
        <Button v-if="stage === 'confirm'" variant="destructive" :disabled="starting || !canConfirmRestore(typed)" data-testid="restore-start" @click="start">{{ $t('backup.restoreDialog.start') }}</Button>
        <Button v-else-if="stage === 'done' || stage === 'partial'" data-testid="restore-sign-in" @click="signIn">{{ $t('backup.restoreDialog.signIn') }}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
