import type { TemplateDraft } from '@/services/api'

export const AI_TEMPLATE_DRAFT_KEY = 'whatomate:ai-template-draft'

export function storeAITemplateDraft(draft: TemplateDraft) {
  sessionStorage.setItem(AI_TEMPLATE_DRAFT_KEY, JSON.stringify(draft))
}

/** Reads and clears the draft left by "Generate with AI" on the Templates list. */
export function takeAITemplateDraft(): TemplateDraft | null {
  const raw = sessionStorage.getItem(AI_TEMPLATE_DRAFT_KEY)
  if (!raw) return null
  sessionStorage.removeItem(AI_TEMPLATE_DRAFT_KEY)
  try {
    const d = JSON.parse(raw)
    if (!d || typeof d.name !== 'string') return null
    const str = (v: unknown) => (typeof v === 'string' ? v : '')
    return {
      whatsapp_account: str(d.whatsapp_account),
      name: d.name,
      display_name: str(d.display_name),
      language: str(d.language) || 'en',
      category: str(d.category) || 'UTILITY',
      header_type: str(d.header_type) || 'NONE',
      header_content: str(d.header_content),
      body_content: str(d.body_content),
      footer_content: str(d.footer_content),
      buttons: Array.isArray(d.buttons) ? d.buttons : [],
      sample_values: Array.isArray(d.sample_values) ? d.sample_values : [],
      add_security_recommendation: d.add_security_recommendation === true,
      code_expiration_minutes: typeof d.code_expiration_minutes === 'number' ? d.code_expiration_minutes : 0,
      notes: str(d.notes),
    }
  } catch {
    return null
  }
}
