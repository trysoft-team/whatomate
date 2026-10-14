import axios, { type AxiosInstance, type AxiosError, type InternalAxiosRequestConfig } from 'axios'
import { setupTokenRejected, storedSetupToken } from '@/lib/setup'

// Get base path from server-injected config or fallback
const basePath = ((window as any).__BASE_PATH__ ?? '').replace(/\/$/, '')
const API_BASE_URL = import.meta.env.VITE_API_URL || `${basePath}/api`

export const api: AxiosInstance = axios.create({
  baseURL: API_BASE_URL,
  timeout: 30000,
  withCredentials: true,
  headers: {
    'Content-Type': 'application/json'
  }
})

// Helper to read a cookie by name
function getCookie(name: string): string | null {
  const match = document.cookie.match(new RegExp('(?:^|; )' + name + '=([^;]*)'))
  return match ? decodeURIComponent(match[1]) : null
}

/**
 * Build standard headers for native fetch() calls.
 * Includes X-Organization-ID (for org switching) and optionally X-CSRF-Token (for mutating requests).
 */
export function getRequestHeaders(opts?: { csrf?: boolean }): Record<string, string> {
  const headers: Record<string, string> = {}
  const selectedOrgId = localStorage.getItem('selected_organization_id')
  if (selectedOrgId) {
    headers['X-Organization-ID'] = selectedOrgId
  }
  if (opts?.csrf) {
    const csrfToken = getCookie('whm_csrf')
    if (csrfToken) {
      headers['X-CSRF-Token'] = csrfToken
    }
  }
  return headers
}

// Request interceptor to add CSRF token and organization header
api.interceptors.request.use(
  (config: InternalAxiosRequestConfig) => {
    // Add CSRF token on mutating requests (cookie-based auth sends cookies automatically)
    const method = (config.method || '').toUpperCase()
    if (method === 'POST' || method === 'PUT' || method === 'DELETE' || method === 'PATCH') {
      const csrfToken = getCookie('whm_csrf')
      if (csrfToken) {
        config.headers['X-CSRF-Token'] = csrfToken
      }
    }
    // Add organization override header for org switching
    const selectedOrgId = localStorage.getItem('selected_organization_id')
    if (selectedOrgId) {
      config.headers['X-Organization-ID'] = selectedOrgId
    }
    if (config.url?.startsWith('/setup/')) {
      const setupToken = storedSetupToken()
      if (setupToken) {
        config.headers['X-Setup-Token'] = setupToken
      }
    }
    return config
  },
  (error: AxiosError) => {
    return Promise.reject(error)
  }
)

// Token refresh mutex — ensures only one refresh runs at a time.
// Without this, multiple concurrent 401s each trigger a refresh, but the
// single-use refresh token (JTI deleted from Redis) causes all but the first
// to fail, which clears auth and logs the user out.
let isRefreshing = false
let refreshSubscribers: Array<(success: boolean) => void> = []

function onRefreshComplete(success: boolean) {
  refreshSubscribers.forEach(cb => cb(success))
  refreshSubscribers = []
}

// refreshAccessToken posts to /auth/refresh. The refresh token is single-use
// with rotation, so two tabs refreshing at once would have the loser's token
// rejected as "revoked" and get logged out. The Web Locks API serializes the
// refresh across all same-origin tabs: each tab refreshes in turn using the
// cookie rotated by the previous holder, so every refresh succeeds instead of
// racing. The in-tab `isRefreshing` mutex still dedupes concurrent 401s within
// a single tab. Falls back to a plain request where Web Locks is unavailable
// (older browsers / insecure contexts).
async function refreshAccessToken(): Promise<void> {
  const doRefresh = () =>
    axios.post(`${API_BASE_URL}/auth/refresh`, {}, { withCredentials: true })

  if (navigator.locks?.request) {
    await navigator.locks.request('whm-token-refresh', async () => {
      await doRefresh()
    })
  } else {
    await doRefresh()
  }
}

// Response interceptor for error handling
api.interceptors.response.use(
  (response) => response,
  async (error: AxiosError) => {
    const originalRequest = error.config as InternalAxiosRequestConfig & { _retry?: boolean }

    // Skip token refresh logic for auth endpoints
    const isAuthEndpoint = originalRequest?.url?.startsWith('/auth/')

    // The setup server has no sessions: a 401 means the setup key is missing or wrong
    if (originalRequest?.url?.startsWith('/setup/')) {
      if (error.response?.status === 401) setupTokenRejected()
      return Promise.reject(error)
    }

    // Handle 401 errors - try to refresh token (but not for auth endpoints)
    if (error.response?.status === 401 && !originalRequest._retry && !isAuthEndpoint) {
      originalRequest._retry = true

      // If a refresh is already in flight, queue this request to wait for it
      if (isRefreshing) {
        return new Promise((resolve, reject) => {
          refreshSubscribers.push((success: boolean) => {
            if (success) {
              resolve(api(originalRequest))
            } else {
              reject(error)
            }
          })
        })
      }

      isRefreshing = true

      try {
        // Browser sends whm_refresh cookie automatically via withCredentials.
        // Serialized across tabs via Web Locks to avoid single-use-token races.
        await refreshAccessToken()

        // Cookies are updated by the server response — notify waiting requests
        onRefreshComplete(true)
        isRefreshing = false

        // Retry the original request
        return api(originalRequest)
      } catch {
        // Refresh failed — notify waiting requests and redirect to login
        onRefreshComplete(false)
        isRefreshing = false

        localStorage.removeItem('user')
        localStorage.removeItem('auth_token')
        localStorage.removeItem('refresh_token')
        window.location.href = basePath + '/login'
      }
    }

    return Promise.reject(error)
  }
)

// API service methods
export const authService = {
  getWSToken: () => api.get('/auth/ws-token'),
}

export const usersService = {
  list: (params?: { search?: string; page?: number; limit?: number; role_id?: string; online_only?: boolean }) =>
    api.get('/users', { params }),
  get: (id: string) => api.get(`/users/${id}`),
  create: (data: { email: string; password: string; full_name: string; role_id?: string }) =>
    api.post('/users', data),
  update: (id: string, data: { email?: string; password?: string; full_name?: string; role_id?: string; is_active?: boolean }) =>
    api.put(`/users/${id}`, data),
  delete: (id: string) => api.delete(`/users/${id}`),
  me: () => api.get('/me'),
  updateSettings: (data: { email_notifications: boolean; new_message_alerts: boolean; campaign_updates: boolean }) =>
    api.put('/me/settings', data),
  changePassword: (data: { current_password: string; new_password: string }) =>
    api.put('/me/password', data),
  updateAvailability: (isAvailable: boolean) =>
    api.put('/me/availability', { is_available: isAvailable }),
  listMyOrganizations: () => api.get('/me/organizations'),
}

export const apiKeysService = {
  list: (params?: { search?: string; page?: number; limit?: number }) =>
    api.get<{ api_keys: any[]; total?: number }>('/api-keys', { params }),
  get: (id: string) => api.get(`/api-keys/${id}`),
  create: (data: { name: string; expires_at?: string }) =>
    api.post('/api-keys', data),
  update: (id: string, data: { is_active?: boolean }) =>
    api.put(`/api-keys/${id}`, data),
  delete: (id: string) => api.delete(`/api-keys/${id}`)
}

