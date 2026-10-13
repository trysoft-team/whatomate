<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Archive, Download, History, Loader2, RotateCcw, Trash2, Upload, XCircle } from 'lucide-vue-next'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Progress } from '@/components/ui/progress'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { PageHeader, DeleteConfirmDialog, IconButton } from '@/components/shared'
import { backupsService, type BackupInfo, type BackupJob } from '@/services/api'
import { formatBytes, jobPercent, lastRestoreFailure, uploadInChunks } from '@/lib/backup'
import { getErrorMessage } from '@/lib/api-utils'
import { formatDateTime } from '@/lib/utils'
import RestoreBackupDialog from './RestoreBackupDialog.vue'

const { t } = useI18n()
const backups = ref<BackupInfo[]>([])
const dir = ref('')
const job = ref<BackupJob | null>(null)
const loading = ref(true)
const includeFiles = ref(true)
const uploadPercent = ref<number | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)
const restoreName = ref<string | null>(null)
const restoreOpen = ref(false)
const deleteName = ref<string | null>(null)
const deleteOpen = ref(false)
const deleting = ref(false)
let timer: ReturnType<typeof setInterval> | null = null

const backupRunning = computed(() => !!job.value?.running && job.value.kind === 'backup')
const busy = computed(() => !!job.value?.running || uploadPercent.value !== null)
const restoreFailure = computed(() => lastRestoreFailure(job.value))

async function load() {
  try {
    const res = await backupsService.list()
    backups.value = res.data.data.backups
    dir.value = res.data.data.dir
    job.value = res.data.data.job
    if (job.value?.running) startPolling()
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedLoad', { resource: t('backup.listTitle') })))
  } finally {
    loading.value = false
  }
}

function startPolling() {
  if (timer) return
  timer = setInterval(async () => {
    try {
      const res = await backupsService.job()
      job.value = res.data.data
      if (!job.value.running) {
        clearInterval(timer!)
        timer = null
        if (job.value.kind === 'backup') {
          if (job.value.error) toast.error(t('backup.failed', { error: job.value.error }))
          else toast.success(t('backup.finished', { name: job.value.name ?? '' }))
        }
        await load()
      }
    } catch {
      // keep polling
    }
  }, 1000)
}

async function backUpNow() {
  try {
    const res = await backupsService.create(includeFiles.value)
    job.value = res.data.data
    toast.success(t('backup.started'))
    startPolling()
  } catch (e) {
    toast.error(getErrorMessage(e, t('backup.failed', { error: '' })))
  }
}

async function onFile(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  uploadPercent.value = 0
  try {
    await uploadInChunks(file, {
      start: async (name, size) => (await backupsService.startUpload(name, size)).data.data,
      put: async (id, offset, chunk) => (await backupsService.putChunk(id, offset, chunk)).data.data,
      finish: async (id) => (await backupsService.finishUpload(id)).data.data,
      cancel: (id) => backupsService.cancelUpload(id),
    }, (sent, total) => { uploadPercent.value = Math.round((sent / total) * 100) })
    toast.success(t('backup.uploaded'))
    await load()
  } catch (err) {
    toast.error(getErrorMessage(err, t('backup.failed', { error: '' })))
  } finally {
    uploadPercent.value = null
  }
}

async function confirmDelete() {
  if (!deleteName.value) return
  deleting.value = true
  try {
    await backupsService.remove(deleteName.value)
    deleteOpen.value = false
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedDelete', { resource: deleteName.value })))
  } finally {
    deleting.value = false
  }
}

function openRestore(name: string) {
  restoreName.value = name
  restoreOpen.value = true
}

function when(b: BackupInfo) {
  return b.manifest ? formatDateTime(b.manifest.created_at) : '—'
}

onMounted(load)
onBeforeUnmount(() => { if (timer) clearInterval(timer) })
</script>

