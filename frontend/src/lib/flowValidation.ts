/**
 * Live Meta WhatsApp Flow component rules (based on Meta Flows components docs).
 * https://developers.facebook.com/documentation/business-messaging/whatsapp/flows/guides/components
 */

export type FlowIssueSeverity = 'error' | 'warning'

export interface FlowIssue {
  severity: FlowIssueSeverity
  code: string
  message: string
  screenIndex: number
  screenId?: string
  componentId?: string
  componentType?: string
}

export interface ValidatableComponent {
  id?: string
  type: string
  name?: string
  label?: string
  text?: string
  description?: string
  condition?: string
  value?: string
  required?: boolean
  pattern?: string
  'helper-text'?: string
  'data-source'?: any[]
  'list-items'?: any[]
  images?: any[]
  'media-size'?: string
  'on-click-action'?: any
  'max-uploaded-photos'?: number
  'max-uploaded-documents'?: number
  then?: ValidatableComponent[]
  else?: ValidatableComponent[]
  cases?: Record<string, ValidatableComponent[]>
  children?: ValidatableComponent[]
  [key: string]: any
}

export interface ValidatableScreen {
  id?: string
  title?: string
  terminal?: boolean
  layout?: { children?: ValidatableComponent[] }
  [key: string]: any
}

export interface ValidateFlowOptions {
  endpointEnabled?: boolean
  /** Flow JSON version string, e.g. "6.1" / "7.1" */
  jsonVersion?: string
}

function len(v: unknown): number {
  return v == null ? 0 : String(v).length
}

function walkComponents(
  comps: ValidatableComponent[] | undefined,
  visit: (c: ValidatableComponent, depth: number, parentIfDepth: number) => void,
  depth = 0,
  parentIfDepth = 0,
) {
  if (!Array.isArray(comps)) return
  for (const c of comps) {
    if (!c?.type) continue
    visit(c, depth, parentIfDepth)
    const nextIf = c.type === 'If' ? parentIfDepth + 1 : parentIfDepth
    if (Array.isArray(c.then)) walkComponents(c.then, visit, depth + 1, nextIf)
    if (Array.isArray(c.else)) walkComponents(c.else, visit, depth + 1, nextIf)
    if (c.cases) {
      for (const branch of Object.values(c.cases)) {
        if (Array.isArray(branch)) walkComponents(branch, visit, depth + 1, parentIfDepth)
      }
    }
    if (Array.isArray(c.children)) walkComponents(c.children, visit, depth + 1, parentIfDepth)
  }
}

function countType(comps: ValidatableComponent[] | undefined, type: string): number {
  let n = 0
  walkComponents(comps, (c) => {
    if (c.type === type) n++
  })
  return n
}

function collectAll(comps: ValidatableComponent[] | undefined): ValidatableComponent[] {
  const out: ValidatableComponent[] = []
  walkComponents(comps, (c) => out.push(c))
  return out
}

function countAll(comps: ValidatableComponent[] | undefined): number {
  return collectAll(comps).length
}

function footersIn(comps: ValidatableComponent[] | undefined): ValidatableComponent[] {
  return collectAll(comps).filter(c => c.type === 'Footer')
}

function parseVersion(v?: string): number {
  if (!v) return 6.1
  const n = Number(String(v).replace(/^v/i, ''))
  return Number.isFinite(n) ? n : 6.1
}

function issue(
  partial: Omit<FlowIssue, 'severity'> & { severity?: FlowIssueSeverity },
): FlowIssue {
  return { severity: 'error', ...partial }
}

