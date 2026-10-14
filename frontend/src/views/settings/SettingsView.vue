<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from '@/components/ui/select'
import { PageHeader, AuditLogPanel } from '@/components/shared'
import LanguageSwitcher from '@/components/LanguageSwitcher.vue'
import { toast } from 'vue-sonner'
import { Settings, Bell, Loader2, Globe, Phone, Upload, Play, Pause, Music, Mail } from 'lucide-vue-next'
import { usersService, organizationService, sharedMetaAppService, organizationsService, serverSettingsService, type MetaAppShareMode, type Organization, type ServerSettings } from '@/services/api'
import { useAuthStore } from '@/stores/auth'
import { getTimezoneGroups } from '@/lib/timezones'
import { setOrgDisplayPrefs, type TimeFormat } from '@/lib/orgDisplay'
import { validPort } from '@/lib/setup'
import { Checkbox } from '@/components/ui/checkbox'

const { t } = useI18n()
const authStore = useAuthStore()

// The active org may be overridden by the X-Organization-ID header
// (localStorage.selected_organization_id) when a super admin switches orgs.
// That override is what the backend uses for scoping, so we must read it here
// too — otherwise the activity log panel would query the user's default org
// instead of the currently-active one.
const orgID = computed(
  () => localStorage.getItem('selected_organization_id') || authStore.organizationId,
)
const userID = computed(() => authStore.user?.id || '')
const canWriteAccounts = computed(() => authStore.hasPermission('accounts', 'write'))
const isSuperAdmin = computed(() => !!authStore.user?.is_super_admin)

const isSubmitting = ref(false)
const isSavingSharedMeta = ref(false)
const isLoading = ref(true)

// General Settings
const generalSettings = ref({
  organization_name: 'My Organization',
  default_timezone: 'UTC',
  date_format: 'YYYY-MM-DD',
  time_format: '12h' as TimeFormat,
  mask_phone_numbers: false,
  meta_app_id: '',
  meta_config_id: '',
  meta_app_secret: '',
  has_meta_app_secret: false,
  meta_source: 'config' as 'org' | 'shared' | 'config',
})

const sharedMeta = ref({
  has_app_secret: false,
  share_enabled: false,
  share_mode: 'global' as Exclude<MetaAppShareMode, 'off'>,
  shared_org_ids: [] as string[],
})
const allOrganizations = ref<Organization[]>([])
const sharingEnabled = computed({
  get: () => sharedMeta.value.share_enabled,
  set: (v: boolean) => {
    sharedMeta.value.share_enabled = v
    if (v && sharedMeta.value.share_mode !== 'global' && sharedMeta.value.share_mode !== 'selected') {
      sharedMeta.value.share_mode = 'global'
    }
  },
})

const timezoneGroups = computed(() => getTimezoneGroups(generalSettings.value.default_timezone))
const timezoneItemClass = 'text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100'

function metaSourceLabel(source: string): string {
  if (source === 'org') return t('settings.metaSourceOrg')
  if (source === 'shared') return t('settings.metaSourceShared')
  return t('settings.metaSourceConfig')
}

function toggleSharedOrg(id: string, checked: boolean) {
  const set = new Set(sharedMeta.value.shared_org_ids)
  if (checked) set.add(id)
  else set.delete(id)
  sharedMeta.value.shared_org_ids = Array.from(set)
}

// Notification Settings
const notificationSettings = ref({
  email_notifications: true,
  new_message_alerts: true,
  campaign_updates: true
})

// Calling Settings
const callingSettings = ref({
  calling_enabled: false,
  max_call_duration: 300,
  transfer_timeout_secs: 120,
  hold_music_file: '',
  ringback_file: ''
})

const isUploadingHoldMusic = ref(false)
const isUploadingRingback = ref(false)
const holdMusicInput = ref<HTMLInputElement | null>(null)
const ringbackInput = ref<HTMLInputElement | null>(null)
const holdMusicAudio = ref<HTMLAudioElement | null>(null)
const ringbackAudio = ref<HTMLAudioElement | null>(null)
const playingHoldMusic = ref(false)
const playingRingback = ref(false)

// Bump these keys to force the AuditLogPanel to remount and refetch after a save.
// The backend writes audit entries asynchronously in a goroutine, so we delay
// the remount slightly to give the write time to hit the DB before refetching.
const generalLogKey = ref(0)
const notificationLogKey = ref(0)
const callingLogKey = ref(0)
const emailLogKey = ref(0)

const smtpSettings = ref({
  smtp_host: '',
  smtp_port: 587,
  smtp_username: '',
  smtp_password: '',
  smtp_from: '',
  smtp_security: 'tls',
  has_smtp_password: false,
})

const serverSettings = ref<ServerSettings | null>(null)
const serverPort = ref(4747)
const isSavingServer = ref(false)

async function loadServerSettings() {
  if (!isSuperAdmin.value) return
  try {
    const res = await serverSettingsService.get()
    serverSettings.value = res.data.data
    serverPort.value = res.data.data.config_port
  } catch (error) {
    console.error('Failed to load server settings:', error)
    serverSettings.value = null
  }
}

