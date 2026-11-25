/**
 * Meta Flow JSON wraps screen fields in a Form container. Our visual builder
 * edits a flat children list, so synced flows must be unwrapped on load and
 * re-wrapped when pushing back to Meta.
 */

export interface BuilderFlowComponent {
  id: string
  type: string
  name?: string
  children?: BuilderFlowComponent[]
  then?: BuilderFlowComponent[]
  else?: BuilderFlowComponent[]
  cases?: Record<string, BuilderFlowComponent[]>
  [key: string]: any
}

export interface BuilderFlowScreen {
  id: string
  title: string
  data?: Record<string, any>
  /** Preserved Form.name from Meta so sanitize can restore the wrapper. */
  formName?: string
  layout: {
    type: string
    children: BuilderFlowComponent[]
  }
  [key: string]: any
}

function newBuilderId(): string {
  const chars = 'abcdefghijklmnopqrstuvwxyz0123456789'
  let result = 'id_'
  for (let i = 0; i < 12; i++) {
    result += chars.charAt(Math.floor(Math.random() * chars.length))
  }
  return result
}

/** Ensure every component (and nested If/Switch children) has a builder id. */
export function ensureComponentIds(comp: any): BuilderFlowComponent {
  const next: BuilderFlowComponent = {
    ...comp,
    id: typeof comp?.id === 'string' && comp.id ? comp.id : newBuilderId(),
    type: String(comp?.type || 'Unknown'),
  }

  if (Array.isArray(comp?.then)) {
    next.then = comp.then.map((c: any) => ensureComponentIds(c))
  }
  if (Array.isArray(comp?.else)) {
    next.else = comp.else.map((c: any) => ensureComponentIds(c))
  }
  if (comp?.cases && typeof comp.cases === 'object') {
    const cases: Record<string, BuilderFlowComponent[]> = {}
    for (const [key, children] of Object.entries(comp.cases)) {
      cases[key] = (Array.isArray(children) ? children : []).map((c: any) => ensureComponentIds(c))
    }
    next.cases = cases
  }
  // Don't keep Form.children after flatten; if still present (nested), drop after copy.
  if (next.type === 'Form' && Array.isArray(next.children)) {
    delete next.children
  }
  return next
}

function componentTreeNeedsIds(comp: any): boolean {
  if (!comp || typeof comp !== 'object') return false
  if (!comp.id) return true
  if (Array.isArray(comp.then) && comp.then.some(componentTreeNeedsIds)) return true
  if (Array.isArray(comp.else) && comp.else.some(componentTreeNeedsIds)) return true
  if (comp.cases && typeof comp.cases === 'object') {
    for (const children of Object.values(comp.cases)) {
      if (Array.isArray(children) && children.some(componentTreeNeedsIds)) return true
    }
  }
  return false
}

/** True when screens still have a Meta Form wrapper or lack builder ids. */
export function screensNeedNormalize(screens: any[] | undefined | null): boolean {
  if (!Array.isArray(screens)) return false
  for (const screen of screens) {
    const children = screen?.layout?.children
    if (!Array.isArray(children)) continue
    for (const child of children) {
      if (child?.type === 'Form' && Array.isArray(child.children)) return true
      if (componentTreeNeedsIds(child)) return true
    }
  }
  return false
}

/**
 * Flatten Meta Form wrappers into a flat layout.children list the builder can edit.
 * Idempotent when screens are already flat.
 */
export function normalizeScreensForBuilder(screens: any[] | undefined | null): BuilderFlowScreen[] {
  if (!Array.isArray(screens)) return []

  return screens.map((screen, index) => {
    const layout = screen?.layout && typeof screen.layout === 'object' ? screen.layout : {}
    const rawChildren = Array.isArray(layout.children) ? layout.children : []
    let formName: string | undefined =
      typeof screen?.formName === 'string' && screen.formName ? screen.formName : undefined
    const flattened: any[] = []

    for (const child of rawChildren) {
      if (child?.type === 'Form' && Array.isArray(child.children)) {
        if (!formName && typeof child.name === 'string' && child.name) {
          formName = child.name
        }
        flattened.push(...child.children)
      } else if (child) {
        flattened.push(child)
      }
    }

    return {
      ...screen,
      id: typeof screen?.id === 'string' && screen.id ? screen.id : `SCREEN_${index + 1}`,
      title: typeof screen?.title === 'string' ? screen.title : `Screen ${index + 1}`,
      data: screen?.data && typeof screen.data === 'object' ? screen.data : {},
      formName: formName || 'form',
      layout: {
        type: typeof layout.type === 'string' && layout.type ? layout.type : 'SingleColumnLayout',
        children: flattened.map((c) => ensureComponentIds(c)),
      },
    }
  })
}

/** Field types that Meta expects inside a Form wrapper. */
const FORM_FIELD_TYPES = new Set([
  'TextInput',
  'TextArea',
  'Dropdown',
  'RadioButtonsGroup',
  'CheckboxGroup',
  'ChipsSelector',
  'OptIn',
  'DatePicker',
  'CalendarPicker',
  'PhotoPicker',
  'DocumentPicker',
])

function screenNeedsFormWrap(children: BuilderFlowComponent[]): boolean {
  // NavigationList must be alone on a screen and is not wrapped in Form.
  if (children.some((c) => c.type === 'NavigationList')) return false
  return children.some((c) => FORM_FIELD_TYPES.has(c.type))
}

/**
 * Re-wrap flat builder children in a Meta Form container when needed.
 * Leaves screens that already have a top-level Form untouched.
 */
export function wrapScreensForMeta(screens: BuilderFlowScreen[]): any[] {
  return screens.map((screen) => {
    const children = Array.isArray(screen.layout?.children) ? screen.layout.children : []
    const alreadyWrapped = children.length === 1 && children[0]?.type === 'Form'
    const metaChildren = alreadyWrapped
      ? children
      : screenNeedsFormWrap(children)
        ? [
            {
              type: 'Form',
              name: screen.formName || 'form',
              children,
            },
          ]
        : children

    const { formName: _formName, ...rest } = screen
    return {
      ...rest,
      layout: {
        type: screen.layout?.type || 'SingleColumnLayout',
        children: metaChildren,
      },
    }
  })
}
