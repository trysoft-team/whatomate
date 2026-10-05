import { describe, expect, it } from 'vitest'
import { formatWhatsAppText } from './whatsappFormat'

describe('formatWhatsAppText', () => {
  it('escapes HTML', () => {
    expect(formatWhatsAppText('<script>x</script>')).toBe('&lt;script&gt;x&lt;/script&gt;')
  })

  it('formats bold', () => {
    expect(formatWhatsAppText('Hello *world*')).toBe('Hello <strong>world</strong>')
  })

  it('formats italic', () => {
    expect(formatWhatsAppText('Hello _world_')).toBe('Hello <em>world</em>')
  })

  it('formats strikethrough', () => {
    expect(formatWhatsAppText('Hello ~world~')).toBe('Hello <s>world</s>')
  })

  it('formats monospace', () => {
    expect(formatWhatsAppText('code `TM` here')).toBe('code <code class="wa-mono">TM</code> here')
  })

  it('formats italic monospace initials', () => {
    expect(formatWhatsAppText('_`TM`_')).toBe('<em><code class="wa-mono">TM</code></em>')
  })

  it('formats multiline code blocks', () => {
    expect(formatWhatsAppText('```\nline\n```')).toBe('<code class="wa-pre">\nline\n</code>')
  })
})