async function saveServerPort() {
  if (!validPort(serverPort.value)) {
    toast.error(t('settings.server.invalidPort'))
    return
  }
  isSavingServer.value = true
  try {
    const res = await serverSettingsService.update(serverPort.value)
    serverSettings.value = res.data.data
    toast.success(t('settings.server.saved'))
  } catch (e: any) {
    toast.error(e?.response?.data?.message ?? t('settings.server.saveFailed'))
  } finally {
    isSavingServer.value = false
  }
}

function refreshActivityLog(key: typeof generalLogKey) {
  setTimeout(() => { key.value++ }, 500)
}

onMounted(async () => {
  try {
    const [orgResponse, userResponse] = await Promise.all([
      organizationService.getSettings(),
      usersService.me()
    ])

    // Organization settings
    const orgData = orgResponse.data.data || orgResponse.data
    if (orgData) {
      generalSettings.value = {
        organization_name: orgData.name || 'My Organization',
        default_timezone: orgData.settings?.timezone || 'UTC',
        date_format: orgData.settings?.date_format || 'YYYY-MM-DD',
        time_format: (orgData.settings?.time_format === '24h' ? '24h' : '12h') as TimeFormat,
        mask_phone_numbers: orgData.settings?.mask_phone_numbers || false,
        meta_app_id: orgData.settings?.meta_app_id || '',
        meta_config_id: orgData.settings?.meta_config_id || '',
        meta_app_secret: '',
        has_meta_app_secret: orgData.settings?.has_meta_app_secret || false,
        meta_source: (orgData.settings?.meta_source as 'org' | 'shared' | 'config') || 'config',
      }
      setOrgDisplayPrefs({
        time_format: generalSettings.value.time_format,
        date_format: generalSettings.value.date_format,
        timezone: generalSettings.value.default_timezone,
      })
      callingSettings.value = {
        calling_enabled: orgData.settings?.calling_enabled || false,
        max_call_duration: orgData.settings?.max_call_duration || 300,
        transfer_timeout_secs: orgData.settings?.transfer_timeout_secs || 120,
        hold_music_file: orgData.settings?.hold_music_file || '',
        ringback_file: orgData.settings?.ringback_file || ''
      }
      smtpSettings.value = {
        smtp_host: orgData.settings?.smtp_host || '',
        smtp_port: orgData.settings?.smtp_port || 587,
        smtp_username: orgData.settings?.smtp_username || '',
        smtp_password: '',
        smtp_from: orgData.settings?.smtp_from || '',
        smtp_security: orgData.settings?.smtp_security || 'tls',
        has_smtp_password: orgData.settings?.has_smtp_password || false,
      }
    }

    // User notification settings
    const user = userResponse.data.data || userResponse.data
    if (user.settings) {
      notificationSettings.value = {
        email_notifications: user.settings.email_notifications ?? true,
        new_message_alerts: user.settings.new_message_alerts ?? true,
        campaign_updates: user.settings.campaign_updates ?? true
      }
    }

    if (user?.is_super_admin) {
      await loadSharedMetaApp()
    }
    await loadServerSettings()
  } catch (error) {
    console.error('Failed to load settings:', error)
  } finally {
    isLoading.value = false
  }
})

async function loadSharedMetaApp() {
  try {
    const [sharedRes, orgsRes] = await Promise.all([
      sharedMetaAppService.get(),
      organizationsService.list(),
    ])
    const data = (sharedRes.data as any)?.data || sharedRes.data || {}
    const mode = (data.share_mode || 'off') as MetaAppShareMode
    sharedMeta.value = {
      has_app_secret: !!data.has_app_secret,
      share_enabled: mode === 'global' || mode === 'selected',
      share_mode: mode === 'selected' ? 'selected' : 'global',
      shared_org_ids: Array.isArray(data.shared_org_ids) ? data.shared_org_ids : [],
    }
    // If this org has no private override, show shared pool values in the main fields
    // so super admins edit one set of credentials.
    if (!generalSettings.value.meta_app_id && !generalSettings.value.meta_config_id && data.app_id) {
      generalSettings.value.meta_app_id = data.app_id || ''
      generalSettings.value.meta_config_id = data.config_id || ''
      if (!generalSettings.value.has_meta_app_secret && data.has_app_secret) {
        generalSettings.value.has_meta_app_secret = true
      }
    }
    const orgList = (orgsRes.data as any)?.data?.organizations || (orgsRes.data as any)?.organizations || []
    allOrganizations.value = orgList
  } catch (error) {
    console.error('Failed to load shared Meta App settings:', error)
  }
}

