<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { PageHeader, SearchInput, DataTable, CrudFormDialog, DeleteConfirmDialog, IconButton, ErrorState, type Column } from '@/components/shared'
import { organizationsService, type Organization } from '@/services/api'
import { useOrganizationsStore } from '@/stores/organizations'
import { useAuthStore } from '@/stores/auth'
import { useCrudState } from '@/composables/useCrudState'
import { toast } from 'vue-sonner'
import { Plus, Building2, Pencil, Trash2 } from 'lucide-vue-next'
import { getErrorMessage, unwrapListResponse } from '@/lib/api-utils'
import { formatDate } from '@/lib/utils'

const { t } = useI18n()
const authStore = useAuthStore()
const organizationsStore = useOrganizationsStore()

interface OrgFormData {
  name: string
}

const defaultFormData: OrgFormData = { name: '' }

const organizations = ref<Organization[]>([])
const isLoading = ref(false)
const isDeleting = ref(false)
const error = ref(false)
const searchQuery = ref('')
const {
  isSubmitting, isDialogOpen, editingItem: editingOrg, deleteDialogOpen, itemToDelete: orgToDelete,
  formData, openCreateDialog, openEditDialog: baseOpenEditDialog, openDeleteDialog, closeDialog, closeDeleteDialog,
} = useCrudState<Organization, OrgFormData>(defaultFormData)

const isSuperAdmin = computed(() => authStore.user?.is_super_admin || false)
const canWrite = computed(() => isSuperAdmin.value)
const canDelete = computed(() => isSuperAdmin.value)
const currentOrgId = computed(() => organizationsStore.selectedOrgId || authStore.organizationId)

const sortKey = ref('name')
const sortDirection = ref<'asc' | 'desc'>('asc')

const columns = computed<Column<Organization>[]>(() => [
  { key: 'name', label: t('organizations.name'), sortable: true },
  { key: 'slug', label: t('organizations.slug'), sortable: true },
  { key: 'member_count', label: t('organizations.members'), sortable: true },
  { key: 'created_at', label: t('organizations.createdAt'), sortable: true },
  { key: 'actions', label: t('common.actions'), align: 'right' },
])

const filteredOrganizations = computed(() => {
  const q = searchQuery.value.trim().toLowerCase()
  if (!q) return organizations.value
  return organizations.value.filter((org) =>
    org.name.toLowerCase().includes(q) || (org.slug || '').toLowerCase().includes(q)
  )
})

function openEditDialog(org: Organization) {
  baseOpenEditDialog(org, (item) => ({ name: item.name }))
}

function isCurrentOrg(org: Organization) {
  return org.id === currentOrgId.value
}

function canDeleteOrg(org: Organization) {
  return canDelete.value && !isCurrentOrg(org) && organizations.value.length > 1
}

function deleteLabel(org: Organization) {
  if (isCurrentOrg(org)) return t('organizations.deleteCurrentWarning')
  if (organizations.value.length <= 1) return t('organizations.deleteLastWarning')
  return t('organizations.deleteOrganization')
}

async function refreshSwitcher() {
  await Promise.all([
    organizationsStore.fetchOrganizations(),
    organizationsStore.fetchMyOrganizations(),
  ])
}

async function fetchOrganizations() {
  isLoading.value = true
  error.value = false
  try {
    const response = await organizationsService.list()
    organizations.value = unwrapListResponse<Organization>(response, 'organizations')
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedLoad', { resource: t('resources.organizations') })))
    error.value = true
  } finally {
    isLoading.value = false
  }
}

onMounted(() => fetchOrganizations())

async function saveOrganization() {
  if (!formData.value.name.trim()) {
    toast.error(t('organizations.nameRequired'))
    return
  }
  isSubmitting.value = true
  try {
    const payload = { name: formData.value.name.trim() }
    if (editingOrg.value) {
      await organizationsService.update(editingOrg.value.id, payload)
      toast.success(t('common.updatedSuccess', { resource: t('resources.Organization') }))
    } else {
      await organizationsService.create(payload)
      toast.success(t('common.createdSuccess', { resource: t('resources.Organization') }))
    }
    closeDialog()
    await fetchOrganizations()
    await refreshSwitcher()
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedSave', { resource: t('resources.organization') })))
  } finally {
    isSubmitting.value = false
  }
}

