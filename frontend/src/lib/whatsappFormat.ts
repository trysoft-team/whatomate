/**
 * Render WhatsApp-style text formatting to safe HTML.
 * Supports: *bold*, _italic_, ~strikethrough~, `monospace`, ```code blocks```.
 * Monospace is applied before italic/bold so _`AB`_ becomes italic monospace.
 */

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

/** Convert WhatsApp formatting markers to HTML (content is HTML-escaped first). */
export function formatWhatsAppText(text: string): string {
  if (!text) return ''

  let s = escapeHtml(text)

  // ```multiline code```
  s = s.replace(/```([\s\S]*?)```/g, '<code class="wa-pre">$1</code>')
  // `inline monospace`
  s = s.replace(/`([^`\n]+?)`/g, '<code class="wa-mono">$1</code>')
  // *bold*
  s = s.replace(/\*([^*\n]+?)\*/g, '<strong>$1</strong>')
  // _italic_ (may wrap monospace HTML, e.g. _<code>…</code>_)
  s = s.replace(/_([^_\n]+?)_/g, '<em>$1</em>')
  // ~strikethrough~
  s = s.replace(/~([^~\n]+?)~/g, '<s>$1</s>')

  return s
}