export interface FlowEncryptionStatus {
  configured: boolean
  public_key?: string
  meta_status?: string
  meta_key_matches: boolean
  meta_status_error?: string
}

export const accountsService = {
  list: () => api.get('/accounts'),
  getFlowEncryption: (id: string) =>
    api.get<{ data: FlowEncryptionStatus }>(`/accounts/${id}/flow-encryption`),
  setupFlowEncryption: (id: string, rotate = false) =>
    api.post<{ data: FlowEncryptionStatus }>(`/accounts/${id}/flow-encryption`, { rotate }),
}

export const contactsService = {
  list: (params?: { search?: string; page?: number; limit?: number; tags?: string }) =>
    api.get('/contacts', { params }),
  get: (id: string) => api.get(`/contacts/${id}`),
  create: (data: any) => api.post('/contacts', data),
  update: (id: string, data: any) => api.put(`/contacts/${id}`, data),
  delete: (id: string) => api.delete(`/contacts/${id}`),
  assign: (id: string, userId: string | null) =>
    api.put(`/contacts/${id}/assign`, { user_id: userId }),
  updateTags: (id: string, tags: string[]) =>
    api.put(`/contacts/${id}/tags`, { tags }),
  getSessionData: (id: string) => api.get(`/contacts/${id}/session-data`),
  markRead: (id: string) => api.post(`/contacts/${encodeURIComponent(id)}/mark-read`),
  sendTyping: (id: string, account?: string | null) =>
    api.post(`/contacts/${encodeURIComponent(id)}/typing`, account ? { whatsapp_account: account } : undefined),
  closeAgentSession: (id: string) =>
    api.post(`/contacts/${encodeURIComponent(id)}/close-agent-session`),
}

// Generic Import/Export Service
export interface ExportColumn {
  key: string
  label: string
}

export interface ExportConfig {
  table: string
  columns: ExportColumn[]
  default_columns: string[]
}

export interface ImportConfig {
  table: string
  required_columns: ExportColumn[]
  optional_columns: ExportColumn[]
  unique_column: string
}

export interface ImportResult {
  created: number
  updated: number
  skipped: number
  errors: number
  messages: string[]
}

export const dataService = {
  // Get export configuration for a table
  getExportConfig: (table: string) => api.get<ExportConfig>(`/export/${table}/config`),

  // Get import configuration for a table
  getImportConfig: (table: string) => api.get<ImportConfig>(`/import/${table}/config`),

  // Export data - returns CSV blob
  exportData: async (table: string, columns?: string[], filters?: Record<string, string>) => {
    const response = await api.post('/export', { table, columns, filters }, {
      responseType: 'blob'
    })
    return response
  },

  // Import data from CSV file
  importData: (table: string, file: File, updateOnDuplicate?: boolean, columnMapping?: Record<string, string>) => {
    const formData = new FormData()
    formData.append('file', file)
    formData.append('table', table)
    if (updateOnDuplicate) {
      formData.append('update_on_duplicate', 'true')
    }
    if (columnMapping) {
      formData.append('column_mapping', JSON.stringify(columnMapping))
    }
    return api.post<ImportResult>('/import', formData, {
      headers: { 'Content-Type': 'multipart/form-data' }
    })
  }
}

export const messagesService = {
  list: (contactId: string, params?: { page?: number; limit?: number; before_id?: string; account?: string }) =>
    api.get(`/contacts/${contactId}/messages`, { params }),
  send: (
    contactId: string,
    data: {
      type: string
      content: any
      reply_to_message_id?: string
      whatsapp_account?: string
      // Interactive payload. Mirrors backend InteractiveContent.
      interactive?: {
        type: 'button' | 'cta_url' | 'list' | 'voice_call' | 'flow'
        body: string
        buttons?: Array<{ id: string; title: string }>
        button_text?: string
        url?: string
        // voice_call only
        display_text?: string
        ttl_minutes?: number
        // flow only
        flow_id?: string
        first_screen?: string
        header?: string
      }
    },
  ) => api.post(`/contacts/${contactId}/messages`, data),
  sendTemplate: (contactId: string, data: { template_name: string; template_params?: Record<string, string>; header_params?: Record<string, string>; button_params?: Record<string, string>; account_name?: string }, headerFile?: File) => {
    if (headerFile) {
      const formData = new FormData()
      formData.append('contact_id', contactId)
      formData.append('template_name', data.template_name)
      if (data.template_params) {
        formData.append('template_params', JSON.stringify(data.template_params))
      }
      if (data.header_params) {
        formData.append('header_params', JSON.stringify(data.header_params))
      }
      if (data.button_params) {
        formData.append('button_params', JSON.stringify(data.button_params))
      }
      if (data.account_name) {
        formData.append('account_name', data.account_name)
      }
      formData.append('header_file', headerFile)
      return api.post('/messages/template', formData, {
        headers: { 'Content-Type': 'multipart/form-data' }
      })
    }
    return api.post('/messages/template', { contact_id: contactId, ...data })
  },
  sendReaction: (contactId: string, messageId: string, emoji: string) =>
    api.post(`/contacts/${contactId}/messages/${messageId}/reaction`, { emoji })
}

export type ChatAIMode =
  | 'suggest_reply'
  | 'rewrite'
  | 'summarize'
  | 'translate'
  | 'quick_replies'
  | 'suggest_tags_fields'
  | 'suggest_template'

export type ChatAIRewriteTone = 'friendly' | 'professional' | 'shorter' | 'grammar' | 'whatsapp'

export interface ChatAIAssistRequest {
  mode: ChatAIMode
  draft?: string
  tone?: ChatAIRewriteTone
  text?: string
  target_language?: string
  message_id?: string
}

export interface ChatAITextResult {
  text: string
  target_language?: string
}

export interface ChatAISummary {
  summary: string
  key_points: string[]
  open_questions: string[]
  sentiment: 'positive' | 'neutral' | 'frustrated' | 'angry'
}

export interface ChatAIFieldSuggestion {
  key: string
  current: string
  value: string
}

export interface ChatAITagsFieldsResult {
  tags: string[]
  fields: ChatAIFieldSuggestion[]
}

export interface ChatAITemplateSuggestion {
  template_id: string
  template_name: string
  display_name: string
  params: Record<string, string>
  reason: string
}

export interface OpenRouterModel {
  id: string
  name: string
  context_length: number
  supports_json: boolean
  recommended: boolean
}

export const aiProvidersService = {
  openRouterFreeModels: () =>
    api.get<{ data: { models: OpenRouterModel[] } }>('/ai/openrouter/free-models'),
}

const chatAITimeout = 120000

export const chatAIService = {
  assist: <T = any>(contactId: string, body: ChatAIAssistRequest) =>
    api.post<{ data: T }>(`/contacts/${encodeURIComponent(contactId)}/ai-assist`, body, { timeout: chatAITimeout }),
  transcribe: (messageId: string, force = false) =>
    api.post<{ data: { text: string } }>(`/messages/${encodeURIComponent(messageId)}/transcribe`, undefined, {
      timeout: chatAITimeout,
      params: force ? { force: true } : undefined,
    }),
}