function validateComponentFields(
  c: ValidatableComponent,
  screenIndex: number,
  screenId: string | undefined,
  issues: FlowIssue[],
) {
  const base = {
    screenIndex,
    screenId,
    componentId: c.id,
    componentType: c.type,
  }

  const push = (code: string, message: string, severity: FlowIssueSeverity = 'error') => {
    issues.push(issue({ ...base, code, message, severity }))
  }

  const textLimits: Record<string, number> = {
    TextHeading: 80,
    TextSubheading: 80,
    TextBody: 4096,
    TextCaption: 409,
  }
  if (textLimits[c.type] != null && len(c.text) > textLimits[c.type]) {
    push('CHAR_TEXT', `${c.type} text exceeds ${textLimits[c.type]} characters (${len(c.text)})`)
  }
  if (c.type === 'RichText') {
    const text = Array.isArray(c.text) ? c.text.join('\n') : c.text
    if (!text || !String(text).trim()) push('RT_EMPTY', 'RichText requires text')
  }

  if (c.type === 'TextInput' || c.type === 'TextArea') {
    if (!c.name) push('NAME_REQUIRED', `${c.type} requires a field name`)
    if (len(c.label) > 20) push('CHAR_LABEL', `${c.type} label max 20 characters`)
    if (len(c['helper-text']) > 80) push('CHAR_HELPER', `${c.type} helper text max 80 characters`)
    if (c.type === 'TextInput' && c.pattern && !c['helper-text']) {
      push('PATTERN_HELPER', 'TextInput with a pattern requires helper-text')
    }
  }

  if (['Dropdown', 'RadioButtonsGroup', 'CheckboxGroup', 'ChipsSelector'].includes(c.type)) {
    if (!c.name) push('NAME_REQUIRED', `${c.type} requires a field name`)
    if (!c.label) push('LABEL_REQUIRED', `${c.type} requires a label`)
    const opts = Array.isArray(c['data-source']) ? c['data-source'] : []
    const minOpts = c.type === 'ChipsSelector' ? 2 : 1
    const maxOpts = 20
    const labelMax = c.type === 'ChipsSelector' ? 80 : 30
    if (len(c.label) > labelMax) push('CHAR_LABEL', `${c.type} label max ${labelMax} characters`)
    if (len(c.description) > 300) push('CHAR_DESC', `${c.type} description max 300 characters`)
    if (opts.length < minOpts) push('OPTS_MIN', `${c.type} needs at least ${minOpts} option(s)`)
    if (opts.length > maxOpts) push('OPTS_MAX', `${c.type} allows at most ${maxOpts} options`)
    for (const opt of opts) {
      if (!opt?.id) push('OPT_ID', `${c.type} option is missing id`)
      if (!opt?.title) push('OPT_TITLE', `${c.type} option is missing title`)
      if (len(opt?.title) > 30) push('OPT_TITLE_LEN', `Option title max 30 characters`)
      if (len(opt?.description) > 300) push('OPT_DESC_LEN', `Option description max 300 characters`)
      if (len(opt?.metadata) > 20) push('OPT_META_LEN', `Option metadata max 20 characters`)
    }
  }

  if (c.type === 'OptIn') {
    if (!c.name) push('NAME_REQUIRED', 'OptIn requires a field name')
    if (!c.label) push('LABEL_REQUIRED', 'OptIn requires a label')
    if (len(c.label) > 120) push('CHAR_LABEL', 'OptIn label max 120 characters')
  }

  if (c.type === 'Footer') {
    if (!c.label) push('LABEL_REQUIRED', 'Footer requires button text')
    if (len(c.label) > 35) push('CHAR_LABEL', 'Footer label max 35 characters')
    if (!c['on-click-action']?.name) push('FOOTER_ACTION', 'Footer requires an on-click-action')
  }

  if (c.type === 'EmbeddedLink') {
    if (!c.text) push('LINK_TEXT', 'EmbeddedLink requires text')
    if (len(c.text) > 60) push('CHAR_TEXT', 'EmbeddedLink text max 60 characters', 'warning')
  }

  if (c.type === 'DatePicker' || c.type === 'CalendarPicker') {
    if (!c.name) push('NAME_REQUIRED', `${c.type} requires a field name`)
    if (!c.label) push('LABEL_REQUIRED', `${c.type} requires a label`)
  }

  if (c.type === 'PhotoPicker' || c.type === 'DocumentPicker') {
    if (!c.name) push('NAME_REQUIRED', `${c.type} requires a field name`)
    if (!c.label) push('LABEL_REQUIRED', `${c.type} requires a label`)
    if (len(c.label) > 80) push('CHAR_LABEL', `${c.type} label max 80 characters`)
    if (len(c.description) > 300) push('CHAR_DESC', `${c.type} description max 300 characters`)
  }

  if (c.type === 'Image') {
    if (!c.src) push('IMG_SRC', 'Image requires src', 'warning')
  }

  if (c.type === 'ImageCarousel') {
    const images = Array.isArray(c.images) ? c.images : []
    if (images.length < 1) push('IC_MIN', 'ImageCarousel needs at least 1 image')
    if (images.length > 3) push('IC_MAX', 'ImageCarousel allows at most 3 images')
    images.forEach((img, i) => {
      if (!img?.src) push('IC_SRC', `Carousel image ${i + 1} needs src`)
      if (!img?.['alt-text']) push('IC_ALT', `Carousel image ${i + 1} needs alt-text`)
    })
  }

  if (c.type === 'NavigationList') {
    if (!c.name) push('NAME_REQUIRED', 'NavigationList requires a name')
    if (len(c.label) > 80) push('CHAR_LABEL', 'NavigationList label max 80 characters')
    if (len(c.description) > 300) push('CHAR_DESC', 'NavigationList description max 300 characters')
    const items = Array.isArray(c['list-items']) ? c['list-items'] : []
    if (items.length < 1) push('NL_MIN', 'NavigationList needs at least 1 item')
    if (items.length > 20) push('NL_MAX', 'NavigationList allows at most 20 items')
    const badges = items.filter((it: any) => it?.badge)
    if (badges.length > 1) push('NL_BADGE', 'Only one NavigationList item may have a badge')
    if (c['media-size'] === 'large' && items.some((it: any) => it?.end)) {
      push('NL_END_LARGE', 'NavigationList end add-on cannot be used with media-size large')
    }
    const itemActions = items.some((it: any) => it?.['on-click-action'])
    const compAction = !!c['on-click-action']
    if (itemActions && compAction) {
      push('NL_ACTION', 'Define on-click-action on the list OR on items, not both')
    }
    if (!itemActions && !compAction) {
      push('NL_ACTION_REQUIRED', 'NavigationList requires an on-click-action (list or items)')
    }
    for (const [i, it] of items.entries()) {
      const title = it?.['main-content']?.title
      if (!title) push('NL_TITLE', `Nav item ${i + 1} needs main-content.title`)
      if (len(title) > 30) push('NL_TITLE_LEN', `Nav item title max 30 characters`)
      if (len(it?.['main-content']?.description) > 20) {
        push('NL_DESC_LEN', `Nav item description max 20 characters`)
      }
      if (Array.isArray(it?.tags) && it.tags.length > 3) {
        push('NL_TAGS', `Nav item allows at most 3 tags`)
      }
    }
  }

  if (c.type === 'If') {
    if (!c.condition || !String(c.condition).trim()) push('IF_COND', 'If requires a condition')
    if (!Array.isArray(c.then) || c.then.length === 0) push('IF_THEN', 'If then branch must contain at least one component')
  }

  if (c.type === 'Switch') {
    if (!c.value || !String(c.value).trim()) push('SW_VALUE', 'Switch requires a value expression')
    const cases = c.cases || {}
    if (!Object.keys(cases).length) push('SW_CASES', 'Switch cases cannot be empty')
  }
}

