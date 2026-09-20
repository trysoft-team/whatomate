<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { PageHeader, CrudFormDialog, DeleteConfirmDialog } from '@/components/shared'
import { googleCredentialsService, type GoogleCredential } from '@/services/api'
import { toast } from 'vue-sonner'
import { Plus, Sheet, Trash2 } from 'lucide-vue-next'

const creds = ref<GoogleCredential[]>([])
const isLoading = ref(false)
const isDialogOpen = ref(false)
const isSubmitting = ref(false)
const deleteOpen = ref(false)
const toDelete = ref<GoogleCredential | null>(null)
const formLabel = ref('')
const formJSON = ref('')

async function load() {
  isLoading.value = true
  try {
    const res = await googleCredentialsService.list()
    creds.value = (res.data as any)?.data || (res.data as any) || []
  } catch {
    toast.error('Failed to load Google credentials')
  } finally {
    isLoading.value = false
  }
}

onMounted(load)

function openCreate() {
  formLabel.value = ''
  formJSON.value = ''
  isDialogOpen.value = true
}

async function save() {
  if (!formLabel.value.trim() || !formJSON.value.trim()) return
  isSubmitting.value = true
  try {
    await googleCredentialsService.create({ label: formLabel.value.trim(), service_json: formJSON.value.trim() })
    isDialogOpen.value = false
    toast.success('Credential saved')
    await load()
  } catch {
    toast.error('Failed to save credential (must be a service_account JSON)')
  } finally {
    isSubmitting.value = false
  }
}

async function confirmDelete() {
  if (!toDelete.value) return
  try {
    await googleCredentialsService.remove(toDelete.value.id)
    toast.success('Deleted')
    deleteOpen.value = false
    toDelete.value = null
    await load()
  } catch {
    toast.error('Failed to delete')
  }
}
</script>

<template>
  <div class="flex flex-col h-full">
    <PageHeader title="Google credentials" description="Service accounts for Sheets nodes (encrypted at rest)" :icon="Sheet" icon-gradient="bg-gradient-to-br from-yellow-500 to-amber-600 shadow-yellow-500/20">
      <template #actions>
        <Button variant="outline" size="sm" @click="openCreate"><Plus class="h-4 w-4 mr-2" />Add credential</Button>
      </template>
    </PageHeader>
    <ScrollArea class="flex-1">
      <div class="p-6 max-w-3xl mx-auto">
        <Card>
          <CardHeader>
            <CardTitle>Service accounts</CardTitle>
            <CardDescription>JSON keys are encrypted. Share the spreadsheet with the client_email.</CardDescription>
          </CardHeader>
          <CardContent>
            <div v-if="isLoading" class="text-sm text-muted-foreground">Loading…</div>
            <div v-else-if="creds.length === 0" class="text-sm text-muted-foreground">No credentials yet.</div>
            <ul v-else class="space-y-2">
              <li v-for="c in creds" :key="c.id" class="flex items-center justify-between border rounded-md px-3 py-2">
                <div>
                  <p class="font-medium">{{ c.label }}</p>
                  <p class="text-xs text-muted-foreground">{{ c.client_email }}</p>
                </div>
                <Button variant="ghost" size="icon" class="h-8 w-8" @click="toDelete = c; deleteOpen = true">
                  <Trash2 class="h-4 w-4" />
                </Button>
              </li>
            </ul>
          </CardContent>
        </Card>
      </div>
    </ScrollArea>

    <CrudFormDialog v-model:open="isDialogOpen" create-title="Add Google credential" :is-submitting="isSubmitting" @submit="save">
      <div class="space-y-3">
        <div class="space-y-1.5">
          <Label>Label</Label>
          <Input v-model="formLabel" placeholder="Production Sheets" />
        </div>
        <div class="space-y-1.5">
          <Label>Service account JSON</Label>
          <Textarea v-model="formJSON" class="min-h-[140px] font-mono text-xs" placeholder='{"type":"service_account",...}' />
        </div>
      </div>
    </CrudFormDialog>

    <DeleteConfirmDialog
      v-model:open="deleteOpen"
      :title="`Delete ${toDelete?.label || 'credential'}?`"
      description="Flows referencing this credential will fail until updated."
      @confirm="confirmDelete"
    />
  </div>
</template>
