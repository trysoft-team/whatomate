<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/services/api'
import { toast } from 'vue-sonner'
import { CrudFormDialog } from '@/components/shared'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Loader2,
  Globe,
  Mail,
  MapPin,
  Image as ImageIcon,
  AlertTriangle,
  Pencil,
  BadgeCheck,
  ExternalLink
} from 'lucide-vue-next'

interface Props {
  open: boolean
  accountId: string | null
  accountName: string
}

const props = defineProps<Props>()
const emit = defineEmits(['update:open'])

const { t } = useI18n()

const dialogOpen = computed({
  get: () => props.open,
  set: (value) => emit('update:open', value)
})

interface BusinessProfile {
  messaging_product: string
  address: string
  description: string
  vertical: string
  email: string
  websites: string[]
  profile_picture_url: string
  about: string
}

const isLoading = ref(false)
const isSubmitting = ref(false)
const profile = ref<BusinessProfile>({
  messaging_product: 'whatsapp',
  address: '',
  description: '',
  vertical: '',
  email: '',
  websites: ['', ''],
  profile_picture_url: '',
  about: ''
})

// Categories (Verticals) supported by Meta
const verticals = [
  { value: 'ALCOHOL', label: 'Alcohol' },
  { value: 'APPAREL', label: 'Apparel' },
  { value: 'AUTO', label: 'Automotive' },
  { value: 'BEAUTY', label: 'Beauty & Personal Care' },
  { value: 'EDU', label: 'Education' },
  { value: 'ENTERTAIN', label: 'Entertainment' },
  { value: 'EVENT_PLAN', label: 'Event Planning' },
  { value: 'FINANCE', label: 'Finance & Banking' },
  { value: 'GOVT', label: 'Government & Public Service' },
  { value: 'GROCERY', label: 'Grocery' },
  { value: 'HEALTH', label: 'Health & Wellness' },
  { value: 'HOTEL', label: 'Hotel & Lodging' },
  { value: 'NONPROFIT', label: 'Non-profit' },
  { value: 'ONLINE_GAMBLING', label: 'Online Gambling' },
  { value: 'OTC_DRUGS', label: 'Over-the-counter Drugs' },
  { value: 'OTHER', label: 'Other/Not Listed' },
  { value: 'PHYSICAL_GAMBLING', label: 'Physical Gambling' },
  { value: 'PROF_SERVICES', label: 'Professional Services' },
  { value: 'RETAIL', label: 'Retail' },
  { value: 'TRAVEL', label: 'Travel & Transportation' }
]

const selectedVerticalLabel = computed(() => {
  const found = verticals.find(v => v.value === profile.value.vertical)
  return found?.label || ''
})

interface DisplayNameInfo {
  verified_name: string
  name_status: string
  new_display_name: string
  new_name_status: string
  can_reregister: boolean
  has_stored_pin: boolean
}

const displayName = ref<DisplayNameInfo | null>(null)
const displayNameLoading = ref(false)
const displayNameError = ref(false)
const newDisplayName = ref('')
const confirmingDisplayName = ref(false)
const isRequestingDisplayName = ref(false)
const reregisterPin = ref('')
const isApplyingDisplayName = ref(false)

const hasPendingDisplayName = computed(() =>
  !!displayName.value?.new_display_name &&
  !!displayName.value.new_name_status &&
  displayName.value.new_name_status !== 'NONE'
)

const trimmedNewDisplayName = computed(() => newDisplayName.value.trim())

function displayNameStatusLabel(status: string) {
  const key = `businessProfile.displayNameStatus.${status}`
  const label = t(key)
  return label === key ? status : label
}

function displayNameStatusClass(status: string) {
  switch (status) {
    case 'APPROVED':
    case 'AVAILABLE_WITHOUT_REVIEW':
      return 'border-emerald-500/40 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400'
    case 'PENDING_REVIEW':
      return 'border-amber-500/40 bg-amber-500/10 text-amber-600 dark:text-amber-400'
    case 'DECLINED':
    case 'EXPIRED':
      return 'border-destructive/40 bg-destructive/10 text-destructive'
    default:
      return 'text-muted-foreground'
  }
}