function validateScreen(
  screen: ValidatableScreen,
  screenIndex: number,
  issues: FlowIssue[],
  opts: ValidateFlowOptions,
) {
  const children = screen.layout?.children || []
  const screenId = screen.id
  const all = collectAll(children)
  const version = parseVersion(opts.jsonVersion)

  if (countAll(children) > 50) {
    issues.push(issue({
      code: 'U_007',
      message: `Screen "${screen.title || screenId || screenIndex + 1}" exceeds 50 components (including nested)`,
      screenIndex,
      screenId,
    }))
  }

  // Unique field names
  const names = new Map<string, string>()
  for (const c of all) {
    if (!c.name) continue
    if (['TextHeading', 'TextSubheading', 'TextBody', 'TextCaption', 'RichText', 'Image', 'ImageCarousel', 'EmbeddedLink', 'If', 'Switch', 'Footer'].includes(c.type)) {
      continue
    }
    if (names.has(c.name)) {
      issues.push(issue({
        code: 'DUP_NAME',
        message: `Duplicate field name "${c.name}" on this screen`,
        screenIndex,
        screenId,
        componentId: c.id,
        componentType: c.type,
      }))
    } else {
      names.set(c.name, c.type)
    }
  }

  // Footer rules
  const topFooters = children.filter(c => c.type === 'Footer')
  const allFooters = footersIn(children)
  if (allFooters.length > 1) {
    issues.push(issue({
      code: 'X_FOOTER_MAX',
      message: 'Only one Footer is allowed per screen',
      screenIndex,
      screenId,
    }))
  }

  // If + Footer pairing
  for (const c of children) {
    if (c.type !== 'If') continue
    const thenF = footersIn(c.then)
    const elseF = footersIn(c.else)
    if (thenF.length || elseF.length) {
      if (thenF.length !== 1 || elseF.length !== 1) {
        issues.push(issue({
          code: 'X_006',
          message: 'Footer inside If must exist in both then and else branches',
          screenIndex,
          screenId,
          componentId: c.id,
          componentType: 'If',
        }))
      }
      if (topFooters.length) {
        issues.push(issue({
          code: 'X_007',
          message: 'When Footer is inside If, no Footer may exist outside it on the same screen',
          screenIndex,
          screenId,
          componentId: c.id,
          componentType: 'If',
        }))
      }
    }
  }
  walkComponents(children, (c, _d, ifDepth) => {
    if (c.type === 'Footer' && ifDepth > 1) {
      issues.push(issue({
        code: 'X_008',
        message: 'Footer may only appear inside the outermost If, not nested Ifs',
        screenIndex,
        screenId,
        componentId: c.id,
        componentType: 'Footer',
      }))
    }
  })

  // Media exclusivity
  const photos = countType(children, 'PhotoPicker')
  const docs = countType(children, 'DocumentPicker')
  if (photos + docs > 1) {
    issues.push(issue({
      code: 'X_003',
      message: 'Only one PhotoPicker or DocumentPicker is allowed per screen',
      screenIndex,
      screenId,
    }))
  }
  if (!opts.endpointEnabled) {
    for (const c of all) {
      if (c.type === 'PhotoPicker' && Number(c['max-uploaded-photos'] || 1) > 1) {
        issues.push(issue({
          code: 'PP_MAX_COMPLETE',
          message: `PhotoPicker "${c.name || c.id}" max files > 1 requires a data endpoint`,
          screenIndex,
          screenId,
          componentId: c.id,
          componentType: c.type,
        }))
      }
      if (c.type === 'DocumentPicker' && Number(c['max-uploaded-documents'] || 1) > 1) {
        issues.push(issue({
          code: 'DP_MAX_COMPLETE',
          message: `DocumentPicker "${c.name || c.id}" max files > 1 requires a data endpoint`,
          screenIndex,
          screenId,
          componentId: c.id,
          componentType: c.type,
        }))
      }
    }
  }

  // NavigationList exclusivity
  const navCount = countType(children, 'NavigationList')
  if (navCount > 2) {
    issues.push(issue({
      code: 'X_012',
      message: 'At most 2 NavigationList components per screen',
      screenIndex,
      screenId,
    }))
  }
  if (navCount > 0) {
    const nonNav = all.filter(c => c.type !== 'NavigationList')
    if (nonNav.length) {
      issues.push(issue({
        code: 'X_004',
        message: 'NavigationList cannot be combined with other components on the same screen',
        screenIndex,
        screenId,
      }))
    }
    if (screen.terminal) {
      issues.push(issue({
        code: 'X_015',
        message: 'NavigationList cannot be used on a terminal screen',
        screenIndex,
        screenId,
      }))
    }
  }

  // RichText exclusivity (Footer allowed from 6.3+)
  const richCount = countType(children, 'RichText')
  if (richCount) {
    const others = all.filter(c => c.type !== 'RichText' && c.type !== 'Footer')
    const footerOk = version >= 6.3
    if (others.length || (!footerOk && all.some(c => c.type === 'Footer'))) {
      issues.push(issue({
        code: 'X_005',
        message: footerOk
          ? 'RichText may only share a screen with Footer'
          : 'RichText must be alone on the screen (Footer allowed from Flow JSON 6.3+)',
        screenIndex,
        screenId,
      }))
    }
  }

  // Per-screen caps
  const caps: Array<[string, number, string]> = [
    ['Image', 3, 'X_009'],
    ['OptIn', 5, 'X_010'],
    ['EmbeddedLink', 2, 'X_011'],
    ['ImageCarousel', 2, 'X_013'],
  ]
  for (const [type, max, code] of caps) {
    const n = countType(children, type)
    if (n > max) {
      issues.push(issue({
        code,
        message: `At most ${max} ${type} component(s) per screen (found ${n})`,
        screenIndex,
        screenId,
      }))
    }
  }

  for (const c of all) {
    validateComponentFields(c, screenIndex, screenId, issues)
  }
}

