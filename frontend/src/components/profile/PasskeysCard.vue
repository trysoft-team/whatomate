<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { DeleteConfirmDialog } from '@/components/shared'
import { toast } from 'vue-sonner'
import { KeyRound, Loader2, Plus, Trash2, Fingerprint } from 'lucide-vue-next'
import { passkeysService, type Passkey } from '@/services/api'
import { createPasskey, defaultPasskeyName, isPasskeyCancelled, isPasskeySignalSupported, isPasskeySupported } from '@/lib/passkeys'
import { useAuthStore } from '@/stores/auth'
import { getErrorMessage } from '@/lib/api-utils'
import { formatDateTime } from '@/lib/utils'

const { t } = useI18n()

const authStore = useAuthStore()
const supported = isPasskeySupported()
const signalSupported = isPasskeySignalSupported()
const passkeys = ref<Passkey[]>([])
const isLoading = ref(false)
const isAdding = ref(false)
const showNameInput = ref(false)
const newName = ref('')
const deleteOpen = ref(false)
const toDelete = ref<Passkey | null>(null)

async function load() {
  isLoading.value = true
  try {
    const res = await passkeysService.list()
    passkeys.value = (res.data as any)?.data || []
    void authStore.syncBrowserPasskeys(passkeys.value)
  } catch {
    passkeys.value = []
  } finally {
    isLoading.value = false
  }
}

onMounted(load)

function startAdd() {
  newName.value = defaultPasskeyName()
  showNameInput.value = true
}

async function addPasskey() {
  isAdding.value = true
  try {
    const begin = await passkeysService.beginRegistration()
    const credential = await createPasskey((begin.data as any).data)
    await passkeysService.finishRegistration(newName.value.trim(), credential)
    toast.success(t('passkeys.added'))
    showNameInput.value = false
    await load()
  } catch (err) {
    if (isPasskeyCancelled(err)) {
      toast.info(t('passkeys.cancelled'))
    } else if ((err as { name?: string })?.name === 'InvalidStateError') {
      toast.error(t('passkeys.alreadyRegistered'))
    } else {
      toast.error(getErrorMessage(err, t('passkeys.addFailed')))
    }
  } finally {
    isAdding.value = false
  }
}

async function confirmDelete() {
  if (!toDelete.value) return
  try {
    await passkeysService.remove(toDelete.value.id)
    toast.success(t('passkeys.removed'), {
      description: toDelete.value.current_site && signalSupported
        ? t('passkeys.removedSignalled')
        : t('passkeys.removedManual'),
    })
    deleteOpen.value = false
    toDelete.value = null
    await load()
  } catch (err) {
    toast.error(getErrorMessage(err, t('passkeys.removeFailed')))
  }
}
</script>

<template>
  <Card data-testid="passkeys-card">
    <CardHeader>
      <CardTitle>{{ $t('passkeys.title') }}</CardTitle>
      <CardDescription>{{ $t('passkeys.description') }}</CardDescription>
    </CardHeader>
    <CardContent class="space-y-4">
      <p v-if="!supported" class="text-sm text-muted-foreground">{{ $t('passkeys.unsupported') }}</p>

      <div v-if="isLoading" class="flex justify-center py-2">
        <Loader2 class="h-5 w-5 animate-spin text-muted-foreground" />
      </div>
      <p v-else-if="passkeys.length === 0" class="text-sm text-muted-foreground">{{ $t('passkeys.empty') }}</p>
      <ul v-else class="space-y-2">
        <li
          v-for="pk in passkeys"
          :key="pk.id"
          class="flex items-center justify-between gap-3 rounded-md border px-3 py-2"
        >
          <div class="flex items-center gap-3 min-w-0">
            <KeyRound class="h-4 w-4 text-muted-foreground shrink-0" />
            <div class="min-w-0">
              <div class="flex items-center gap-2">
                <p class="font-medium truncate">{{ pk.name }}</p>
                <Badge v-if="!pk.current_site" variant="outline" class="text-[11px] shrink-0" :title="$t('passkeys.otherSiteHint')">
                  {{ pk.rp_id }}
                </Badge>
              </div>
              <p class="text-xs text-muted-foreground">
                {{ $t('passkeys.addedOn', { date: formatDateTime(pk.created_at) }) }}
                <template v-if="pk.last_used_at"> · {{ $t('passkeys.lastUsed', { date: formatDateTime(pk.last_used_at) }) }}</template>
              </p>
            </div>
          </div>
          <Button variant="ghost" size="icon" class="h-8 w-8 shrink-0" :aria-label="$t('passkeys.remove')" @click="toDelete = pk; deleteOpen = true">
            <Trash2 class="h-4 w-4" />
          </Button>
        </li>
      </ul>

      <div v-if="supported" class="flex justify-end">
        <div v-if="showNameInput" class="w-full space-y-2">
          <Label for="passkey-name">{{ $t('passkeys.nameLabel') }}</Label>
          <div class="flex gap-2">
            <Input
              id="passkey-name"
              v-model="newName"
              maxlength="100"
              :disabled="isAdding"
              @keydown.enter.prevent="addPasskey"
            />
            <Button variant="ghost" :disabled="isAdding" @click="showNameInput = false">{{ $t('common.cancel') }}</Button>
            <Button :disabled="isAdding" data-testid="passkey-create" @click="addPasskey">
              <Loader2 v-if="isAdding" class="mr-2 h-4 w-4 animate-spin" />
              <Fingerprint v-else class="mr-2 h-4 w-4" />
              {{ $t('passkeys.create') }}
            </Button>
          </div>
        </div>
        <Button v-else variant="outline" size="sm" data-testid="passkey-add" @click="startAdd">
          <Plus class="mr-2 h-4 w-4" />
          {{ $t('passkeys.add') }}
        </Button>
      </div>
    </CardContent>
  </Card>

  <DeleteConfirmDialog
    v-model:open="deleteOpen"
    :title="$t('passkeys.removeTitle', { name: toDelete?.name || '' })"
    :description="$t('passkeys.removeDesc') + ' ' + (toDelete?.current_site && signalSupported ? $t('passkeys.removeDescSignal') : $t('passkeys.removeDescManual'))"
    :confirm-label="$t('passkeys.remove')"
    @confirm="confirmDelete"
  />
</template>
