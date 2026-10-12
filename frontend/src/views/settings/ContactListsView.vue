<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { PageHeader, CrudFormDialog, DeleteConfirmDialog } from '@/components/shared'
import { contactListsService, type ContactList } from '@/services/api'
import { toast } from 'vue-sonner'
import { Plus, BookUser, Trash2 } from 'lucide-vue-next'

const lists = ref<ContactList[]>([])
const isLoading = ref(false)
const isDialogOpen = ref(false)
const isSubmitting = ref(false)
const deleteOpen = ref(false)
const toDelete = ref<ContactList | null>(null)
const formName = ref('')
const formDescription = ref('')

async function load() {
  isLoading.value = true
  try {
    const res = await contactListsService.list()
    lists.value = (res.data as any)?.data || (res.data as any) || []
  } catch {
    toast.error('Failed to load contact lists')
  } finally {
    isLoading.value = false
  }
}

onMounted(load)

function openCreate() {
  formName.value = ''
  formDescription.value = ''
  isDialogOpen.value = true
}

async function save() {
  if (!formName.value.trim()) return
  isSubmitting.value = true
  try {
    await contactListsService.create({ name: formName.value.trim(), description: formDescription.value })
    isDialogOpen.value = false
    toast.success('List created')
    await load()
  } catch {
    toast.error('Failed to create list')
  } finally {
    isSubmitting.value = false
  }
}

async function confirmDelete() {
  if (!toDelete.value) return
  try {
    await contactListsService.remove(toDelete.value.id)
    toast.success('List deleted')
    deleteOpen.value = false
    toDelete.value = null
    await load()
  } catch {
    toast.error('Failed to delete list')
  }
}
</script>

<template>
  <div class="flex flex-col h-full">
    <PageHeader title="Contact lists" description="Phonebooks for chatbot Phonebook nodes" :icon="BookUser" icon-gradient="bg-gradient-to-br from-lime-500 to-green-600 shadow-lime-500/20">
      <template #actions>
        <Button variant="outline" size="sm" @click="openCreate"><Plus class="h-4 w-4 mr-2" />Add list</Button>
      </template>
    </PageHeader>
    <ScrollArea class="flex-1">
      <div class="p-6 max-w-3xl mx-auto">
        <Card>
          <CardHeader>
            <CardTitle>Lists</CardTitle>
            <CardDescription>Add or remove contacts mid-flow via the Phonebook node.</CardDescription>
          </CardHeader>
          <CardContent>
            <div v-if="isLoading" class="text-sm text-muted-foreground">Loading…</div>
            <div v-else-if="lists.length === 0" class="text-sm text-muted-foreground">No lists yet.</div>
            <ul v-else class="space-y-2">
              <li v-for="l in lists" :key="l.id" class="flex items-center justify-between border rounded-md px-3 py-2">
                <div>
                  <p class="font-medium">{{ l.name }}</p>
                  <p class="text-xs text-muted-foreground">{{ l.member_count || 0 }} members</p>
                </div>
                <Button variant="ghost" size="icon" class="h-8 w-8" @click="toDelete = l; deleteOpen = true">
                  <Trash2 class="h-4 w-4" />
                </Button>
              </li>
            </ul>
          </CardContent>
        </Card>
      </div>
    </ScrollArea>

    <CrudFormDialog v-model:open="isDialogOpen" create-title="New contact list" :is-submitting="isSubmitting" @submit="save">
      <div class="space-y-3">
        <div class="space-y-1.5">
          <Label>Name</Label>
          <Input v-model="formName" />
        </div>
        <div class="space-y-1.5">
          <Label>Description</Label>
          <Input v-model="formDescription" />
        </div>
      </div>
    </CrudFormDialog>

    <DeleteConfirmDialog
      v-model:open="deleteOpen"
      :title="`Delete ${toDelete?.name || 'list'}?`"
      description="Members will be removed from this list."
      @confirm="confirmDelete"
    />
  </div>
</template>