export const templatesService = {
  list: (params?: { status?: string; category?: string; account?: string; search?: string; page?: number; limit?: number }) =>
    api.get<{ templates: any[]; total?: number }>('/templates', { params }),
  get: (id: string) => api.get(`/templates/${id}`),
  uploadMedia: (accountName: string, file: File) => {
    const formData = new FormData()
    formData.append('file', file)
    formData.append('account', accountName)
    return api.post('/templates/upload-media', formData, {
      headers: { 'Content-Type': 'multipart/form-data' }
    })
  },
  generate: (prompt: string, whatsappAccount?: string) =>
    api.post<{ data: TemplateDraft }>('/templates/generate', { prompt, whatsapp_account: whatsappAccount || '' }, { timeout: 120000 })
}

export interface TemplateDraft {
  whatsapp_account: string
  name: string
  display_name: string
  language: string
  category: string
  header_type: string
  header_content: string
  body_content: string
  footer_content: string
  buttons: any[]
  sample_values: { component: string; index: number; value: string }[]
  add_security_recommendation: boolean
  code_expiration_minutes: number
  notes?: string
}

export const flowsService = {
  list: (params?: { account?: string; search?: string; page?: number; limit?: number }) =>
    api.get<{ flows: any[]; total?: number }>('/flows', { params }),
  get: (id: string) => api.get<{ flow: any }>(`/flows/${id}`),
  create: (data: any) => api.post('/flows', data),
  update: (id: string, data: any) => api.put(`/flows/${id}`, data),
  delete: (id: string) => api.delete(`/flows/${id}`),
  saveToMeta: (id: string) => api.post(`/flows/${id}/save-to-meta`),
  publish: (id: string) => api.post(`/flows/${id}/publish`),
  duplicate: (id: string) => api.post(`/flows/${id}/duplicate`),
  sync: (whatsappAccount?: string) =>
    api.post('/flows/sync', whatsappAccount ? { whatsapp_account: whatsappAccount } : {}, { timeout: 120000 }),
  generate: (data: { prompt: string; whatsapp_account?: string }) =>
    api.post<{ name: string; category: string; screens: any[] }>('/flows/generate', data, { timeout: 120000 }),
}

export const campaignsService = {
  list: (params?: { status?: string; from?: string; to?: string; search?: string; page?: number; limit?: number }) =>
    api.get('/campaigns', { params }),
  get: (id: string) => api.get(`/campaigns/${id}`),
  create: (data: any) => api.post('/campaigns', data),
  update: (id: string, data: any) => api.put(`/campaigns/${id}`, data),
  delete: (id: string) => api.delete(`/campaigns/${id}`),
  start: (id: string) => api.post(`/campaigns/${id}/start`),
  pause: (id: string) => api.post(`/campaigns/${id}/pause`),
  cancel: (id: string) => api.post(`/campaigns/${id}/cancel`),
  retryFailed: (id: string) => api.post(`/campaigns/${id}/retry-failed`),
  // Recipients
  getRecipients: (id: string) => api.get(`/campaigns/${id}/recipients`),
  addRecipients: (id: string, recipients: Array<{ phone_number: string; recipient_name?: string; template_params?: Record<string, any> }>) =>
    api.post(`/campaigns/${id}/recipients/import`, { recipients }),
  deleteRecipient: (campaignId: string, recipientId: string) =>
    api.delete(`/campaigns/${campaignId}/recipients/${recipientId}`),
  // Media
  uploadMedia: (campaignId: string, file: File) => {
    const formData = new FormData()
    formData.append('file', file)
    return api.post(`/campaigns/${campaignId}/media`, formData, {
      headers: { 'Content-Type': 'multipart/form-data' }
    })
  },
  getMedia: (campaignId: string) =>
    api.get(`/campaigns/${campaignId}/media`, { responseType: 'arraybuffer' })
}

export const chatbotService = {
  // Settings
  getSettings: () => api.get('/chatbot/settings'),
  updateSettings: (data: any) => api.put('/chatbot/settings', data),

  // Keywords
  listKeywords: (params?: { search?: string; page?: number; limit?: number }) =>
    api.get<{ rules: any[]; total?: number }>('/chatbot/keywords', { params }),
  getKeyword: (id: string) => api.get(`/chatbot/keywords/${id}`),
  createKeyword: (data: any) => api.post('/chatbot/keywords', data),
  updateKeyword: (id: string, data: any) => api.put(`/chatbot/keywords/${id}`, data),
  deleteKeyword: (id: string) => api.delete(`/chatbot/keywords/${id}`),

  // Flows
  listFlows: (params?: { search?: string; page?: number; limit?: number; whatsapp_account?: string }) =>
    api.get<{ flows: any[]; total?: number }>('/chatbot/flows', { params }),
  getFlow: (id: string) => api.get(`/chatbot/flows/${id}`),
  createFlow: (data: any) => api.post('/chatbot/flows', data),
  generateFlow: (data: { prompt: string; whatsapp_account?: string }) =>
    api.post<{
      name?: string
      description?: string
      trigger_keywords?: string[]
      graph: ChatFlowGraph
    }>('/chatbot/flows/generate', data),
  updateFlow: (id: string, data: any) => api.put(`/chatbot/flows/${id}`, data),
  publishFlow: (id: string) => api.post(`/chatbot/flows/${id}/publish`),
  getFlowAnalytics: (id: string) => api.get(`/chatbot/flows/${id}/analytics`),
  deleteFlow: (id: string) => api.delete(`/chatbot/flows/${id}`),
  uploadMedia: (file: File) => {
    const formData = new FormData()
    formData.append('file', file)
    return api.post('/chatbot/media', formData, {
      headers: { 'Content-Type': 'multipart/form-data' },
    })
  },

  // Sessions
  listSessions: (params?: { status?: string; whatsapp_account?: string; flow_id?: string }) =>
    api.get<{ sessions: any[] }>('/chatbot/sessions', { params }),
  countSessions: (params?: { status?: string; whatsapp_account?: string }) =>
    api.get<{ count: number }>('/chatbot/sessions/count', { params }),
  getSession: (id: string) => api.get(`/chatbot/sessions/${id}`),

  // AI Contexts
  listAIContexts: (params?: { search?: string; page?: number; limit?: number }) =>
    api.get<{ contexts: any[]; total?: number }>('/chatbot/ai-contexts', { params }),
  getAIContext: (id: string) => api.get(`/chatbot/ai-contexts/${id}`),
  createAIContext: (data: any) => api.post('/chatbot/ai-contexts', data),
  updateAIContext: (id: string, data: any) => api.put(`/chatbot/ai-contexts/${id}`, data),
  deleteAIContext: (id: string) => api.delete(`/chatbot/ai-contexts/${id}`),

  // Agent Transfers
  listTransfers: (params?: {
    status?: string
    agent_id?: string
    team_id?: string
    limit?: number
    offset?: number
    include?: string // 'all' | 'contact,agent,team' etc.
  }) => api.get('/chatbot/transfers', { params }),
  createTransfer: (data: {
    contact_id: string
    whatsapp_account: string
    agent_id?: string
    notes?: string
    source?: string
  }) => api.post('/chatbot/transfers', data),
  pickNextTransfer: () => api.post('/chatbot/transfers/pick'),
  resumeTransfer: (id: string) => api.put(`/chatbot/transfers/${id}/resume`),
  assignTransfer: (id: string, agentId: string | null, teamId?: string | null) =>
    api.put(`/chatbot/transfers/${id}/assign`, { agent_id: agentId, team_id: teamId })
}

