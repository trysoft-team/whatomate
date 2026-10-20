import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { dismissAllCallAlerts, dismissCallAlert, showIncomingTransferAlert } from './callAlert'

vi.mock('vue-sonner', () => {
  const dismiss = vi.fn()
  const toastFn = Object.assign(
    vi.fn(() => 'toast-1'),
    { dismiss },
  )
  return { toast: toastFn }
})

vi.mock('@/i18n', () => ({
  i18n: {
    global: {
      t: (key: string) => key,
    },
  },
}))

beforeAll(() => {
  class FakeAudio {
    volume = 1
    loop = false
    currentTime = 0
    play = vi.fn().mockResolvedValue(undefined)
    pause = vi.fn()
  }
  ;(globalThis as any).Audio = FakeAudio
})

describe('callAlert', () => {
  afterEach(() => {
    dismissAllCallAlerts()
    vi.clearAllMocks()
  })

  it('shows a transfer toast once per id and answers via callback', async () => {
    const { toast } = await import('vue-sonner')
    const onAnswer = vi.fn()

    showIncomingTransferAlert(
      {
        id: 'xfer-1',
        contact_name: 'Ada',
        caller_phone: '+1555',
        whatsapp_account: 'Main',
        team_name: 'Sales',
      },
      onAnswer,
    )
    showIncomingTransferAlert(
      { id: 'xfer-1', caller_phone: '+1555' },
      onAnswer,
    )

    expect(toast).toHaveBeenCalledTimes(1)
    const opts = (toast as any).mock.calls[0][1]
    expect(opts.description).toContain('Ada')
    expect(opts.description).toContain('+1555')
    expect(opts.description).toContain('Main')
    expect(opts.description).toContain('Sales')
    expect(opts.duration).toBe(Infinity)

    opts.action.onClick()
    expect(onAnswer).toHaveBeenCalledWith('xfer-1')
  })

  it('dismissCallAlert removes the toast', async () => {
    const { toast } = await import('vue-sonner')
    showIncomingTransferAlert({ id: 'xfer-2', caller_phone: '+1' }, () => {})
    dismissCallAlert('xfer-2')
    expect((toast as any).dismiss).toHaveBeenCalledWith('toast-1')
  })
})
