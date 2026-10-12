import type { CannedResponseDraft } from '@/services/api'

export const AI_CANNED_RESPONSE_DRAFT_KEY = 'whatomate:ai-canned-response-draft'

export function storeAICannedResponseDraft(draft: CannedResponseDraft) {
  sessionStorage.setItem(AI_CANNED_RESPONSE_DRAFT_KEY, JSON.stringify(draft))
}

/** Reads and clears the draft left by "Generate with AI" on the Canned Responses list. */
export function takeAICannedResponseDraft(): CannedResponseDraft | null {
  const raw = sessionStorage.getItem(AI_CANNED_RESPONSE_DRAFT_KEY)
  if (!raw) return null
  sessionStorage.removeItem(AI_CANNED_RESPONSE_DRAFT_KEY)
  try {
    const draft = JSON.parse(raw)
    if (typeof draft?.content !== 'string' || !draft.content.trim()) return null
    return {
      name: typeof draft.name === 'string' ? draft.name : '',
      shortcut: typeof draft.shortcut === 'string' ? draft.shortcut : '',
      category: typeof draft.category === 'string' ? draft.category : '',
      content: draft.content,
      buttons: Array.isArray(draft.buttons) ? draft.buttons : [],
      notes: typeof draft.notes === 'string' ? draft.notes : '',
    }
  } catch {
    return null
  }
}