async function saveSharedMetaApp() {
  isSavingSharedMeta.value = true
  try {
    const payload: {
      app_id: string
      config_id: string
      app_secret?: string
      share_mode: MetaAppShareMode
      shared_org_ids: string[]
    } = {
      // Reuse the single Meta App ID / Config / Secret fields above.
      app_id: generalSettings.value.meta_app_id,
      config_id: generalSettings.value.meta_config_id,
      share_mode: sharedMeta.value.share_enabled ? sharedMeta.value.share_mode : 'off',
      shared_org_ids: sharedMeta.value.shared_org_ids,
    }
    if (generalSettings.value.meta_app_secret) {
      payload.app_secret = generalSettings.value.meta_app_secret
    }
    await sharedMetaAppService.update(payload)
    await loadSharedMetaApp()
    const orgResponse = await organizationService.getSettings()
    const orgData = orgResponse.data.data || orgResponse.data
    if (orgData?.settings?.meta_source) {
      generalSettings.value.meta_source = orgData.settings.meta_source
    }
    toast.success(t('settings.sharedMetaSaved'))
  } catch (error) {
    toast.error(t('common.failedSave', { resource: t('settings.sharedMetaApp') }))
  } finally {
    isSavingSharedMeta.value = false
  }
}

async function saveGeneralSettings() {
  isSubmitting.value = true
  try {
    const payload: any = {
      name: generalSettings.value.organization_name,
      timezone: generalSettings.value.default_timezone,
      date_format: generalSettings.value.date_format,
      time_format: generalSettings.value.time_format,
      mask_phone_numbers: generalSettings.value.mask_phone_numbers
    }
    if (canWriteAccounts.value) {
      payload.meta_app_id = generalSettings.value.meta_app_id
      payload.meta_config_id = generalSettings.value.meta_config_id
      if (generalSettings.value.meta_app_secret) {
        payload.meta_app_secret = generalSettings.value.meta_app_secret
      }
    }
    await organizationService.updateSettings(payload)
    setOrgDisplayPrefs({
      time_format: generalSettings.value.time_format,
      date_format: generalSettings.value.date_format,
      timezone: generalSettings.value.default_timezone,
    })
    toast.success(t('settings.generalSaved'))
    // Clear secret input after save
    generalSettings.value.meta_app_secret = ''
    // Refresh organization settings to update has_meta_app_secret status
    const orgResponse = await organizationService.getSettings()
    const orgData = orgResponse.data.data || orgResponse.data
    if (orgData) {
      generalSettings.value.has_meta_app_secret = orgData.settings?.has_meta_app_secret || false
    }
    refreshActivityLog(generalLogKey)
  } catch (error) {
    toast.error(t('common.failedSave', { resource: t('resources.settings') }))
  } finally {
    isSubmitting.value = false
  }
}

async function saveNotificationSettings() {
  isSubmitting.value = true
  try {
    await usersService.updateSettings({
      email_notifications: notificationSettings.value.email_notifications,
      new_message_alerts: notificationSettings.value.new_message_alerts,
      campaign_updates: notificationSettings.value.campaign_updates
    })
    toast.success(t('settings.notificationsSaved'))
    refreshActivityLog(notificationLogKey)
  } catch (error) {
    toast.error(t('common.failedSave', { resource: t('resources.notificationSettings') }))
  } finally {
    isSubmitting.value = false
  }
}

async function saveCallingSettings() {
  isSubmitting.value = true
  try {
    await organizationService.updateSettings({
      calling_enabled: callingSettings.value.calling_enabled,
      max_call_duration: callingSettings.value.max_call_duration,
      transfer_timeout_secs: callingSettings.value.transfer_timeout_secs
    })
    toast.success(t('settings.callingSaved'))
    refreshActivityLog(callingLogKey)
  } catch (error) {
    toast.error(t('common.failedSave', { resource: t('resources.settings') }))
  } finally {
    isSubmitting.value = false
  }
}

async function saveSMTPSettings() {
  isSubmitting.value = true
  try {
    const payload: Record<string, any> = {
      smtp_host: smtpSettings.value.smtp_host,
      smtp_port: smtpSettings.value.smtp_port,
      smtp_username: smtpSettings.value.smtp_username,
      smtp_from: smtpSettings.value.smtp_from,
      smtp_security: smtpSettings.value.smtp_security,
    }
    if (smtpSettings.value.smtp_password) {
      payload.smtp_password = smtpSettings.value.smtp_password
    }
    await organizationService.updateSettings(payload)
    smtpSettings.value.smtp_password = ''
    const orgResponse = await organizationService.getSettings()
    const orgData = orgResponse.data.data || orgResponse.data
    smtpSettings.value.has_smtp_password = orgData?.settings?.has_smtp_password || false
    toast.success('SMTP settings saved')
    refreshActivityLog(emailLogKey)
  } catch (error) {
    toast.error(t('common.failedSave', { resource: 'SMTP' }))
  } finally {
    isSubmitting.value = false
  }
}

async function uploadAudio(type: 'hold_music' | 'ringback', event: Event) {
  const input = event.target as HTMLInputElement
  const file = input?.files?.[0]
  if (!file) return

  const isHold = type === 'hold_music'
  if (isHold) isUploadingHoldMusic.value = true
  else isUploadingRingback.value = true

  try {
    const response = await organizationService.uploadOrgAudio(file, type)
    const data = response.data.data || response.data
    if (isHold) callingSettings.value.hold_music_file = data.filename
    else callingSettings.value.ringback_file = data.filename
    toast.success(t('settings.audioUploaded'))
  } catch (error) {
    toast.error(t('settings.audioUploadFailed'))
  } finally {
    if (isHold) isUploadingHoldMusic.value = false
    else isUploadingRingback.value = false
    input.value = ''
  }
}