export interface CannedResponseButton {
  id: string
  title: string
  type?: 'reply' | 'url' | 'phone' | 'voice_call' | 'flow'
  url?: string
  phone_number?: string
  ttl_minutes?: number
  flow_id?: string
  screen?: string
}

export interface CannedResponse {
  id: string
  name: string
  shortcut: string
  content: string
  category: string
  is_active: boolean
  usage_count: number
  buttons?: CannedResponseButton[]
  created_at: string
  updated_at: string
}

interface CannedResponseUpsertPayload {
  name?: string
  shortcut?: string
  content?: string
  category?: string
  is_active?: boolean
  buttons?: CannedResponseButton[]
}

export const cannedResponsesService = {
  list: (params?: { category?: string; search?: string; active_only?: string; page?: number; limit?: number }) =>
    api.get<{ canned_responses: CannedResponse[]; total?: number }>('/canned-responses', { params }),
  get: (id: string) => api.get<CannedResponse>(`/canned-responses/${id}`),
  create: (data: CannedResponseUpsertPayload & { name: string; content: string }) =>
    api.post('/canned-responses', data),
  update: (id: string, data: CannedResponseUpsertPayload) =>
    api.put(`/canned-responses/${id}`, data),
  delete: (id: string) => api.delete(`/canned-responses/${id}`),
  use: (id: string) => api.post(`/canned-responses/${id}/use`),
  generate: (prompt: string) =>
    api.post<{ data: CannedResponseDraft }>('/canned-responses/generate', { prompt }, { timeout: 120000 })
}

export interface CannedResponseDraft {
  name: string
  shortcut: string
  category: string
  content: string
  buttons: CannedResponse['buttons']
  notes?: string
}

export const agentAnalyticsService = {
  getSummary: (params?: { from?: string; to?: string; agent_id?: string }) =>
    api.get('/analytics/agents', { params })
}

// Meta WhatsApp Analytics Types
export type MetaAnalyticsType =
  | 'analytics'
  | 'conversation_analytics'
  | 'pricing_analytics'
  | 'template_analytics'
  | 'call_analytics'

export type MetaGranularity = 'HALF_HOUR' | 'DAY' | 'MONTH'

export interface MetaAnalyticsAccount {
  id: string
  name: string
  phone_id: string
}

export interface MetaMessagingDataPoint {
  start: number
  end: number
  sent: number
  delivered: number
}

interface MetaConversationDataPoint {
  start: number
  end: number
  conversation: number
  conversation_type: string
  conversation_direction: string
  conversation_category: string
  cost: number
}

export interface MetaPricingDataPoint {
  start: number
  end: number
  volume: number
  cost: number
  country?: string              // Country code (IN, US, etc.)
  pricing_type?: string         // FREE_CUSTOMER_SERVICE, FREE_ENTRY_POINT, REGULAR
  pricing_category?: string     // MARKETING, UTILITY, AUTHENTICATION, SERVICE, etc.
  tier?: string                 // Pricing tier
}

interface MetaTemplateCostItem {
  type: string    // amount_spent, cost_per_delivered, cost_per_url_button_click
  value?: number  // The cost value
}

interface MetaTemplateClickItem {
  type: string           // quick_reply_button, unique_url_button
  button_content: string // The button text
  count: number          // Number of clicks
}

export interface MetaTemplateDataPoint {
  start: number
  end: number
  template_id: string
  sent: number
  delivered: number
  read: number
  replied?: number
  clicked?: MetaTemplateClickItem[]  // Array of button click details
  cost?: MetaTemplateCostItem[]
}

export interface MetaCallDataPoint {
  start: number
  end: number
  count: number
  cost: number
  average_duration: number
  direction?: string // USER_INITIATED or BUSINESS_INITIATED
}

interface MetaAnalyticsData {
  id: string
  analytics?: {
    granularity: string
    data_points: MetaMessagingDataPoint[]
  }
  conversation_analytics?: {
    granularity: string
    data_points: MetaConversationDataPoint[]
  }
  pricing_analytics?: {
    granularity: string
    data_points: MetaPricingDataPoint[]
  }
  template_analytics?: {
    granularity: string
    data_points: MetaTemplateDataPoint[]
  }
  call_analytics?: {
    granularity: string
    data_points: MetaCallDataPoint[]
  }
}

export interface MetaAnalyticsResponse {
  account_id: string
  account_name: string
  data: MetaAnalyticsData | null
  template_names?: Record<string, string> // meta_template_id -> template name
  currency?: string // ISO 4217 code the WABA is billed in, from Meta
}

export const metaAnalyticsService = {
  get: (params: {
    account_id?: string
    analytics_type: MetaAnalyticsType
    start: string
    end: string
    granularity?: MetaGranularity
    template_ids?: string
  }) => api.get<{ accounts: MetaAnalyticsResponse[]; cached: boolean }>('/analytics/meta', { params }),

  getAccounts: () => api.get<{ accounts: MetaAnalyticsAccount[] }>('/analytics/meta/accounts'),

  refresh: () => api.post('/analytics/meta/refresh')
}

// Dashboard Widgets (customizable analytics)
export interface DashboardWidget {
  id: string
  name: string
  description: string
  data_source: string
  metric: string
  field: string
  filters: Array<{ field: string; operator: string; value: string }>
  display_type: string
  chart_type: string
  group_by_field: string
  show_change: boolean
  color: string
  size: string
  display_order: number
  grid_x: number
  grid_y: number
  grid_w: number
  grid_h: number
  config: Record<string, any>
  is_shared: boolean
  is_default: boolean
  is_owner: boolean
  created_by: string
  created_at: string
  updated_at: string
}

export interface WidgetData {
  widget_id: string
  value: number
  change: number
  prev_value: number
  chart_data: Array<{ label: string; value: number }>
  data_points: Array<{ label: string; value: number; color?: string }>
  grouped_series?: {
    labels: string[]
    datasets: Array<{ label: string; data: number[] }>
  }
  table_rows?: Array<{
    id: string
    label: string
    sub_label: string
    status: string
    direction?: string
    created_at: string
  }>
  leaderboard?: Array<{ rank: number; label: string; value: number }>
  heatmap?: { x_labels: string[]; y_labels: string[]; cells: number[][] }
  funnel?: Array<{ stage: string; value: number }>
  kpis?: Array<{ label: string; value: number; change?: number; unit?: string }>
  alerts?: Array<{ severity: string; title: string; subtitle: string; at: string; href?: string }>
  queue_items?: Array<{ id: string; contact_id: string; label: string; source: string; wait_mins: number; transferred_at: string }>
  agents?: Array<{ id: string; name: string; is_available: boolean; active_transfers: number }>
  stub?: boolean
  live?: boolean
}

interface DataSourceInfo {
  name: string
  label: string
  fields: string[]
}

export interface LayoutItem {
  id: string
  grid_x: number
  grid_y: number
  grid_w: number
  grid_h: number
}

