<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatWhatsAppText } from '@/lib/whatsappFormat'
import {
  Image as ImageIcon,
  Video,
  FileText,
  ExternalLink,
  Phone,
  Copy,
  Reply,
  ListChecks,
  PhoneCall,
  Smartphone,
} from 'lucide-vue-next'

const props = defineProps<{
  headerType: string
  header: string
  body: string
  footer: string
  buttons: any[]
  isAuthentication?: boolean
  addSecurityRecommendation?: boolean
  codeExpirationMinutes?: number
  mediaUrl?: string
}>()

const { t } = useI18n()

const authBody = computed(() => {
  let text = '*123456* is your verification code.'
  if (props.addSecurityRecommendation) text += ' For your security, do not share this code.'
  return text
})

const bodyHtml = computed(() => formatWhatsAppText(props.isAuthentication ? authBody.value : props.body))

const footerText = computed(() => {
  if (!props.isAuthentication) return props.footer
  return props.codeExpirationMinutes ? `This code expires in ${props.codeExpirationMinutes} minutes.` : ''
})

const showHeader = computed(() => !props.isAuthentication && props.headerType && props.headerType !== 'NONE')
const isEmpty = computed(() => !props.isAuthentication && !props.body.trim() && !showHeader.value)

const previewButtons = computed(() => {
  return (props.buttons || []).map((b: any) => {
    let text = b.text || ''
    if (b.type === 'OTP') {
      text = b.otp_type === 'ONE_TAP' ? (b.autofill_text || 'Autofill') : (b.text || 'Copy code')
    }
    return { type: b.type, otpType: b.otp_type, text: text || t('templates.button', 'Button') }
  })
})

function buttonIcon(type: string, otpType?: string) {
  switch (type) {
    case 'URL': return ExternalLink
    case 'PHONE_NUMBER': return Phone
    case 'COPY_CODE': return Copy
    case 'FLOW': return ListChecks
    case 'VOICE_CALL': return PhoneCall
    case 'OTP': return otpType === 'ONE_TAP' || otpType === 'ZERO_TAP' ? Smartphone : Copy
    default: return Reply
  }
}

const mediaIcon = computed(() => {
  switch (props.headerType) {
    case 'VIDEO': return Video
    case 'DOCUMENT': return FileText
    default: return ImageIcon
  }
})
</script>

<template>
  <div class="rounded-xl border overflow-hidden" data-testid="template-live-preview">
    <div class="flex items-center gap-2 bg-[#202c33] light:bg-[#008069] px-3 py-2.5 text-white">
      <div class="h-7 w-7 rounded-full bg-white/20 flex items-center justify-center text-xs font-semibold">W</div>
      <div class="leading-tight">
        <p class="text-xs font-medium">{{ t('templates.previewBusiness', 'Your business') }}</p>
        <p class="text-[10px] text-white/70">{{ t('templates.previewSubtitle', 'WhatsApp preview') }}</p>
      </div>
    </div>
    <div class="min-h-[320px] bg-[#0b141a] light:bg-[#efeae2] p-3">
      <div v-if="isEmpty" class="flex h-[296px] items-center justify-center text-center text-xs text-white/50 light:text-gray-500 px-6">
        {{ t('templates.previewEmpty', 'Start writing the message body to see how it will look in WhatsApp.') }}
      </div>
      <div v-else class="max-w-[92%]">
        <div class="rounded-lg rounded-tl-none bg-[#202c33] light:bg-white text-[#e9edef] light:text-[#111b21] shadow-sm overflow-hidden">
          <div v-if="showHeader" class="p-1 pb-0">
            <template v-if="headerType === 'TEXT'">
              <p class="px-2 pt-1.5 text-sm font-semibold break-words">{{ header }}</p>
            </template>
            <img v-else-if="headerType === 'IMAGE' && mediaUrl" :src="mediaUrl" alt="" class="w-full max-h-48 rounded-md object-cover" />
            <div v-else class="h-32 rounded-md bg-black/25 light:bg-gray-200 flex flex-col items-center justify-center gap-1 text-white/60 light:text-gray-500">
              <component :is="mediaIcon" class="h-8 w-8" />
              <span class="text-[10px] uppercase tracking-wide">{{ headerType.toLowerCase() }}</span>
            </div>
          </div>
          <div class="px-3 pt-1.5 pb-1">
            <p class="wa-formatted text-sm whitespace-pre-wrap break-words" v-html="bodyHtml" />
            <p v-if="footerText" class="mt-1 text-xs text-white/50 light:text-gray-500">{{ footerText }}</p>
            <p class="text-right text-[10px] text-white/40 light:text-gray-400">12:00</p>
          </div>
          <div v-if="previewButtons.length" class="border-t border-white/10 light:border-gray-200">
            <div
              v-for="(btn, i) in previewButtons"
              :key="i"
              class="flex items-center justify-center gap-1.5 py-2 text-sm text-[#53bdeb] light:text-[#027eb5] border-b last:border-b-0 border-white/10 light:border-gray-200"
            >
              <component :is="buttonIcon(btn.type, btn.otpType)" class="h-3.5 w-3.5" />
              <span class="truncate">{{ btn.text }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.wa-formatted :deep(s) {
  text-decoration: line-through;
}

.wa-formatted :deep(.wa-mono),
.wa-formatted :deep(.wa-pre) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 0.9em;
}

.wa-formatted :deep(.wa-pre) {
  display: block;
  white-space: pre-wrap;
  padding: 0.25rem 0.4rem;
  border-radius: 0.25rem;
  background: rgb(255 255 255 / 0.08);
}
</style>