async function fetchDisplayName() {
  if (!props.accountId) return
  displayNameLoading.value = true
  displayNameError.value = false
  try {
    const response = await api.get(`/accounts/${props.accountId}/display_name`)
    displayName.value = response.data.data
  } catch (error) {
    console.error('Failed to fetch display name:', error)
    displayName.value = null
    displayNameError.value = true
  } finally {
    displayNameLoading.value = false
  }
}

function startDisplayNameRequest() {
  const name = trimmedNewDisplayName.value
  if (!name) return
  if (name === displayName.value?.verified_name) {
    toast.error(t('businessProfile.displayNameSame'))
    return
  }
  confirmingDisplayName.value = true
}

async function submitDisplayNameRequest() {
  if (!props.accountId || !trimmedNewDisplayName.value) return
  isRequestingDisplayName.value = true
  try {
    const response = await api.post(`/accounts/${props.accountId}/display_name`, {
      display_name: trimmedNewDisplayName.value
    })
    const data = response.data.data
    displayName.value = displayName.value ? { ...displayName.value, ...data } : data
    newDisplayName.value = ''
    confirmingDisplayName.value = false
    toast.success(t('businessProfile.displayNameSubmitted'))
  } catch (error: any) {
    toast.error(error.response?.data?.message || t('common.failedUpdate', { resource: t('businessProfile.displayName') }))
  } finally {
    isRequestingDisplayName.value = false
  }
}

async function applyDisplayName() {
  if (!props.accountId) return
  isApplyingDisplayName.value = true
  try {
    await api.post(`/accounts/${props.accountId}/display_name/apply`, {
      pin: reregisterPin.value.trim()
    })
    reregisterPin.value = ''
    toast.success(t('businessProfile.displayNameApplied'))
    await fetchDisplayName()
  } catch (error: any) {
    toast.error(error.response?.data?.message || t('common.failedUpdate', { resource: t('businessProfile.displayName') }))
  } finally {
    isApplyingDisplayName.value = false
  }
}

watch(() => props.open, async (isOpen) => {
  if (isOpen && props.accountId) {
    newDisplayName.value = ''
    reregisterPin.value = ''
    confirmingDisplayName.value = false
    await Promise.all([fetchProfile(), fetchDisplayName()])
  }
})

async function fetchProfile() {
  if (!props.accountId) return

  isLoading.value = true
  try {
    const response = await api.get(`/accounts/${props.accountId}/business_profile`)
    const data = response.data.data

    // Fill the form, ensure arrays have data
    profile.value = {
      messaging_product: data.messaging_product || 'whatsapp',
      address: data.address || '',
      description: data.description || '',
      vertical: data.vertical || '',
      email: data.email || '',
      websites: data.websites && data.websites.length > 0 ? [...data.websites, ''] : ['', ''],
      profile_picture_url: data.profile_picture_url || '',
      about: data.about || ''
    }

    // Ensure at least two slots for websites
    if (profile.value.websites.length < 2) {
      profile.value.websites.push('')
    }
    // Trim to max 2
    profile.value.websites = profile.value.websites.slice(0, 2)

  } catch (error: any) {
    console.error('Failed to fetch business profile:', error)
    toast.error(t('common.failedLoad', { resource: t('resources.businessProfile') }))
  } finally {
    isLoading.value = false
  }
}

async function saveProfile() {
  if (!props.accountId) return

  isSubmitting.value = true
  try {
    // Filter out empty websites
    const websites = profile.value.websites.filter(w => w.trim() !== '')

    const payload = {
      messaging_product: 'whatsapp',
      address: profile.value.address,
      description: profile.value.description,
      vertical: profile.value.vertical,
      email: profile.value.email,
      websites: websites,
      about: profile.value.about
    }

    await api.put(`/accounts/${props.accountId}/business_profile`, payload)
    toast.success(t('common.updatedSuccess', { resource: t('resources.BusinessProfile') }))
    emit('update:open', false)
  } catch (error: any) {
    console.error('Failed to update profile:', error)
    const message = error.response?.data?.message || t('common.failedUpdate', { resource: t('resources.businessProfile') })
    toast.error(message)
  } finally {
    isSubmitting.value = false
  }
}