export const widgetsService = {
  list: () => api.get<{ widgets: DashboardWidget[] }>('/widgets'),
  create: (data: {
    name: string
    description?: string
    data_source: string
    metric: string
    field?: string
    filters?: Array<{ field: string; operator: string; value: string }>
    display_type?: string
    chart_type?: string
    group_by_field?: string
    show_change?: boolean
    color?: string
    size?: string
    config?: Record<string, any>
    is_shared?: boolean
  }) => api.post<DashboardWidget>('/widgets', data),
  update: (id: string, data: Partial<{
    name: string
    description: string
    data_source: string
    metric: string
    field: string
    filters: Array<{ field: string; operator: string; value: string }>
    display_type: string
    chart_type: string
    group_by_field: string
    show_change: boolean
    color: string
    size: string
    config: Record<string, any>
    is_shared: boolean
  }>) => api.put<DashboardWidget>(`/widgets/${id}`, data),
  delete: (id: string) => api.delete(`/widgets/${id}`),
  getAllData: (params?: { from?: string; to?: string }) =>
    api.get<{ data: Record<string, WidgetData> }>('/widgets/data', { params }),
  getDataSources: () => api.get<{
    data_sources: DataSourceInfo[]
    metrics: string[]
    display_types: string[]
    operators: Array<{ value: string; label: string }>
  }>('/widgets/data-sources'),
  saveLayout: (layout: LayoutItem[]) =>
    api.post('/widgets/layout', { layout }),
  generate: (data: { prompt: string }) =>
    api.post<{
      name: string
      description: string
      data_source: string
      metric: string
      field: string
      filters: Array<{ field: string; operator: string; value: string }>
      display_type: string
      chart_type: string
      group_by_field: string
      show_change: boolean
      color: string
      size: string
      config: Record<string, any>
    }>('/widgets/generate', data, { timeout: 120000 })
}

export const organizationService = {
  getSettings: () => api.get('/org/settings'),
  updateSettings: (data: {
    mask_phone_numbers?: boolean
    timezone?: string
    date_format?: string
    time_format?: '12h' | '24h'
    name?: string
    calling_enabled?: boolean
    max_call_duration?: number
    transfer_timeout_secs?: number
    hold_music_file?: string
    ringback_file?: string
    meta_app_id?: string
    meta_config_id?: string
    meta_app_secret?: string
    smtp_host?: string
    smtp_port?: number
    smtp_username?: string
    smtp_password?: string
    smtp_from?: string
    smtp_security?: string
  }) => api.put('/org/settings', data),
  uploadOrgAudio: (file: File, type: 'hold_music' | 'ringback') => {
    const formData = new FormData()
    formData.append('file', file)
    return api.post(`/org/audio?type=${type}`, formData, {
      headers: { 'Content-Type': 'multipart/form-data' }
    })
  }
}

export type MetaAppShareMode = 'off' | 'global' | 'selected'

export interface SharedMetaAppSettings {
  app_id: string
  config_id: string
  share_mode: MetaAppShareMode
  shared_org_ids: string[]
  has_app_secret: boolean
  updated_by_id?: string
}

export const sharedMetaAppService = {
  get: () => api.get<{ data?: SharedMetaAppSettings } & SharedMetaAppSettings>('/admin/meta-app'),
  update: (data: {
    app_id?: string
    config_id?: string
    app_secret?: string
    share_mode?: MetaAppShareMode
    shared_org_ids?: string[]
  }) => api.put('/admin/meta-app', data),
}

export interface ContactList {
  id: string
  name: string
  description?: string
  member_count?: number
}

export const contactListsService = {
  list: () => api.get('/contact-lists'),
  create: (data: { name: string; description?: string }) => api.post('/contact-lists', data),
  update: (id: string, data: { name?: string; description?: string }) => api.put(`/contact-lists/${id}`, data),
  remove: (id: string) => api.delete(`/contact-lists/${id}`),
}

export interface GoogleCredential {
  id: string
  label: string
  client_email?: string
  has_json: boolean
  auth_type: 'service_account' | 'oauth'
  scopes?: string[]
}

export interface Passkey {
  id: string
  name: string
  credential_id: string
  rp_id: string
  created_at: string
  last_used_at?: string
  current_site: boolean
}

export const passkeysService = {
  list: () => api.get('/me/passkeys'),
  beginRegistration: () => api.post('/me/passkeys/register/begin', {}),
  finishRegistration: (name: string, credential: Record<string, unknown>) =>
    api.post('/me/passkeys/register/finish', { name, credential }),
  remove: (id: string) => api.delete(`/me/passkeys/${id}`),
}

export interface GoogleOAuthStatus {
  enabled: boolean
  source: '' | 'config' | 'sso'
  redirect_uri: string
}

export const googleCredentialsService = {
  list: () => api.get('/google-credentials'),
  create: (data: { label: string; service_json: string }) => api.post('/google-credentials', data),
  remove: (id: string) => api.delete(`/google-credentials/${id}`),
  oauthStatus: () => api.get('/google-credentials/oauth'),
  startOAuth: (label: string) => api.post('/google-credentials/oauth/start', { label }),
}

// Organizations
export interface Organization {
  id: string
  name: string
  slug?: string
  member_count?: number
  created_at: string
}

export const organizationsService = {
  list: () => api.get<{ organizations: Organization[] }>('/organizations'),
  create: (data: { name: string }) => api.post('/organizations', data),
  update: (id: string, data: { name: string }) => api.put(`/organizations/${id}`, data),
  delete: (id: string) => api.delete(`/organizations/${id}`),
  // Members
  addMember: (data: { user_id?: string; email?: string; role_id?: string }) =>
    api.post('/organizations/members', data),
}

export interface Webhook {
  id: string
  name: string
  url: string
  events: string[]
  headers: Record<string, string>
  is_active: boolean
  has_secret: boolean
  created_at: string
  updated_at: string
}

export interface WebhookEvent {
  value: string
  label: string
  description: string
}

export interface Team {
  id: string
  name: string
  description: string
  assignment_strategy: 'round_robin' | 'load_balanced' | 'manual'
  per_agent_timeout_secs: number
  is_active: boolean
  member_count: number
  members?: TeamMember[]
  created_by_id?: string
  created_by_name?: string
  updated_by_id?: string
  updated_by_name?: string
  created_at: string
  updated_at: string
}

export interface TeamMember {
  id: string
  team_id?: string
  user_id: string
  role: 'manager' | 'agent'
  last_assigned_at: string | null
  // Flat structure from API
  full_name: string
  email: string
  is_available: boolean
  // Optional nested user for local additions
  user?: {
    id: string
    full_name: string
    email: string
    is_available: boolean
  }
}

export const teamsService = {
  list: (params?: { search?: string; page?: number; limit?: number }) =>
    api.get<{ teams: Team[] }>('/teams', { params }),
  get: (id: string) => api.get<{ team: Team }>(`/teams/${id}`),
  create: (data: {
    name: string
    description?: string
    assignment_strategy?: 'round_robin' | 'load_balanced' | 'manual'
    per_agent_timeout_secs?: number
  }) => api.post<{ team: Team }>('/teams', data),
  update: (id: string, data: {
    name?: string
    description?: string
    assignment_strategy?: 'round_robin' | 'load_balanced' | 'manual'
    per_agent_timeout_secs?: number
    is_active?: boolean
  }) => api.put<{ team: Team }>(`/teams/${id}`, data),
  delete: (id: string) => api.delete(`/teams/${id}`),
  // Members
  listMembers: (teamId: string) => api.get<{ members: TeamMember[] }>(`/teams/${teamId}/members`),
  addMember: (teamId: string, data: { user_id: string; role?: 'manager' | 'agent' }) =>
    api.post<{ member: TeamMember }>(`/teams/${teamId}/members`, data),
  removeMember: (teamId: string, userId: string) =>
    api.delete(`/teams/${teamId}/members/${userId}`)
}

