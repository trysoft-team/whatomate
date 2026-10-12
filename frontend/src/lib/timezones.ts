export interface TimezoneOption {
  value: string
  label: string
}

export interface TimezoneGroup {
  region: string
  timezones: TimezoneOption[]
}

const FRIENDLY_LABELS: Record<string, string> = {
  UTC: 'UTC',
  'America/New_York': 'Eastern Time',
  'America/Chicago': 'Central Time',
  'America/Denver': 'Mountain Time',
  'America/Los_Angeles': 'Pacific Time',
  'Europe/London': 'London',
  'Asia/Tokyo': 'Tokyo',
}

const FALLBACK_ZONES = [
  'UTC',
  'Africa/Cairo',
  'Africa/Johannesburg',
  'Africa/Harare',
  'Africa/Lagos',
  'Africa/Nairobi',
  'America/Chicago',
  'America/Denver',
  'America/Los_Angeles',
  'America/Mexico_City',
  'America/New_York',
  'America/Sao_Paulo',
  'America/Toronto',
  'Asia/Dubai',
  'Asia/Hong_Kong',
  'Asia/Jakarta',
  'Asia/Kolkata',
  'Asia/Seoul',
  'Asia/Shanghai',
  'Asia/Singapore',
  'Asia/Tokyo',
  'Australia/Melbourne',
  'Australia/Perth',
  'Australia/Sydney',
  'Europe/Amsterdam',
  'Europe/Berlin',
  'Europe/London',
  'Europe/Madrid',
  'Europe/Moscow',
  'Europe/Paris',
  'Europe/Rome',
  'Pacific/Auckland',
]

const REGION_ORDER = [
  'UTC',
  'Africa',
  'America',
  'Antarctica',
  'Arctic',
  'Asia',
  'Atlantic',
  'Australia',
  'Europe',
  'Indian',
  'Pacific',
]

function ianaZones(): string[] {
  try {
    if (typeof Intl !== 'undefined' && 'supportedValuesOf' in Intl) {
      return Intl.supportedValuesOf('timeZone')
    }
  } catch {
    // fall through
  }
  return FALLBACK_ZONES
}

function formatOffset(timeZone: string): string {
  try {
    const parts = new Intl.DateTimeFormat('en-US', {
      timeZone,
      timeZoneName: 'shortOffset',
    }).formatToParts(new Date())
    const name = parts.find((part) => part.type === 'timeZoneName')?.value || ''
    return name.replace(/^GMT/, 'UTC')
  } catch {
    return ''
  }
}

export function formatTimezoneLabel(zone: string): string {
  if (FRIENDLY_LABELS[zone]) return FRIENDLY_LABELS[zone]
  const name = zone.replace(/_/g, ' ')
  const offset = formatOffset(zone)
  return offset ? `${name} (${offset})` : name
}

export function getTimezoneGroups(current?: string): TimezoneGroup[] {
  const raw = ianaZones()
  const seen = new Set<string>()
  const zones: string[] = []

  const add = (zone: string) => {
    if (!zone || seen.has(zone)) return
    seen.add(zone)
    zones.push(zone)
  }

  add('UTC')
  for (const zone of raw) {
    if (zone === 'UTC' || zone === 'Etc/UTC') continue
    add(zone)
  }
  if (current) add(current)

  const grouped = new Map<string, TimezoneOption[]>()
  for (const zone of zones) {
    const region = zone === 'UTC' ? 'UTC' : (zone.split('/')[0] || 'Other')
    const list = grouped.get(region) || []
    list.push({ value: zone, label: formatTimezoneLabel(zone) })
    grouped.set(region, list)
  }

  const result: TimezoneGroup[] = []
  for (const region of REGION_ORDER) {
    const timezones = grouped.get(region)
    if (timezones?.length) {
      result.push({ region, timezones })
      grouped.delete(region)
    }
  }
  for (const [region, timezones] of grouped) {
    result.push({ region, timezones })
  }
  return result
}