const fileInput = ref<HTMLInputElement | null>(null)
const isUploading = ref(false)

function triggerFileInput() {
  fileInput.value?.click()
}

async function handleFileChange(event: Event) {
  const input = event.target as HTMLInputElement
  if (!input.files || input.files.length === 0) return

  const file = input.files[0]
  // Validate basic type
  if (!file.type.startsWith('image/')) {
    toast.error(t('businessProfile.selectImage'))
    return
  }

  // Validate size (Meta limit is usually 5MB for profile generic, strict on square)
  if (file.size > 5 * 1024 * 1024) {
    toast.error(t('businessProfile.imageTooLarge'))
    return
  }

  isUploading.value = true
  const formData = new FormData()
  formData.append('file', file)

  try {
    await api.post(`/accounts/${props.accountId}/business_profile/photo`, formData, {
      headers: {
        'Content-Type': 'multipart/form-data'
      }
    })
    toast.success(t('common.updatedSuccess', { resource: t('resources.ProfilePhoto') }))
    // Refresh
    await fetchProfile()
  } catch (error: any) {
    console.error('Failed to upload photo:', error)
    toast.error(error.response?.data?.message || t('common.failedUpload', { resource: t('resources.profilePhoto') }))
  } finally {
    isUploading.value = false
    // Reset input
    if (fileInput.value) fileInput.value.value = ''
  }
}
</script>