// Audit Logs
export interface AuditLogChange {
  field: string
  old_value: any
  new_value: any
}

export interface AuditLogEntry {
  id: string
  resource_type: string
  resource_id: string
  user_id: string
  user_name: string
  action: 'created' | 'updated' | 'deleted'
  changes: AuditLogChange[]
  created_at: string
}

export const auditLogsService = {
  get: (id: string) =>
    api.get<AuditLogEntry>(`/audit-logs/${id}`),
  list: (params?: {
    resource_type?: string
    resource_id?: string
    user_id?: string
    action?: string
    from?: string
    to?: string
    page?: number
    limit?: number
  }) =>
    api.get<{ audit_logs: AuditLogEntry[]; total: number }>('/audit-logs', { params }),
}

export const webhooksService = {
  list: (params?: { search?: string; page?: number; limit?: number }) =>
    api.get<{ webhooks: Webhook[]; available_events: WebhookEvent[]; total?: number }>('/webhooks', { params }),
  get: (id: string) => api.get<Webhook>(`/webhooks/${id}`),
  create: (data: {
    name: string
    url: string
    events: string[]
    headers?: Record<string, string>
    secret?: string
  }) => api.post<Webhook>('/webhooks', data),
  update: (id: string, data: {
    name?: string
    url?: string
    events?: string[]
    headers?: Record<string, string>
    secret?: string
    is_active?: boolean
  }) => api.put<Webhook>(`/webhooks/${id}`, data),
  delete: (id: string) => api.delete(`/webhooks/${id}`),
  test: (id: string) => api.post(`/webhooks/${id}/test`)
}

export interface CustomAction {
  id: string
  name: string
  icon: string
  action_type: 'webhook' | 'url' | 'javascript'
  config: {
    url?: string
    method?: string
    headers?: Record<string, string>
    body?: string
    open_in_new_tab?: boolean
    code?: string
  }
  is_active: boolean
  display_order: number
  created_at: string
  updated_at: string
}

export interface ActionResult {
  success: boolean
  message?: string
  redirect_url?: string
  clipboard?: string
  toast?: {
    message: string
    type: 'success' | 'error' | 'info' | 'warning'
  }
  data?: Record<string, any>
}

export const customActionsService = {
  list: (params?: { search?: string; page?: number; limit?: number }) =>
    api.get<{ custom_actions: CustomAction[]; total?: number }>('/custom-actions', { params }),
  get: (id: string) => api.get<CustomAction>(`/custom-actions/${id}`),
  create: (data: {
    name: string
    icon?: string
    action_type: 'webhook' | 'url' | 'javascript'
    config: Record<string, any>
    is_active?: boolean
    display_order?: number
  }) => api.post<CustomAction>('/custom-actions', data),
  update: (id: string, data: {
    name?: string
    icon?: string
    action_type?: 'webhook' | 'url' | 'javascript'
    config?: Record<string, any>
    is_active?: boolean
    display_order?: number
  }) => api.put<CustomAction>(`/custom-actions/${id}`, data),
  delete: (id: string) => api.delete(`/custom-actions/${id}`),
  execute: (id: string, contactId: string) =>
    api.post<ActionResult>(`/custom-actions/${id}/execute`, { contact_id: contactId }),
  generate: (prompt: string) =>
    api.post<{ data: CustomActionDraft }>('/custom-actions/generate', { prompt }, { timeout: 120000 })
}

export interface CustomActionDraft {
  name: string
  icon: string
  action_type: 'webhook' | 'url' | 'javascript'
  config: Record<string, any>
  notes?: string
}

// Roles and Permissions
export interface Permission {
  id: string
  resource: string
  action: string
  description: string
  key: string // "resource:action"
}

export interface Role {
  id: string
  name: string
  description: string
  is_system: boolean
  is_default: boolean
  permissions: string[] // ["resource:action", ...]
  user_count: number
  created_at: string
  updated_at: string
}

export const rolesService = {
  list: (params?: { search?: string; page?: number; limit?: number }) =>
    api.get<{ roles: Role[] }>('/roles', { params }),
  get: (id: string) => api.get<Role>(`/roles/${id}`),
  create: (data: { name: string; description?: string; is_default?: boolean; permissions: string[] }) =>
    api.post<Role>('/roles', data),
  update: (id: string, data: { name?: string; description?: string; is_default?: boolean; permissions?: string[] }) =>
    api.put<Role>(`/roles/${id}`, data),
  delete: (id: string) => api.delete(`/roles/${id}`)
}

export const permissionsService = {
  list: () => api.get<{ permissions: Permission[] }>('/permissions')
}

// Tags
export interface Tag {
  name: string
  color: string
  created_at: string
  updated_at: string
}

export const tagsService = {
  list: (params?: { search?: string; page?: number; limit?: number }) =>
    api.get<{ tags: Tag[]; total?: number; page?: number; limit?: number }>('/tags', { params }),
  create: (data: { name: string; color?: string }) =>
    api.post<Tag>('/tags', data),
  update: (name: string, data: { name?: string; color?: string }) =>
    api.put<Tag>(`/tags/${encodeURIComponent(name)}`, data),
  delete: (name: string) => api.delete(`/tags/${encodeURIComponent(name)}`)
}

// Conversation Notes
export interface ConversationNote {
  id: string
  contact_id: string
  created_by_id: string
  created_by_name: string
  content: string
  created_at: string
  updated_at: string
}

export const notesService = {
  list: (contactId: string, params?: { limit?: number; before?: string }) =>
    api.get<{ notes: ConversationNote[]; total: number; has_more: boolean }>(`/contacts/${contactId}/notes`, { params }),
  create: (contactId: string, data: { content: string }) =>
    api.post<ConversationNote>(`/contacts/${contactId}/notes`, data),
  update: (contactId: string, noteId: string, data: { content: string }) =>
    api.put<ConversationNote>(`/contacts/${contactId}/notes/${noteId}`, data),
  delete: (contactId: string, noteId: string) =>
    api.delete(`/contacts/${contactId}/notes/${noteId}`)
}

// Calling - Call Logs & IVR Flows
export interface CallLogAICallArtifacts {
  transcript?: Array<{ role: string; text: string }>
  recordings?: {
    user?: string
    assistant?: string
    stereo?: string
  }
}

export interface CallLog {
  id: string
  organization_id: string
  whatsapp_account: string
  contact_id: string
  whatsapp_call_id: string
  caller_phone: string
  direction: 'incoming' | 'outgoing'
  status: 'ringing' | 'answered' | 'completed' | 'missed' | 'rejected' | 'failed' | 'initiating' | 'accepted' | 'transferring'
  duration: number
  ivr_flow_id?: string
  ivr_path?: Record<string, any>
  agent_id?: string
  started_at?: string
  answered_at?: string
  ended_at?: string
  disconnected_by?: 'client' | 'agent' | 'system'
  error_message?: string
  recording_s3_key?: string
  recording_duration?: number
  ai_call_artifacts?: CallLogAICallArtifacts
  contact?: {
    id: string
    phone_number: string
    profile_name: string
  }
  agent?: {
    id: string
    full_name: string
    email: string
  }
  ivr_flow?: IVRFlow
  created_at: string
  updated_at: string
}

