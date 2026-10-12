export const AI_WHATSAPP_FLOW_DRAFT_KEY = 'whatomate:ai-whatsapp-flow-draft'

export interface AIWhatsAppFlowDraft {
  name: string
  category: string
  screens: any[]
}

/** Reads and clears the draft left by "Generate with AI" on the Flows list. */
export function takeAIWhatsAppFlowDraft(): AIWhatsAppFlowDraft | null {
  const raw = sessionStorage.getItem(AI_WHATSAPP_FLOW_DRAFT_KEY)
  if (!raw) return null
  sessionStorage.removeItem(AI_WHATSAPP_FLOW_DRAFT_KEY)
  try {
    const draft = JSON.parse(raw)
    if (!Array.isArray(draft?.screens) || draft.screens.length === 0) return null
    return {
      name: typeof draft.name === 'string' ? draft.name : '',
      category: typeof draft.category === 'string' ? draft.category : '',
      screens: draft.screens,
    }
  } catch {
    return null
  }
}