<template>
  <CrudFormDialog
    v-model:open="dialogOpen"
    :is-editing="true"
    :is-submitting="isSubmitting || isLoading"
    :title="`${$t('businessProfile.title')}: ${accountName}`"
    :description="$t('businessProfile.description')"
    :submit-label="$t('businessProfile.saveChanges')"
    max-width="max-w-2xl"
    @submit="saveProfile"
  >
    <div v-if="isLoading" class="py-12 flex justify-center">
      <Loader2 class="h-8 w-8 animate-spin text-muted-foreground" />
    </div>

    <div v-else class="space-y-6">
      <Alert variant="warning">
        <AlertTriangle class="h-4 w-4" />
        <AlertTitle>{{ $t('businessProfile.profileUpdates') }}</AlertTitle>
        <AlertDescription>
          {{ $t('businessProfile.profileUpdatesDesc') }}
        </AlertDescription>
      </Alert>

      <div class="grid gap-6 md:grid-cols-2">
        <!-- Profile Picture Preview -->
        <div class="md:col-span-2 flex items-center gap-4">
          <div
            class="group relative h-24 w-24 rounded-full bg-secondary flex items-center justify-center overflow-hidden border border-border cursor-pointer transition-all hover:ring-2 hover:ring-emerald-500 hover:ring-offset-2 hover:ring-offset-background"
            @click="triggerFileInput"
          >
            <!-- Loading Overlay -->
            <div v-if="isUploading" class="absolute inset-0 bg-black/50 flex items-center justify-center z-10">
              <Loader2 class="h-6 w-6 text-white animate-spin" />
            </div>

            <!-- Hover Overlay -->
            <div v-if="!isUploading" class="absolute inset-0 bg-black/40 flex items-center justify-center opacity-0 group-hover:opacity-100 transition-opacity z-10">
              <Pencil class="h-6 w-6 text-white" />
            </div>

            <img v-if="profile.profile_picture_url" :src="profile.profile_picture_url" alt="Profile" class="h-full w-full object-cover" />
            <ImageIcon v-else class="h-10 w-10 text-muted-foreground" />

            <input
              ref="fileInput"
              type="file"
              accept="image/png, image/jpeg"
              class="hidden"
              @change="handleFileChange"
            />
          </div>
          <div class="flex-1">
            <Label>{{ $t('businessProfile.profilePicture') }}</Label>
            <p class="text-xs text-muted-foreground mt-1">
              {{ $t('businessProfile.profilePictureHint') }}
            </p>
          </div>
        </div>

        <!-- Display Name -->
        <div class="md:col-span-2 space-y-3 rounded-lg border border-border p-4" data-testid="display-name-section">
          <div class="flex items-start justify-between gap-3">
            <div>
              <Label>{{ $t('businessProfile.displayName') }}</Label>
              <p class="text-xs text-muted-foreground mt-1">
                {{ $t('businessProfile.displayNameHint') }}
                <a
                  href="https://www.facebook.com/business/help/757569725593362"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="inline-flex items-center gap-0.5 underline underline-offset-2 hover:text-foreground"
                >
                  {{ $t('businessProfile.displayNameGuidelines') }}
                  <ExternalLink class="h-3 w-3" />
                </a>
              </p>
            </div>
            <Loader2 v-if="displayNameLoading" class="h-4 w-4 animate-spin text-muted-foreground shrink-0" />
          </div>

          <p v-if="displayNameError" class="text-xs text-destructive">
            {{ $t('businessProfile.displayNameLoadFailed') }}
          </p>

          <template v-if="displayName">
            <div class="flex flex-wrap items-center gap-2 text-sm">
              <span class="text-muted-foreground">{{ $t('businessProfile.displayNameCurrent') }}:</span>
              <span class="font-medium" data-testid="display-name-current">{{ displayName.verified_name || '—' }}</span>
              <Badge v-if="displayName.name_status" variant="outline" :class="displayNameStatusClass(displayName.name_status)">
                {{ displayNameStatusLabel(displayName.name_status) }}
              </Badge>
            </div>

            <div v-if="hasPendingDisplayName" class="space-y-2" data-testid="display-name-pending">
              <div class="flex flex-wrap items-center gap-2 text-sm">
                <span class="text-muted-foreground">{{ $t('businessProfile.displayNameRequested') }}:</span>
                <span class="font-medium">{{ displayName.new_display_name }}</span>
                <Badge variant="outline" :class="displayNameStatusClass(displayName.new_name_status)">
                  {{ displayNameStatusLabel(displayName.new_name_status) }}
                </Badge>
              </div>

              <p v-if="displayName.new_name_status === 'DECLINED'" class="text-xs text-destructive">
                {{ $t('businessProfile.displayNameDeclined') }}
              </p>

              <template v-if="displayName.new_name_status === 'APPROVED'">
                <div v-if="displayName.can_reregister" class="rounded-md bg-emerald-500/5 border border-emerald-500/30 p-3 space-y-2">
                  <p class="text-xs flex items-start gap-1.5">
                    <BadgeCheck class="h-4 w-4 text-emerald-600 shrink-0" />
                    {{ $t('businessProfile.displayNameApproved') }}
                  </p>
                  <div class="flex flex-wrap items-end gap-2">
                    <div class="space-y-1">
                      <Label for="reregister-pin" class="text-xs">{{ $t('businessProfile.displayNamePin') }}</Label>
                      <Input
                        id="reregister-pin"
                        v-model="reregisterPin"
                        class="w-44"
                        inputmode="numeric"
                        maxlength="6"
                        autocomplete="off"
                        :placeholder="displayName.has_stored_pin ? $t('businessProfile.displayNamePinStored') : $t('businessProfile.displayNamePinRequired')"
                        @keydown.enter.prevent="applyDisplayName"
                      />
                    </div>
                    <Button
                      type="button"
                      size="sm"
                      :disabled="isApplyingDisplayName || (!displayName.has_stored_pin && reregisterPin.trim().length !== 6)"
                      data-testid="display-name-apply"
                      @click="applyDisplayName"
                    >
                      <Loader2 v-if="isApplyingDisplayName" class="h-4 w-4 mr-1 animate-spin" />
                      {{ $t('businessProfile.displayNameApply') }}
                    </Button>
                  </div>
                </div>
                <p v-else class="text-xs text-muted-foreground">
                  {{ $t('businessProfile.displayNameApprovedSmb') }}
                </p>
              </template>
            </div>
          </template>

          <div class="space-y-2">
            <Label for="new-display-name" class="text-xs">{{ $t('businessProfile.displayNameNew') }}</Label>
            <div class="flex gap-2">
              <Input
                id="new-display-name"
                v-model="newDisplayName"
                :placeholder="$t('businessProfile.displayNamePlaceholder')"
                maxlength="100"
                :disabled="isRequestingDisplayName"
                data-testid="display-name-input"
                @input="confirmingDisplayName = false"
                @keydown.enter.prevent="startDisplayNameRequest"
              />
              <Button
                v-if="!confirmingDisplayName"
                type="button"
                variant="outline"
                :disabled="!trimmedNewDisplayName"
                data-testid="display-name-request"
                @click="startDisplayNameRequest"
              >
                {{ $t('businessProfile.displayNameRequest') }}
              </Button>
            </div>
            <div
              v-if="confirmingDisplayName"
              class="rounded-md border border-amber-500/40 bg-amber-500/5 p-3 space-y-2"
              data-testid="display-name-confirm"
            >
              <p class="text-xs">{{ $t('businessProfile.displayNameConfirm', { name: trimmedNewDisplayName }) }}</p>
              <div class="flex justify-end gap-2">
                <Button type="button" variant="ghost" size="sm" :disabled="isRequestingDisplayName" @click="confirmingDisplayName = false">
                  {{ $t('businessProfile.displayNameCancel') }}
                </Button>
                <Button type="button" size="sm" :disabled="isRequestingDisplayName" data-testid="display-name-submit" @click="submitDisplayNameRequest">
                  <Loader2 v-if="isRequestingDisplayName" class="h-4 w-4 mr-1 animate-spin" />
                  {{ $t('businessProfile.displayNameSubmit') }}
                </Button>
              </div>
            </div>
          </div>
        </div>

        <!-- About -->
        <div class="md:col-span-2 space-y-2">
          <Label for="about">{{ $t('businessProfile.about') }}</Label>
          <Input id="about" v-model="profile.about" :placeholder="$t('businessProfile.aboutPlaceholder')" maxlength="139" />
          <p class="text-xs text-muted-foreground text-right">{{ profile.about.length }}/139</p>
        </div>

        <!-- Description -->
        <div class="md:col-span-2 space-y-2">
          <Label for="description">{{ $t('businessProfile.businessDescription') }}</Label>
          <Textarea id="description" v-model="profile.description" :placeholder="$t('businessProfile.descriptionPlaceholder')" :rows="3" maxlength="512" />
          <p class="text-xs text-muted-foreground text-right">{{ profile.description.length }}/512</p>
        </div>

        <!-- Vertical (Category) -->
        <div class="space-y-2">
          <Label for="vertical">{{ $t('businessProfile.industry') }}</Label>
          <Select v-model="profile.vertical">
            <SelectTrigger>
              <SelectValue :placeholder="$t('businessProfile.selectCategory')">
                <template v-if="profile.vertical">{{ selectedVerticalLabel }}</template>
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="v in verticals" :key="v.value" :value="v.value">
                {{ v.label }}
              </SelectItem>
            </SelectContent>
          </Select>
        </div>

        <!-- Email -->
        <div class="space-y-2">
          <Label for="email">{{ $t('businessProfile.contactEmail') }}</Label>
          <div class="relative">
            <Mail class="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
            <Input id="email" v-model="profile.email" type="email" class="pl-9" :placeholder="$t('businessProfile.emailPlaceholder')" maxlength="128" />
          </div>
        </div>

        <!-- Address -->
        <div class="md:col-span-2 space-y-2">
          <Label for="address">{{ $t('businessProfile.businessAddress') }}</Label>
          <div class="relative">
            <MapPin class="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
            <Input id="address" v-model="profile.address" class="pl-9" :placeholder="$t('businessProfile.addressPlaceholder')" maxlength="256" />
          </div>
        </div>

        <!-- Websites -->
        <div class="md:col-span-2 space-y-3">
          <Label>{{ $t('businessProfile.websites') }}</Label>
          <div v-for="(_, index) in profile.websites" :key="index" class="relative">
            <Globe class="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
            <Input v-model="profile.websites[index]" class="pl-9" :placeholder="$t('businessProfile.websitePlaceholder')" maxlength="256" />
          </div>
        </div>
      </div>
    </div>
  </CrudFormDialog>
</template>