// v2 Node-based IVR Flow types
export type IVRNodeType = 'start' | 'greeting' | 'menu' | 'gather' | 'http_callback' | 'transfer' | 'goto_flow' | 'timing' | 'hangup' | 'condition' | 'set_variable' | 'confirm' | 'delay' | 'set_labels' | 'send_message' | 'webhook' | 'language' | 'voicemail' | 'speech_input' | 'ai_voice' | 'ai_call' | 'execute_function' | 'dial_out'

export interface IVRNodePosition {
  x: number
  y: number
}

export interface IVRNode {
  id: string
  type: IVRNodeType
  label: string
  position: IVRNodePosition
  config: Record<string, any>
}

export interface IVREdge {
  from: string
  to: string
  condition: string
}

export interface IVRFlowData {
  version: 2
  nodes: IVRNode[]
  edges: IVREdge[]
  entry_node: string
}

// v2 Node-based Chatbot Flow types. Mirrors IVR's graph shape with a
// chat-specific node-type union. Only types listed in the union are
// implemented today; others land in Phase 3.
export type ChatNodeType =
  | 'start'
  | 'message'
  | 'buttons'
  | 'list'
  | 'end'
  | 'prompt'
  | 'api_call'
  | 'condition'
  | 'timing'
  | 'set_variable'
  | 'ai_response'
  | 'transfer'
  | 'webhook'
  | 'goto_flow'
  | 'whatsapp_flow'
  | 'wa_template'
  | 'set_variable'
  | 'delay'
  | 'reset_session'
  | 'ai_response'
  | 'set_labels'
  | 'disable_bot'
  | 'send_email'
  | 'phonebook'
  | 'google_service'

export interface ChatNode {
  id: string
  type: ChatNodeType
  label: string
  position: IVRNodePosition
  config: Record<string, any>
}

export interface ChatEdge {
  from: string
  to: string
  condition: string
}

export interface ChatFlowGraph {
  version: 2
  nodes: ChatNode[]
  edges: ChatEdge[]
  entry_node: string
}

export interface IVRFlow {
  id: string
  organization_id: string
  whatsapp_account: string
  name: string
  description: string
  is_active: boolean
  is_call_start: boolean
  is_outgoing_end: boolean
  menu: IVRFlowData
  welcome_audio_url: string
  created_at: string
  updated_at: string
}

export interface CallTransfer {
  id: string
  organization_id: string
  call_log_id: string
  whatsapp_call_id: string
  caller_phone: string
  contact_id: string
  whatsapp_account: string
  status: 'waiting' | 'connected' | 'completed' | 'abandoned' | 'no_answer'
  team_id?: string
  agent_id?: string
  initiating_agent_id?: string
  transferred_at: string
  connected_at?: string
  completed_at?: string
  hold_duration: number
  talk_duration: number
  ivr_path?: Record<string, any>
  contact?: {
    id: string
    phone_number: string
    profile_name: string
  }
  agent?: {
    id: string
    full_name: string
    email: string
  }
  initiating_agent?: {
    id: string
    full_name: string
    email: string
  }
  team?: {
    id: string
    name: string
  }
  call_log?: CallLog
  created_at: string
  updated_at: string
}

// Outgoing Calls
export interface CallPermission {
  id: string
  contact_id: string
  whatsapp_account: string
  status: 'pending' | 'accepted' | 'declined' | 'expired'
  message_id?: string
  requested_at: string
  responded_at?: string
  expires_at?: string
}

export const outgoingCallsService = {
  initiate: (data: { contact_id: string; whatsapp_account: string; sdp_offer: string }) =>
    api.post<{ call_log_id: string; sdp_answer: string }>('/calls/outgoing', data),
  hangup: (callLogId: string) =>
    api.post(`/calls/outgoing/${callLogId}/hangup`),
  requestPermission: (data: { contact_id: string; whatsapp_account: string }) =>
    api.post<{ permission_id: string }>('/calls/permission-request', data),
  getPermission: (contactId: string, whatsappAccount: string) =>
    api.get<CallPermission>(`/calls/permission/${contactId}`, { params: { whatsapp_account: whatsappAccount } }),
  getICEServers: () =>
    api.get<{ ice_servers: Array<{ urls: string[]; username?: string; credential?: string }> }>('/calls/ice-servers'),
}

export const callLogsService = {
  list: (params?: { status?: string; account?: string; contact_id?: string; direction?: string; ivr_flow_id?: string; phone?: string; from?: string; to?: string; page?: number; limit?: number }) =>
    api.get<{ call_logs: CallLog[]; total: number }>('/call-logs', { params }),
  get: (id: string) => api.get<CallLog>(`/call-logs/${id}`),
  getRecordingURL: (id: string, kind?: 'main' | 'user' | 'assistant' | 'stereo') =>
    api.get<{ url: string; duration: number; kind?: string }>(`/call-logs/${id}/recording`, {
      params: kind && kind !== 'main' ? { kind } : undefined,
    }),
  hold: (id: string) =>
    api.post<{ status: string }>(`/call-logs/${id}/hold`),
  resume: (id: string) =>
    api.post<{ status: string }>(`/call-logs/${id}/resume`),
}

export const callTransfersService = {
  list: (params?: { status?: string; page?: number; limit?: number }) =>
    api.get<{ call_transfers: CallTransfer[]; total: number }>('/call-transfers', { params }),
  get: (id: string) => api.get<CallTransfer>(`/call-transfers/${id}`),
  connect: (id: string, sdpOffer: string) =>
    api.post<{ sdp_answer: string }>(`/call-transfers/${id}/connect`, { sdp_offer: sdpOffer }),
  hangup: (id: string) =>
    api.post(`/call-transfers/${id}/hangup`),
  initiate: (data: { call_log_id: string; team_id: string; agent_id?: string }) =>
    api.post<{ status: string }>('/call-transfers/initiate', data),
}

export const ivrFlowsService = {
  list: (params?: { search?: string; page?: number; limit?: number }) =>
    api.get<{ ivr_flows: IVRFlow[]; total: number }>('/ivr-flows', { params }),
  get: (id: string) => api.get<IVRFlow>(`/ivr-flows/${id}`),
  generateFlow: (data: { prompt: string; whatsapp_account?: string }) =>
    api.post<{
      name: string
      description: string
      menu: IVRFlowData
    }>('/ivr-flows/generate', data),
  create: (data: { whatsapp_account: string; name: string; description?: string; is_call_start?: boolean; menu: IVRFlowData; welcome_audio_url?: string }) =>
    api.post<IVRFlow>('/ivr-flows', data),
  update: (id: string, data: { name?: string; description?: string; is_active?: boolean; is_call_start?: boolean; is_outgoing_end?: boolean; menu?: IVRFlowData; welcome_audio_url?: string }) =>
    api.put<IVRFlow>(`/ivr-flows/${id}`, data),
  delete: (id: string) => api.delete(`/ivr-flows/${id}`),
  uploadAudio: (file: File) => {
    const formData = new FormData()
    formData.append('file', file)
    return api.post('/ivr-flows/audio', formData, {
      headers: { 'Content-Type': 'multipart/form-data' }
    })
  },
  getAudioUrl: (filename: string) => `${api.defaults.baseURL}/ivr-flows/audio/${encodeURIComponent(filename)}`,
  previewGeminiVoice: (data: { text: string; voice: string }) =>
    api.post('/ivr-flows/voice-preview', data, { responseType: 'blob' }),
}