/** Validate all screens against Meta Flow component rules. */
export function validateFlowScreens(
  screens: ValidatableScreen[] | undefined | null,
  opts: ValidateFlowOptions = {},
): FlowIssue[] {
  if (!Array.isArray(screens) || screens.length === 0) return []
  const issues: FlowIssue[] = []

  let carouselTotal = 0
  screens.forEach((screen, index) => {
    validateScreen(screen, index, issues, opts)
    carouselTotal += countType(screen.layout?.children || [], 'ImageCarousel')
  })
  if (carouselTotal > 3) {
    issues.push(issue({
      code: 'X_014',
      message: `Flow allows at most 3 ImageCarousel components total (found ${carouselTotal})`,
      screenIndex: 0,
    }))
  }

  // Flow should be able to finish
  const hasComplete = screens.some((screen) =>
    collectAll(screen.layout?.children || []).some((c) => {
      if (c.type !== 'Footer') return false
      const name = c['on-click-action']?.name
      if (name === 'complete') return true
      if (name === 'data_exchange' && !c['on-click-action']?.payload?.next_screen) return true
      return false
    }),
  )
  if (!hasComplete) {
    issues.push(issue({
      code: 'FLOW_COMPLETE',
      message: 'Flow must be able to finish: add a Footer with Complete Flow (or Data Exchange without a next screen)',
      screenIndex: screens.length - 1,
      screenId: screens[screens.length - 1]?.id,
      severity: 'warning',
    }))
  }

  return issues
}

export function issuesForComponent(issues: FlowIssue[], componentId?: string): FlowIssue[] {
  if (!componentId) return []
  return issues.filter(i => i.componentId === componentId)
}

export function issuesForScreen(issues: FlowIssue[], screenIndex: number): FlowIssue[] {
  return issues.filter(i => i.screenIndex === screenIndex)
}

export function hasBlockingErrors(issues: FlowIssue[]): boolean {
  return issues.some(i => i.severity === 'error')
}