<template>
  <div class="flex h-full flex-col bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader :title="$t('backup.title')" :description="$t('backup.subtitle')" :icon="Archive" icon-gradient="bg-gradient-to-br from-emerald-500 to-teal-600 shadow-emerald-500/20" back-link="/settings" />

    <ScrollArea class="flex-1">
      <div class="space-y-6 p-6">
        <Alert v-if="restoreFailure" variant="destructive" data-testid="last-restore-failed">
          <XCircle class="h-4 w-4" />
          <AlertTitle>{{ $t('backup.lastRestoreFailed') }}</AlertTitle>
          <AlertDescription class="space-y-1">
            <p class="break-words">{{ restoreFailure.error }}</p>
            <p v-if="restoreFailure.safety">{{ $t('backup.lastRestoreSafety', { name: restoreFailure.safety }) }}</p>
          </AlertDescription>
        </Alert>

        <Card>
          <CardHeader>
            <CardTitle>{{ $t('backup.createTitle') }}</CardTitle>
            <CardDescription>{{ $t('backup.createDesc') }}</CardDescription>
          </CardHeader>
          <CardContent class="space-y-4">
            <div v-if="backupRunning && job" class="space-y-2" data-testid="backup-progress">
              <div class="flex items-center gap-2 text-sm"><Loader2 class="h-4 w-4 animate-spin" />{{ $t(`backup.phase.${job.phase}`, job.phase) }}<span v-if="job.table" class="text-white/50 light:text-gray-500">· {{ job.table }}</span></div>
              <Progress :model-value="jobPercent(job)" class="h-2" />
            </div>
            <div v-else class="flex flex-wrap items-center justify-between gap-4">
              <label class="flex cursor-pointer items-center gap-2 text-sm text-white/70 light:text-gray-700">
                <Checkbox :checked="includeFiles" @update:checked="(v) => (includeFiles = v === true)" data-testid="backup-include-files" />
                {{ $t('backup.includeFiles') }}
              </label>
              <Button :disabled="busy" data-testid="backup-now" @click="backUpNow"><Archive class="mr-2 h-4 w-4" />{{ $t('backup.backUpNow') }}</Button>
            </div>
            <p v-if="dir" class="break-all font-mono text-xs text-white/40 light:text-gray-500">{{ $t('backup.savedIn', { dir }) }}</p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <div class="flex flex-wrap items-center justify-between gap-4">
              <div>
                <CardTitle>{{ $t('backup.listTitle') }}</CardTitle>
                <CardDescription>{{ $t('backup.listDesc') }}</CardDescription>
              </div>
              <div class="flex items-center gap-3">
                <span v-if="uploadPercent !== null" class="text-sm text-white/60 light:text-gray-600">{{ $t('backup.uploading', { percent: uploadPercent }) }}</span>
                <input ref="fileInput" type="file" accept=".zip,application/zip" class="hidden" @change="onFile" />
                <Button variant="outline" size="sm" :disabled="busy" data-testid="backup-upload" @click="fileInput?.click()"><Upload class="mr-2 h-4 w-4" />{{ $t('backup.upload') }}</Button>
              </div>
            </div>
          </CardHeader>
          <CardContent>
            <div v-if="loading" class="flex justify-center py-10"><Loader2 class="h-6 w-6 animate-spin text-white/40 light:text-gray-400" /></div>
            <p v-else-if="backups.length === 0" class="py-10 text-center text-sm text-white/40 light:text-gray-500">{{ $t('backup.empty') }}</p>
            <Table v-else>
              <TableHeader>
                <TableRow>
                  <TableHead>{{ $t('backup.colDate') }}</TableHead>
                  <TableHead>{{ $t('backup.colSize') }}</TableHead>
                  <TableHead>{{ $t('backup.colMadeOn') }}</TableHead>
                  <TableHead>{{ $t('backup.colFiles') }}</TableHead>
                  <TableHead class="text-right">{{ $t('common.actions') }}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow v-for="b in backups" :key="b.name" :data-testid="`backup-row-${b.name}`">
                  <TableCell>
                    <div class="font-medium">{{ when(b) }}</div>
                    <div class="flex items-center gap-1.5 font-mono text-xs text-white/40 light:text-gray-500">
                      {{ b.name }}
                      <span v-if="b.manifest?.kind === 'pre-restore'" class="inline-flex items-center gap-1 rounded bg-amber-500/10 px-1.5 py-0.5 font-sans text-[10px] font-medium uppercase text-amber-300 light:text-amber-700"><History class="h-3 w-3" />{{ $t('backup.preRestore') }}</span>
                    </div>
                    <div v-if="b.error" class="text-xs text-red-400">{{ $t('backup.unreadable', { error: b.error }) }}</div>
                  </TableCell>
                  <TableCell>{{ formatBytes(b.size) }}</TableCell>
                  <TableCell class="text-sm">{{ b.manifest ? `${b.manifest.app_version || '?'} · ${b.manifest.source_driver}` : '—' }}</TableCell>
                  <TableCell>{{ b.manifest?.include_files ? $t('backup.filesYes') : $t('backup.filesNo') }}</TableCell>
                  <TableCell>
                    <div class="flex items-center justify-end gap-1">
                      <a :href="backupsService.downloadUrl(b.name)" :download="b.name"><IconButton :icon="Download" :label="$t('backup.download')" class="h-8 w-8" /></a>
                      <IconButton v-if="b.manifest" :icon="RotateCcw" :label="$t('backup.restore')" class="h-8 w-8" :disabled="busy" @click="openRestore(b.name)" />
                      <IconButton :icon="Trash2" :label="$t('backup.delete')" variant="ghost" class="h-8 w-8 text-destructive" :disabled="busy" @click="deleteName = b.name; deleteOpen = true" />
                    </div>
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      </div>
    </ScrollArea>

    <RestoreBackupDialog v-model:open="restoreOpen" :name="restoreName" @finished="load" />
    <DeleteConfirmDialog v-model:open="deleteOpen" :title="$t('backup.deleteTitle')" :item-name="deleteName ?? ''" :description="$t('backup.deleteDesc')" :is-submitting="deleting" @confirm="confirmDelete" />
  </div>
</template>
