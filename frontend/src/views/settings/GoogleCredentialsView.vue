<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { PageHeader, CrudFormDialog, DeleteConfirmDialog } from '@/components/shared'
import { googleCredentialsService, type GoogleCredential, type GoogleOAuthStatus } from '@/services/api'
import { toast } from 'vue-sonner'
import { Plus, Sheet, Trash2, Loader2, Copy, AlertTriangle, KeyRound, UserRound } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()

const creds = ref<GoogleCredential[]>([])
const isLoading = ref(false)
const isDialogOpen = ref(false)
const isSubmitting = ref(false)
const deleteOpen = ref(false)
const toDelete = ref<GoogleCredential | null>(null)
const formLabel = ref('')
const formJSON = ref('')
const oauthStatus = ref<GoogleOAuthStatus | null>(null)
const isConnecting = ref(false)

const OAUTH_SCOPE_LABELS: Record<string, string> = {
  'https://www.googleapis.com/auth/spreadsheets': 'Sheets',
  'https://www.googleapis.com/auth/calendar.events': 'Calendar',
  'https://www.googleapis.com/auth/documents': 'Docs',
  'https://www.googleapis.com/auth/drive.file': 'Drive (app files)',
}

function missingScopes(c: GoogleCredential): string[] {
  if (c.auth_type !== 'oauth' || !c.scopes?.length) return []
  return Object.entries(OAUTH_SCOPE_LABELS)
    .filter(([scope]) => !c.scopes!.includes(scope))
    .map(([, label]) => label)
}

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

async function loadOAuthStatus() {
  try {
    const res = await googleCredentialsService.oauthStatus()
    oauthStatus.value = (res.data as any)?.data || null
  } catch {
    oauthStatus.value = null
  }
}

function handleOAuthReturn() {
  const connected = route.query.google_connected
  const error = route.query.google_error
  if (typeof connected === 'string') {
    toast.success(`Connected Google account: ${connected}`)
  } else if (typeof error === 'string') {
    toast.error(error)
  } else {
    return
  }
  const query = { ...route.query }
  delete query.google_connected
  delete query.google_error
  router.replace({ query })
}

onMounted(() => {
  handleOAuthReturn()
  load()
  loadOAuthStatus()
})

async function connectGoogle() {
  isConnecting.value = true
  try {
    const res = await googleCredentialsService.startOAuth('')
    const authURL = (res.data as any)?.data?.auth_url
    if (!authURL) throw new Error('missing auth_url')
    window.location.href = authURL
  } catch (err: any) {
    toast.error(err?.response?.data?.message || 'Failed to start Google sign-in')
    isConnecting.value = false
  }
}