function togglePlayAudio(type: 'hold_music' | 'ringback') {
  const isHold = type === 'hold_music'
  const filename = isHold ? callingSettings.value.hold_music_file : callingSettings.value.ringback_file
  if (!filename) return

  const audioRef = isHold ? holdMusicAudio : ringbackAudio
  const playingRef = isHold ? playingHoldMusic : playingRingback

  if (playingRef.value && audioRef.value) {
    audioRef.value.pause()
    audioRef.value.currentTime = 0
    playingRef.value = false
    return
  }

  const audio = new Audio(`/api/ivr-flows/audio/${filename}`)
  audioRef.value = audio
  playingRef.value = true
  audio.play()
  audio.onended = () => { playingRef.value = false }
}
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader :title="$t('settings.title')" :subtitle="$t('settings.subtitle')" :icon="Settings" icon-gradient="bg-gradient-to-br from-gray-500 to-gray-600 shadow-gray-500/20" />
    <ScrollArea class="flex-1">
      <div class="p-6 space-y-4 max-w-4xl mx-auto">
        <Tabs default-value="general" class="w-full">
          <TabsList class="grid w-full grid-cols-4 mb-6 bg-white/[0.04] border border-white/[0.08] light:bg-gray-100 light:border-gray-200">
            <TabsTrigger value="general" class="data-[state=active]:bg-white/[0.08] data-[state=active]:text-white text-white/50 light:data-[state=active]:bg-white light:data-[state=active]:text-gray-900 light:text-gray-500">
              <Settings class="h-4 w-4 mr-2" />
              {{ $t('settings.general') }}
            </TabsTrigger>
            <TabsTrigger value="notifications" class="data-[state=active]:bg-white/[0.08] data-[state=active]:text-white text-white/50 light:data-[state=active]:bg-white light:data-[state=active]:text-gray-900 light:text-gray-500">
              <Bell class="h-4 w-4 mr-2" />
              {{ $t('settings.notifications') }}
            </TabsTrigger>
            <TabsTrigger value="calling" class="data-[state=active]:bg-white/[0.08] data-[state=active]:text-white text-white/50 light:data-[state=active]:bg-white light:data-[state=active]:text-gray-900 light:text-gray-500">
              <Phone class="h-4 w-4 mr-2" />
              {{ $t('settings.calling') }}
            </TabsTrigger>
            <TabsTrigger value="email" class="data-[state=active]:bg-white/[0.08] data-[state=active]:text-white text-white/50 light:data-[state=active]:bg-white light:data-[state=active]:text-gray-900 light:text-gray-500">
              <Mail class="h-4 w-4 mr-2" />
              Email
            </TabsTrigger>
          </TabsList>

          <!-- General Settings Tab -->
          <TabsContent value="general">
            <div class="rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
              <div class="p-6 pb-3">
                <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ $t('settings.generalSettings') }}</h3>
                <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.generalSettingsDesc') }}</p>
              </div>
              <div class="p-6 pt-3 space-y-4">
                <div class="space-y-2">
                  <Label for="org_name" class="text-white/70 light:text-gray-700">{{ $t('settings.organizationName') }}</Label>
                  <Input
                    id="org_name"
                    v-model="generalSettings.organization_name"
                    :placeholder="$t('settings.organizationPlaceholder')"
                  />
                </div>
                <div class="grid grid-cols-2 gap-4">
                  <div class="space-y-2">
                    <Label for="timezone" class="text-white/70 light:text-gray-700">{{ $t('settings.defaultTimezone') }}</Label>
                    <Select v-model="generalSettings.default_timezone">
                      <SelectTrigger class="bg-white/[0.04] border-white/[0.1] text-white/70 light:bg-white light:border-gray-200 light:text-gray-700">
                        <SelectValue :placeholder="$t('settings.selectTimezone')" />
                      </SelectTrigger>
                      <SelectContent class="bg-[#141414] border-white/[0.08] light:bg-white light:border-gray-200 max-h-80">
                        <SelectGroup v-for="group in timezoneGroups" :key="group.region">
                          <SelectLabel class="text-white/40 light:text-gray-500">{{ group.region }}</SelectLabel>
                          <SelectItem
                            v-for="tz in group.timezones"
                            :key="tz.value"
                            :value="tz.value"
                            :text-value="tz.label"
                            :class="timezoneItemClass"
                          >
                            {{ tz.label }}
                          </SelectItem>
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                  </div>
                  <div class="space-y-2">
                    <Label for="date_format" class="text-white/70 light:text-gray-700">{{ $t('settings.dateFormat') }}</Label>
                    <Select v-model="generalSettings.date_format">
                      <SelectTrigger class="bg-white/[0.04] border-white/[0.1] text-white/70 light:bg-white light:border-gray-200 light:text-gray-700">
                        <SelectValue :placeholder="$t('settings.selectFormat')" />
                      </SelectTrigger>
                      <SelectContent class="bg-[#141414] border-white/[0.08] light:bg-white light:border-gray-200">
                        <SelectItem value="YYYY-MM-DD" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">YYYY-MM-DD</SelectItem>
                        <SelectItem value="DD/MM/YYYY" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">DD/MM/YYYY</SelectItem>
                        <SelectItem value="MM/DD/YYYY" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">MM/DD/YYYY</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                </div>
                <div class="space-y-2 max-w-xs">
                  <Label for="time_format" class="text-white/70 light:text-gray-700">{{ $t('settings.timeFormat') }}</Label>
                  <Select v-model="generalSettings.time_format">
                    <SelectTrigger id="time_format" class="bg-white/[0.04] border-white/[0.1] text-white/70 light:bg-white light:border-gray-200 light:text-gray-700">
                      <SelectValue :placeholder="$t('settings.selectTimeFormat')" />
                    </SelectTrigger>
                    <SelectContent class="bg-[#141414] border-white/[0.08] light:bg-white light:border-gray-200">
                      <SelectItem value="24h" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">{{ $t('settings.timeFormat24h') }}</SelectItem>
                      <SelectItem value="12h" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">{{ $t('settings.timeFormat12h') }}</SelectItem>
                    </SelectContent>
                  </Select>
                  <p class="text-xs text-white/40 light:text-gray-500">{{ $t('settings.timeFormatDesc') }}</p>
                </div>
                <div class="space-y-2">
                  <Label class="text-white/70 light:text-gray-700">
                    <Globe class="h-4 w-4 inline mr-1" />
                    {{ $t('settings.language') }}
                  </Label>
                  <LanguageSwitcher class="max-w-xs" />
                  <p class="text-xs text-white/40 light:text-gray-500">{{ $t('settings.languageDesc') }}</p>
                </div>
                <Separator class="bg-white/[0.08] light:bg-gray-200" />
                <div class="flex items-center justify-between">
                  <div>
                    <p class="font-medium text-white light:text-gray-900">{{ $t('settings.maskPhoneNumbers') }}</p>
                    <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.maskPhoneNumbersDesc') }}</p>
                  </div>
                  <Switch
                    :checked="generalSettings.mask_phone_numbers"
                    @update:checked="generalSettings.mask_phone_numbers = $event"
                  />
                </div>
                <div class="flex justify-end">
                  <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="saveGeneralSettings" :disabled="isSubmitting">
                    <Loader2 v-if="isSubmitting" class="mr-2 h-4 w-4 animate-spin" />
                    {{ $t('settings.save') }}
                  </Button>
                </div>
              </div>
            </div>

            <!-- Meta App Credentials Card (accounts:write private override; super admin sharing) -->
            <div v-if="canWriteAccounts || isSuperAdmin" class="mt-6 rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
              <div class="p-6 pb-3">
                <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ $t('settings.metaAppCredentials') }}</h3>
                <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.metaAppCredentialsDesc') }}</p>
                <p class="text-xs text-white/40 light:text-gray-500 mt-1">
                  {{ $t('settings.metaSourceLabel') }}: {{ metaSourceLabel(generalSettings.meta_source) }}
                </p>
              </div>
              <div class="p-6 pt-3 space-y-4">
                <template v-if="canWriteAccounts || isSuperAdmin">
                <p class="text-xs text-white/50 light:text-gray-500">{{ $t('settings.metaOrgOverrideHint') }}</p>
                <div class="grid grid-cols-2 gap-4">
                  <div class="space-y-2">
                    <Label for="meta_app_id" class="text-white/70 light:text-gray-700">{{ $t('settings.metaAppId') }}</Label>
                    <Input
                      id="meta_app_id"
                      v-model="generalSettings.meta_app_id"
                      placeholder="e.g. 123456789012345"
                    />
                  </div>
                  <div class="space-y-2">
                    <Label for="meta_config_id" class="text-white/70 light:text-gray-700">{{ $t('settings.metaConfigId') }}</Label>
                    <Input
                      id="meta_config_id"
                      v-model="generalSettings.meta_config_id"
                      placeholder="e.g. 987654321098765"
                    />
                  </div>
                </div>
                <div class="space-y-2">
                  <Label for="meta_app_secret" class="text-white/70 light:text-gray-700">{{ $t('settings.metaAppSecret') }}</Label>
                  <Input
                    id="meta_app_secret"
                    type="password"
                    v-model="generalSettings.meta_app_secret"
                    :placeholder="generalSettings.has_meta_app_secret || sharedMeta.has_app_secret ? '••••••••••••' : 'Enter Meta App Secret'"
                  />
                </div>
                <div v-if="canWriteAccounts" class="flex justify-end">
                  <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="saveGeneralSettings" :disabled="isSubmitting">
                    <Loader2 v-if="isSubmitting" class="mr-2 h-4 w-4 animate-spin" />
                    {{ $t('settings.save') }}
                  </Button>
                </div>
                </template>

                <!-- Super admin: share the credentials above (on/off) -->
                <template v-if="isSuperAdmin">
                  <Separator class="bg-white/[0.08] light:bg-gray-200" />
                  <div class="flex items-center justify-between gap-4">
                    <div class="space-y-1 min-w-0">
                      <h4 class="text-sm font-semibold text-white light:text-gray-900">{{ $t('settings.sharedMetaApp') }}</h4>
                      <p class="text-xs text-white/40 light:text-gray-500">{{ $t('settings.sharedMetaAppDesc') }}</p>
                    </div>
                    <Switch
                      :checked="sharingEnabled"
                      @update:checked="sharingEnabled = $event"
                    />
                  </div>
                  <div v-if="sharingEnabled" class="space-y-2">
                    <Label class="text-white/70 light:text-gray-700">{{ $t('settings.shareMode') }}</Label>
                    <Select v-model="sharedMeta.share_mode">
                      <SelectTrigger class="bg-white/[0.04] border-white/[0.1] text-white/70 light:bg-white light:border-gray-200 light:text-gray-700">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent class="bg-[#141414] border-white/[0.08] light:bg-white light:border-gray-200">
                        <SelectItem value="global" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">{{ $t('settings.shareModeGlobal') }}</SelectItem>
                        <SelectItem value="selected" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">{{ $t('settings.shareModeSelected') }}</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                  <div v-if="sharingEnabled && sharedMeta.share_mode === 'selected'" class="space-y-2">
                    <Label class="text-white/70 light:text-gray-700">{{ $t('settings.sharedOrganizations') }}</Label>
                    <div class="max-h-40 overflow-y-auto rounded-lg border border-white/[0.08] light:border-gray-200 p-3 space-y-2">
                      <label
                        v-for="org in allOrganizations"
                        :key="org.id"
                        class="flex items-center gap-2 text-sm text-white/70 light:text-gray-700 cursor-pointer"
                      >
                        <Checkbox
                          :checked="sharedMeta.shared_org_ids.includes(org.id)"
                          @update:checked="(v) => toggleSharedOrg(org.id, v === true)"
                        />
                        <span>{{ org.name }}</span>
                      </label>
                      <p v-if="allOrganizations.length === 0" class="text-xs text-white/40 light:text-gray-500">{{ $t('settings.noOrganizations') }}</p>
                    </div>
                  </div>
                  <div class="flex justify-end">
                    <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="saveSharedMetaApp" :disabled="isSavingSharedMeta">
                      <Loader2 v-if="isSavingSharedMeta" class="mr-2 h-4 w-4 animate-spin" />
                      {{ $t('settings.saveSharedMeta') }}
                    </Button>
                  </div>
                </template>
              </div>
            </div>
            <div
              v-if="isSuperAdmin && serverSettings"
              class="mt-4 rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200"
              data-testid="server-settings-card"
            >
              <div class="p-6 pb-3">
                <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ $t('settings.server.title') }}</h3>
                <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.server.desc') }}</p>
              </div>
              <div class="p-6 pt-3 space-y-4">
                <div class="space-y-2 max-w-xs">
                  <Label for="server_port" class="text-white/70 light:text-gray-700">{{ $t('settings.server.port') }}</Label>
                  <Input id="server_port" v-model.number="serverPort" type="number" min="1" max="65535" :disabled="!serverSettings.writable" />
                </div>
                <p class="text-xs font-mono break-all text-white/40 light:text-gray-500">{{ $t('settings.server.configFile', { path: serverSettings.config_path }) }}</p>
                <p v-if="!serverSettings.writable" class="text-xs text-amber-500">{{ $t('settings.server.notWritable', { path: serverSettings.config_path }) }}</p>
                <p v-else-if="serverSettings.config_port !== serverSettings.port" class="text-xs text-amber-500">{{ $t('settings.server.restartNeeded', { port: serverSettings.config_port }) }}</p>
                <div class="flex justify-end">
                  <Button
                    variant="outline"
                    size="sm"
                    class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50"
                    :disabled="isSavingServer || !serverSettings.writable"
                    @click="saveServerPort"
                  >
                    <Loader2 v-if="isSavingServer" class="mr-2 h-4 w-4 animate-spin" />
                    {{ $t('settings.server.save') }}
                  </Button>
                </div>
              </div>
            </div>
            <div v-if="orgID" class="mt-4">
              <AuditLogPanel :key="generalLogKey" resource-type="settings.general" :resource-id="orgID" />
            </div>
          </TabsContent>

          <!-- Notification Settings Tab -->
          <TabsContent value="notifications">
            <div class="rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
              <div class="p-6 pb-3">
                <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ $t('settings.notifications') }}</h3>
                <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.notificationsDesc') }}</p>
              </div>
              <div class="p-6 pt-3 space-y-4">
                <div class="flex items-center justify-between">
                  <div>
                    <p class="font-medium text-white light:text-gray-900">{{ $t('settings.emailNotifications') }}</p>
                    <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.emailNotificationsDesc') }}</p>
                  </div>
                  <Switch
                    :checked="notificationSettings.email_notifications"
                    @update:checked="notificationSettings.email_notifications = $event"
                  />
                </div>
                <Separator class="bg-white/[0.08] light:bg-gray-200" />
                <div class="flex items-center justify-between">
                  <div>
                    <p class="font-medium text-white light:text-gray-900">{{ $t('settings.newMessageAlerts') }}</p>
                    <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.newMessageAlertsDesc') }}</p>
                  </div>
                  <Switch
                    :checked="notificationSettings.new_message_alerts"
                    @update:checked="notificationSettings.new_message_alerts = $event"
                  />
                </div>
                <Separator class="bg-white/[0.08] light:bg-gray-200" />
                <div class="flex items-center justify-between">
                  <div>
                    <p class="font-medium text-white light:text-gray-900">{{ $t('settings.campaignUpdates') }}</p>
                    <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.campaignUpdatesDesc') }}</p>
                  </div>
                  <Switch
                    :checked="notificationSettings.campaign_updates"
                    @update:checked="notificationSettings.campaign_updates = $event"
                  />
                </div>
                <div class="flex justify-end pt-4">
                  <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="saveNotificationSettings" :disabled="isSubmitting">
                    <Loader2 v-if="isSubmitting" class="mr-2 h-4 w-4 animate-spin" />
                    {{ $t('settings.save') }}
                  </Button>
                </div>
              </div>
            </div>
            <div v-if="userID" class="mt-4">
              <AuditLogPanel :key="notificationLogKey" resource-type="settings.notification" :resource-id="userID" />
            </div>
          </TabsContent>

          <!-- Calling Settings Tab -->
          <TabsContent value="calling">
            <div class="rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
              <div class="p-6 pb-3">
                <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ $t('settings.callingSettings') }}</h3>
                <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.callingSettingsDesc') }}</p>
              </div>
              <div class="p-6 pt-3 space-y-4">
                <div class="flex items-center justify-between">
                  <div>
                    <p class="font-medium text-white light:text-gray-900">{{ $t('settings.callingEnabled') }}</p>
                    <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.callingEnabledDesc') }}</p>
                  </div>
                  <Switch
                    :checked="callingSettings.calling_enabled"
                    @update:checked="callingSettings.calling_enabled = $event"
                  />
                </div>
                <Separator class="bg-white/[0.08] light:bg-gray-200" />
                <div class="grid grid-cols-2 gap-4" :class="{ 'opacity-50 pointer-events-none': !callingSettings.calling_enabled }">
                  <div class="space-y-2">
                    <Label for="max_call_duration" class="text-white/70 light:text-gray-700">{{ $t('settings.maxCallDuration') }}</Label>
                    <Input
                      id="max_call_duration"
                      type="number"
                      v-model.number="callingSettings.max_call_duration"
                      :min="60"
                      :max="3600"
                    />
                    <p class="text-xs text-white/40 light:text-gray-500">{{ $t('settings.maxCallDurationDesc') }}</p>
                  </div>
                  <div class="space-y-2">
                    <Label for="transfer_timeout" class="text-white/70 light:text-gray-700">{{ $t('settings.transferTimeout') }}</Label>
                    <Input
                      id="transfer_timeout"
                      type="number"
                      v-model.number="callingSettings.transfer_timeout_secs"
                      :min="30"
                      :max="600"
                    />
                    <p class="text-xs text-white/40 light:text-gray-500">{{ $t('settings.transferTimeoutDesc') }}</p>
                  </div>
                </div>
                <Separator class="bg-white/[0.08] light:bg-gray-200" />
                <!-- Hold Music Upload -->
                <div class="space-y-3" :class="{ 'opacity-50 pointer-events-none': !callingSettings.calling_enabled }">
                  <div>
                    <Label class="text-white/70 light:text-gray-700 flex items-center gap-2">
                      <Music class="h-4 w-4" />
                      {{ $t('settings.holdMusic') }}
                    </Label>
                    <p class="text-xs text-white/40 light:text-gray-500 mt-1">{{ $t('settings.holdMusicDesc') }}</p>
                  </div>
                  <div class="flex items-center gap-3">
                    <span class="text-sm text-white/50 light:text-gray-500">
                      {{ callingSettings.hold_music_file ? `${$t('settings.currentFile')}: ${callingSettings.hold_music_file}` : $t('settings.noFileUploaded') }}
                    </span>
                    <Button
                      v-if="callingSettings.hold_music_file"
                      variant="ghost"
                      size="sm"
                      class="h-8 w-8 p-0 text-white/50 hover:text-white light:text-gray-500 light:hover:text-gray-900"
                      @click="togglePlayAudio('hold_music')"
                    >
                      <Pause v-if="playingHoldMusic" class="h-4 w-4" />
                      <Play v-else class="h-4 w-4" />
                    </Button>
                  </div>
                  <div class="flex items-center gap-2">
                    <input ref="holdMusicInput" type="file" accept=".ogg,.opus,.mp3,.wav" class="hidden" @change="uploadAudio('hold_music', $event)" />
                    <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="holdMusicInput?.click()" :disabled="isUploadingHoldMusic">
                      <Loader2 v-if="isUploadingHoldMusic" class="mr-2 h-4 w-4 animate-spin" />
                      <Upload v-else class="mr-2 h-4 w-4" />
                      {{ $t('settings.uploadAudio') }}
                    </Button>
                    <span class="text-xs text-white/30 light:text-gray-400">.ogg, .opus, .mp3, .wav (max 5MB)</span>
                  </div>
                </div>
                <!-- Ringback Tone Upload -->
                <div class="space-y-3" :class="{ 'opacity-50 pointer-events-none': !callingSettings.calling_enabled }">
                  <div>
                    <Label class="text-white/70 light:text-gray-700 flex items-center gap-2">
                      <Phone class="h-4 w-4" />
                      {{ $t('settings.ringbackTone') }}
                    </Label>
                    <p class="text-xs text-white/40 light:text-gray-500 mt-1">{{ $t('settings.ringbackToneDesc') }}</p>
                  </div>
                  <div class="flex items-center gap-3">
                    <span class="text-sm text-white/50 light:text-gray-500">
                      {{ callingSettings.ringback_file ? `${$t('settings.currentFile')}: ${callingSettings.ringback_file}` : $t('settings.noFileUploaded') }}
                    </span>
                    <Button
                      v-if="callingSettings.ringback_file"
                      variant="ghost"
                      size="sm"
                      class="h-8 w-8 p-0 text-white/50 hover:text-white light:text-gray-500 light:hover:text-gray-900"
                      @click="togglePlayAudio('ringback')"
                    >
                      <Pause v-if="playingRingback" class="h-4 w-4" />
                      <Play v-else class="h-4 w-4" />
                    </Button>
                  </div>
                  <div class="flex items-center gap-2">
                    <input ref="ringbackInput" type="file" accept=".ogg,.opus,.mp3,.wav" class="hidden" @change="uploadAudio('ringback', $event)" />
                    <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="ringbackInput?.click()" :disabled="isUploadingRingback">
                      <Loader2 v-if="isUploadingRingback" class="mr-2 h-4 w-4 animate-spin" />
                      <Upload v-else class="mr-2 h-4 w-4" />
                      {{ $t('settings.uploadAudio') }}
                    </Button>
                    <span class="text-xs text-white/30 light:text-gray-400">.ogg, .opus, .mp3, .wav (max 5MB)</span>
                  </div>
                </div>
                <div class="flex justify-end pt-4">
                  <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="saveCallingSettings" :disabled="isSubmitting">
                    <Loader2 v-if="isSubmitting" class="mr-2 h-4 w-4 animate-spin" />
                    {{ $t('settings.save') }}
                  </Button>
                </div>
              </div>
            </div>
            <div v-if="orgID" class="mt-4">
              <AuditLogPanel :key="callingLogKey" resource-type="settings.calling" :resource-id="orgID" />
            </div>
          </TabsContent>

          <!-- Email / SMTP Tab -->
          <TabsContent value="email">
            <div class="rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
              <div class="p-6 pb-3">
                <h3 class="text-lg font-semibold text-white light:text-gray-900">SMTP</h3>
                <p class="text-sm text-white/40 light:text-gray-500">Org-level mail server for chatbot Email nodes. Passwords are encrypted at rest and never stored in flow graphs.</p>
              </div>
              <div class="p-6 pt-3 space-y-4">
                <div class="grid grid-cols-2 gap-4">
                  <div class="space-y-2">
                    <Label class="text-white/70 light:text-gray-700">Host</Label>
                    <Input v-model="smtpSettings.smtp_host" placeholder="smtp.example.com" />
                  </div>
                  <div class="space-y-2">
                    <Label class="text-white/70 light:text-gray-700">Port</Label>
                    <Input type="number" v-model.number="smtpSettings.smtp_port" />
                  </div>
                </div>
                <div class="grid grid-cols-2 gap-4">
                  <div class="space-y-2">
                    <Label class="text-white/70 light:text-gray-700">Username</Label>
                    <Input v-model="smtpSettings.smtp_username" />
                  </div>
                  <div class="space-y-2">
                    <Label class="text-white/70 light:text-gray-700">Password</Label>
                    <Input type="password" v-model="smtpSettings.smtp_password" :placeholder="smtpSettings.has_smtp_password ? '•••••••• (leave blank to keep)' : ''" />
                  </div>
                </div>
                <div class="grid grid-cols-2 gap-4">
                  <div class="space-y-2">
                    <Label class="text-white/70 light:text-gray-700">From address</Label>
                    <Input v-model="smtpSettings.smtp_from" placeholder="noreply@example.com" />
                  </div>
                  <div class="space-y-2">
                    <Label class="text-white/70 light:text-gray-700">Security</Label>
                    <Select v-model="smtpSettings.smtp_security">
                      <SelectTrigger class="bg-white/[0.04] border-white/[0.1] text-white/70 light:bg-white light:border-gray-200 light:text-gray-700">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="tls">STARTTLS</SelectItem>
                        <SelectItem value="ssl">SSL</SelectItem>
                        <SelectItem value="none">None</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                </div>
                <div class="flex justify-end pt-4">
                  <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="saveSMTPSettings" :disabled="isSubmitting">
                    <Loader2 v-if="isSubmitting" class="mr-2 h-4 w-4 animate-spin" />
                    {{ $t('settings.save') }}
                  </Button>
                </div>
              </div>
            </div>
          </TabsContent>
        </Tabs>
      </div>
    </ScrollArea>
  </div>
</template>