export interface SetupCandidate {
  path: string
  data_dir: string
  scope: 'user' | 'system' | 'fixed'
  writable: boolean
}

export interface SetupStatus {
  needed: boolean
  ready?: boolean
  url?: string
  version?: string
  listen_port?: number
  config_path?: string
  config_path_fixed?: boolean
  candidates?: SetupCandidate[]
  token_required?: boolean
}

export type CheckStatus = 'pass' | 'warn' | 'fail'

export interface SetupCheck {
  id: string
  status: CheckStatus
  detail: string
}

export type SetupDriver = 'sqlite' | 'postgres' | 'mysql'

export interface SetupDatabase {
  driver: SetupDriver
  path?: string
  host?: string
  port?: number
  user?: string
  password?: string
  name?: string
  ssl_mode?: string
}

export interface SetupDatabaseResult {
  ok: boolean
  database_exists: boolean
  error?: string
}

export interface SetupCompleteRequest {
  config_path: string
  port: number
  database: SetupDatabase
  organization: string
  admin: { full_name: string; email: string; password: string }
}

export interface SetupBackupRef {
  name?: string
  path?: string
}

export interface SetupBackupSummary {
  name: string
  size: number
  manifest: BackupManifest
  target_driver: string
  key_matches: boolean
  key_given: boolean
}

export interface SetupRestoreChoice {
  backup: SetupBackupRef | null
  encryptionKey: string
}

export interface SetupRestoreRequest {
  config_path: string
  port: number
  database: SetupDatabase
  backup: SetupBackupRef
  encryption_key: string
}

export interface SetupRestoreStatus {
  running: boolean
  phase: string
  table?: string
  done: number
  total: number
  finished: boolean
  failed: boolean
  files_failed: boolean
  error?: string
}

export const setupService = {
  status: () => api.get<{ data: SetupStatus }>('/setup/status'),
  checks: (params: { port: number; config_path: string }) =>
    api.get<{ data: { checks: SetupCheck[] } }>('/setup/checks', { params, timeout: 20000 }),
  testDatabase: (db: SetupDatabase) => api.post<{ data: SetupDatabaseResult }>('/setup/database/test', db),
  createDatabase: (db: SetupDatabase) => api.post<{ data: SetupDatabaseResult }>('/setup/database/create', db),
  complete: (req: SetupCompleteRequest) =>
    api.post<{ data: { url: string } }>('/setup/complete', req, { timeout: 600000 }),
  backups: (configPath: string) =>
    api.get<{ data: { dir: string; backups: BackupInfo[] } }>('/setup/backups', { params: { config_path: configPath } }),
  startUpload: (configPath: string, name: string, size: number) =>
    api.post<{ data: { id: string; received: number; chunk_max: number } }>('/setup/backup-uploads', { config_path: configPath, name, size }),
  putChunk: (id: string, offset: number, chunk: Blob) =>
    api.put<{ data: { received: number } }>(`/setup/backup-uploads/${id}`, chunk, {
      params: { offset },
      headers: { 'Content-Type': 'application/octet-stream' },
      timeout: 120000,
    }),
  // The setup server takes only JSON POST bodies, so send an empty object.
  finishUpload: (id: string) => api.post<{ data: BackupInfo }>(`/setup/backup-uploads/${id}/finish`, {}, { timeout: 120000 }),
  cancelUpload: (id: string) => api.delete(`/setup/backup-uploads/${id}`),
  inspectBackup: (req: { config_path: string; database: SetupDatabase; backup: SetupBackupRef; encryption_key: string }) =>
    api.post<{ data: SetupBackupSummary }>('/setup/backup/inspect', req, { timeout: 60000 }),
  restore: (req: SetupRestoreRequest) => api.post<{ data: { started: boolean } }>('/setup/restore', req, { timeout: 120000 }),
  restoreStatus: () => api.get<{ data: SetupRestoreStatus }>('/setup/restore'),
}

export interface ServerSettings {
  port: number
  config_port: number
  config_path: string
  writable: boolean
}

export const serverSettingsService = {
  get: () => api.get<{ data: ServerSettings }>('/admin/server-settings'),
  update: (port: number) => api.put<{ data: ServerSettings }>('/admin/server-settings', { port }),
}

export interface BackupManifest {
  format: number
  app_version: string
  kind: 'manual' | 'pre-restore' | 'cli'
  created_at: string
  source_driver: string
  key_fingerprint: string
  include_files: boolean
  tables: { name: string; rows: number; sha256: string; columns: string[] }[]
  files?: Record<string, { count: number; bytes: number }>
}

export interface BackupInfo {
  name: string
  size: number
  manifest?: BackupManifest
  error?: string
}

export interface BackupJob {
  kind: '' | 'backup' | 'restore'
  running: boolean
  phase: string
  table?: string
  done: number
  total: number
  name?: string
  safety_name?: string
  error?: string
  started_at: string
  finished_at?: string
}

export interface BackupSummary {
  name: string
  size: number
  manifest: BackupManifest
  target_driver: string
  key_matches: boolean
}

export interface MaintenanceStatus {
  active: boolean
  restore?: { running: boolean; phase: string; table?: string; done: number; total: number; finished: boolean; failed: boolean; db_replaced: boolean }
}

const backupPath = (name: string) => `/admin/backups/${encodeURIComponent(name)}`

export const backupsService = {
  list: () => api.get<{ data: { dir: string; backups: BackupInfo[]; job: BackupJob } }>('/admin/backups'),
  create: (includeFiles: boolean) => api.post<{ data: BackupJob }>('/admin/backups', { include_files: includeFiles }),
  job: () => api.get<{ data: BackupJob }>('/admin/backup-job'),
  inspect: (name: string) => api.get<{ data: BackupSummary }>(`${backupPath(name)}/inspect`),
  restore: (name: string) => api.post<{ data: BackupJob }>(`${backupPath(name)}/restore`, { confirm: 'RESTORE' }),
  remove: (name: string) => api.delete(backupPath(name)),
  downloadUrl: (name: string) => `${API_BASE_URL}${backupPath(name)}/download`,
  startUpload: (name: string, size: number) =>
    api.post<{ data: { id: string; received: number; chunk_max: number } }>('/admin/backup-uploads', { name, size }),
  putChunk: (id: string, offset: number, chunk: Blob) =>
    api.put<{ data: { received: number } }>(`/admin/backup-uploads/${id}`, chunk, {
      params: { offset },
      headers: { 'Content-Type': 'application/octet-stream' },
      timeout: 120000,
    }),
  finishUpload: (id: string) => api.post<{ data: BackupInfo }>(`/admin/backup-uploads/${id}/finish`, undefined, { timeout: 120000 }),
  cancelUpload: (id: string) => api.delete(`/admin/backup-uploads/${id}`),
  maintenance: () => api.get<{ data: MaintenanceStatus }>('/maintenance'),
}

export default api