async function copyRedirectURI() {
  if (!oauthStatus.value?.redirect_uri) return
  try {
    await navigator.clipboard.writeText(oauthStatus.value.redirect_uri)
    toast.success('Redirect URI copied')
  } catch {
    toast.error('Copy failed')
  }
}

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
    <PageHeader title="Google credentials" description="Google accounts and service accounts for Sheets, Calendar, Docs and Drive nodes (encrypted at rest)" :icon="Sheet" icon-gradient="bg-gradient-to-br from-yellow-500 to-amber-600 shadow-yellow-500/20">
      <template #actions>
        <Button variant="outline" size="sm" @click="openCreate"><Plus class="h-4 w-4 mr-2" />Add service account</Button>
        <Button
          size="sm"
          :disabled="!oauthStatus?.enabled || isConnecting"
          data-testid="google-connect"
          @click="connectGoogle"
        >
          <Loader2 v-if="isConnecting" class="h-4 w-4 mr-2 animate-spin" />
          <svg v-else class="h-4 w-4 mr-2" viewBox="0 0 24 24" aria-hidden="true">
            <path fill="#4285F4" d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92a5.06 5.06 0 0 1-2.2 3.32v2.76h3.56c2.08-1.92 3.28-4.74 3.28-8.09z" />
            <path fill="#34A853" d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.56-2.76c-.99.66-2.25 1.06-3.72 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84A11 11 0 0 0 12 23z" />
            <path fill="#FBBC05" d="M5.84 14.11a6.6 6.6 0 0 1 0-4.22V7.05H2.18a11 11 0 0 0 0 9.9l3.66-2.84z" />
            <path fill="#EA4335" d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15A10.6 10.6 0 0 0 12 1 11 11 0 0 0 2.18 7.05l3.66 2.84C6.71 7.31 9.14 5.38 12 5.38z" />
          </svg>
          Connect Google account
        </Button>
      </template>
    </PageHeader>
    <ScrollArea class="flex-1">
      <div class="p-6 max-w-3xl mx-auto space-y-4">
        <Card v-if="oauthStatus && !oauthStatus.enabled" data-testid="google-oauth-setup">
          <CardHeader>
            <CardTitle class="text-base">Set up “Connect Google account”</CardTitle>
            <CardDescription>
              One-time server setup. After that, anyone can connect with their Google account instead of pasting a service-account key.
            </CardDescription>
          </CardHeader>
          <CardContent class="space-y-3 text-sm">
            <ol class="list-decimal pl-5 space-y-1.5 text-muted-foreground">
              <li>
                In <a href="https://console.cloud.google.com/apis/credentials" target="_blank" rel="noopener noreferrer" class="underline underline-offset-2 hover:text-foreground">Google Cloud Console → Credentials</a>,
                create an <span class="text-foreground">OAuth client ID</span> of type <span class="text-foreground">Web application</span>.
              </li>
              <li>Add this <span class="text-foreground">Authorized redirect URI</span>:</li>
            </ol>
            <div class="flex items-center gap-2">
              <Input :model-value="oauthStatus.redirect_uri" readonly class="font-mono text-xs" />
              <Button variant="outline" size="icon" class="shrink-0" @click="copyRedirectURI"><Copy class="h-4 w-4" /></Button>
            </div>
            <ol start="3" class="list-decimal pl-5 space-y-1.5 text-muted-foreground">
              <li>Enable the Google Sheets, Calendar, Docs and Drive APIs for the project.</li>
              <li>
                Put the client ID and secret in <code class="text-foreground">config.toml</code> under
                <code class="text-foreground">[google]</code> as <code class="text-foreground">oauth_client_id</code> and
                <code class="text-foreground">oauth_client_secret</code>, then restart. An enabled Google SSO client is used automatically if present.
              </li>
            </ol>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Credentials</CardTitle>
            <CardDescription>
              Google accounts act as the signed-in user; Drive access is limited to files created by Whatomate.
              For service accounts, share each spreadsheet, calendar or file with the service account email.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div v-if="isLoading" class="text-sm text-muted-foreground">Loading…</div>
            <div v-else-if="creds.length === 0" class="text-sm text-muted-foreground">No credentials yet.</div>
            <ul v-else class="space-y-2">
              <li v-for="c in creds" :key="c.id" class="flex items-center justify-between gap-3 border rounded-md px-3 py-2">
                <div class="min-w-0">
                  <div class="flex items-center gap-2">
                    <p class="font-medium truncate">{{ c.label }}</p>
                    <Badge variant="outline" class="shrink-0 gap-1 text-[11px]">
                      <UserRound v-if="c.auth_type === 'oauth'" class="h-3 w-3" />
                      <KeyRound v-else class="h-3 w-3" />
                      {{ c.auth_type === 'oauth' ? 'Google account' : 'Service account' }}
                    </Badge>
                  </div>
                  <p v-if="c.client_email && c.client_email !== c.label" class="text-xs text-muted-foreground truncate">{{ c.client_email }}</p>
                  <p v-if="missingScopes(c).length" class="text-xs text-amber-500 flex items-center gap-1 mt-0.5">
                    <AlertTriangle class="h-3 w-3" />
                    Not granted: {{ missingScopes(c).join(', ') }}. Reconnect to allow.
                  </p>
                </div>
                <Button variant="ghost" size="icon" class="h-8 w-8 shrink-0" @click="toDelete = c; deleteOpen = true">
                  <Trash2 class="h-4 w-4" />
                </Button>
              </li>
            </ul>
          </CardContent>
        </Card>
      </div>
    </ScrollArea>

    <CrudFormDialog v-model:open="isDialogOpen" create-title="Add service account" :is-submitting="isSubmitting" @submit="save">
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