async function confirmDelete() {
  if (!orgToDelete.value) return
  isDeleting.value = true
  try {
    await organizationsService.delete(orgToDelete.value.id)
    toast.success(t('common.deletedSuccess', { resource: t('resources.Organization') }))
    closeDeleteDialog()
    await fetchOrganizations()
    await refreshSwitcher()
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedDelete', { resource: t('resources.organization') })))
  } finally {
    isDeleting.value = false
  }
}
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader :title="$t('organizations.title')" :subtitle="$t('organizations.subtitle')" :icon="Building2" icon-gradient="bg-gradient-to-br from-sky-500 to-indigo-600 shadow-sky-500/20" back-link="/settings">
      <template #actions>
        <Button v-if="canWrite" variant="outline" size="sm" @click="openCreateDialog"><Plus class="h-4 w-4 mr-2" />{{ $t('organizations.addOrganization') }}</Button>
      </template>
    </PageHeader>

    <ErrorState
      v-if="error && !isLoading"
      :title="$t('common.loadErrorTitle')"
      :description="$t('common.loadErrorDescription')"
      :retry-label="$t('common.retryLoad')"
      class="flex-1"
      @retry="fetchOrganizations"
    />

    <ScrollArea v-else class="flex-1">
      <div class="p-6">
        <div class="max-w-6xl mx-auto">
          <Card>
            <CardHeader>
              <div class="flex items-center justify-between flex-wrap gap-4">
                <div>
                  <CardTitle>{{ $t('organizations.organizationList') }}</CardTitle>
                  <CardDescription>{{ $t('organizations.organizationListDesc') }}</CardDescription>
                </div>
                <SearchInput v-model="searchQuery" :placeholder="$t('organizations.search') + '...'" class="w-64" />
              </div>
            </CardHeader>
            <CardContent>
              <DataTable
                :items="filteredOrganizations"
                :columns="columns"
                :is-loading="isLoading"
                :empty-icon="Building2"
                :empty-title="searchQuery ? $t('organizations.noMatching') : $t('organizations.noOrgsYet')"
                :empty-description="searchQuery ? $t('organizations.noMatchingDesc') : $t('organizations.noOrgsYetDesc')"
                v-model:sort-key="sortKey"
                v-model:sort-direction="sortDirection"
                item-name="organizations"
              >
                <template #cell-name="{ item: org }">
                  <div class="flex items-center gap-2">
                    <span class="font-medium">{{ org.name }}</span>
                    <Badge v-if="isCurrentOrg(org)" variant="secondary">{{ $t('organizations.current') }}</Badge>
                  </div>
                </template>
                <template #cell-slug="{ item: org }">
                  <span class="text-muted-foreground font-mono text-sm">{{ org.slug || '—' }}</span>
                </template>
                <template #cell-member_count="{ item: org }">
                  <span class="text-muted-foreground">{{ org.member_count ?? 0 }}</span>
                </template>
                <template #cell-created_at="{ item: org }">
                  <span class="text-muted-foreground">{{ formatDate(org.created_at) }}</span>
                </template>
                <template #cell-actions="{ item: org }">
                  <div class="flex items-center justify-end gap-1">
                    <IconButton v-if="canWrite" :icon="Pencil" :label="$t('organizations.editOrganization')" class="h-8 w-8" @click="openEditDialog(org)" />
                    <IconButton
                      v-if="canDelete"
                      :label="deleteLabel(org)"
                      class="h-8 w-8"
                      :disabled="!canDeleteOrg(org)"
                      @click="openDeleteDialog(org)"
                    >
                      <Trash2 class="h-4 w-4 text-destructive" />
                    </IconButton>
                  </div>
                </template>
                <template #empty-action>
                  <Button v-if="canWrite" variant="outline" size="sm" @click="openCreateDialog">
                    <Plus class="h-4 w-4 mr-2" />
                    {{ $t('organizations.addOrganization') }}
                  </Button>
                </template>
              </DataTable>
            </CardContent>
          </Card>
        </div>
      </div>
    </ScrollArea>

    <CrudFormDialog
      v-model:open="isDialogOpen"
      :is-editing="!!editingOrg"
      :is-submitting="isSubmitting"
      :edit-title="$t('organizations.editTitle')"
      :create-title="$t('organizations.createTitle')"
      :edit-description="$t('organizations.editDesc')"
      :create-description="$t('organizations.createDesc')"
      max-width="max-w-md"
      @submit="saveOrganization"
    >
      <div class="space-y-4">
        <div class="space-y-2">
          <Label>{{ $t('organizations.name') }} <span class="text-destructive">*</span></Label>
          <Input v-model="formData.name" :placeholder="$t('organizations.namePlaceholder')" maxlength="100" />
          <p class="text-xs text-muted-foreground">{{ $t('organizations.maxCharacters') }}</p>
        </div>
      </div>
    </CrudFormDialog>

    <DeleteConfirmDialog v-model:open="deleteDialogOpen" :title="$t('organizations.deleteOrganization')" :item-name="orgToDelete?.name" :description="$t('organizations.deleteWarning')" :is-submitting="isDeleting" @confirm="confirmDelete" />
  </div>
</template>
