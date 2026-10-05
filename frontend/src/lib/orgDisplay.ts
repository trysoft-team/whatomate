/** Organization display preferences (timezone / date / time format). */

export type TimeFormat = '12h' | '24h'

export interface OrgDisplayPrefs {
  time_format: TimeFormat
  date_format?: string
  timezone?: string
}

const STORAGE_KEY = 'org_display_prefs'

function normalizeTimeFormat(value: unknown): TimeFormat {
  const v = String(value || '').trim().toLowerCase()
  if (v === '24h' || v === '24' || v === '24-hour' || v === '24hour') return '24h'
  return '12h'
}

function readStored(): OrgDisplayPrefs {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return { time_format: '12h' }
    const parsed = JSON.parse(raw)
    return {
      time_format: normalizeTimeFormat(parsed?.time_format),
      date_format: typeof parsed?.date_format === 'string' ? parsed.date_format : undefined,
      timezone: typeof parsed?.timezone === 'string' ? parsed.timezone : undefined,
    }
  } catch {
    return { time_format: '12h' }
  }
}

let prefs: OrgDisplayPrefs = typeof localStorage !== 'undefined' ? readStored() : { time_format: '12h' }

export function getOrgDisplayPrefs(): OrgDisplayPrefs {
  return prefs
}

export function getTimeFormat(): TimeFormat {
  return prefs.time_format
}

/** Whether times should render in 12-hour clock (AM/PM). */
export function preferHour12(): boolean {
  return prefs.time_format !== '24h'
}

export function setOrgDisplayPrefs(partial: Partial<OrgDisplayPrefs>): void {
  prefs = {
    ...prefs,
    ...partial,
    time_format: partial.time_format
      ? normalizeTimeFormat(partial.time_format)
      : prefs.time_format,
  }
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(prefs))
  } catch {
    // ignore quota / private mode
  }
}

/** Load prefs from /org/settings (call after login / org switch). */
export async function hydrateOrgDisplayPrefs(): Promise<void> {
  try {
    const { organizationService } = await import('@/services/api')
    const res = await organizationService.getSettings()
    const data = res.data?.data || res.data
    const s = data?.settings
    if (!s) return
    setOrgDisplayPrefs({
      time_format: normalizeTimeFormat(s.time_format),
      date_format: s.date_format,
      timezone: s.timezone,
    })
  } catch {
    // Keep cached prefs if settings fetch fails.
  }
}
