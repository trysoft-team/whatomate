/**
 * Minimal Meta Flow condition / value resolver for the builder playground.
 * Supports the common operators used in Flow JSON If/Switch expressions.
 */

export type FlowFormValues = Record<string, unknown>
export type FlowScreenData = Record<string, any>

function exampleFromData(data: FlowScreenData, key: string): unknown {
  const entry = data?.[key]
  if (entry && typeof entry === 'object' && '__example__' in entry) {
    return (entry as { __example__: unknown }).__example__
  }
  return undefined
}

/** Resolve ${form.x} / ${data.x} references. */
export function resolveFlowRef(
  expr: string,
  form: FlowFormValues,
  data: FlowScreenData = {},
): unknown {
  const trimmed = expr.trim()
  const formMatch = trimmed.match(/^\$\{form\.([a-zA-Z0-9_]+)\}$/)
  if (formMatch) {
    const key = formMatch[1]
    if (key in form) return form[key]
    const ex = exampleFromData(data, key)
    return ex !== undefined ? ex : undefined
  }
  const dataMatch = trimmed.match(/^\$\{data\.([a-zA-Z0-9_]+)\}$/)
  if (dataMatch) {
    const key = dataMatch[1]
    if (key in data && !(data[key] && typeof data[key] === 'object' && 'type' in data[key])) {
      return data[key]
    }
    return exampleFromData(data, key)
  }
  return undefined
}

function parseLiteral(token: string): unknown {
  const t = token.trim()
  if (t === 'true') return true
  if (t === 'false') return false
  if (t === 'null' || t === 'nil') return null
  if (/^-?\d+(\.\d+)?$/.test(t)) return Number(t)
  const sq = t.match(/^'(.*)'$/s)
  if (sq) return sq[1]
  const dq = t.match(/^"(.*)"$/s)
  if (dq) return dq[1]
  return t
}

function coerceEquals(a: unknown, b: unknown): boolean {
  if (a === b) return true
  if (a == null && b == null) return true
  if (typeof a === 'boolean' || typeof b === 'boolean') {
    return Boolean(a) === Boolean(b) && String(a) !== '' && String(b) !== ''
      ? Boolean(a) === Boolean(b)
      : String(a) === String(b)
  }
  // Meta compares string form values to quoted literals
  return String(a) === String(b)
}

function evalAtom(
  expr: string,
  form: FlowFormValues,
  data: FlowScreenData,
): boolean {
  let e = expr.trim()
  while (e.startsWith('(') && e.endsWith(')')) {
    // only strip matching outer parens
    let depth = 0
    let wrap = true
    for (let i = 0; i < e.length; i++) {
      if (e[i] === '(') depth++
      else if (e[i] === ')') {
        depth--
        if (depth === 0 && i < e.length - 1) {
          wrap = false
          break
        }
      }
    }
    if (!wrap) break
    e = e.slice(1, -1).trim()
  }

  if (e.startsWith('!')) {
    return !evalAtom(e.slice(1), form, data)
  }

  // Split on || then && (low to high precedence handled by recursion order)
  const orParts = splitTopLevel(e, '||')
  if (orParts.length > 1) {
    return orParts.some(p => evalAtom(p, form, data))
  }
  const andParts = splitTopLevel(e, '&&')
  if (andParts.length > 1) {
    return andParts.every(p => evalAtom(p, form, data))
  }

  for (const op of ['==', '!=', '>=', '<=', '>', '<'] as const) {
    const parts = splitTopLevel(e, op)
    if (parts.length === 2) {
      const left = resolveOperand(parts[0], form, data)
      const right = resolveOperand(parts[1], form, data)
      switch (op) {
        case '==':
          return coerceEquals(left, right)
        case '!=':
          return !coerceEquals(left, right)
        case '>':
          return Number(left) > Number(right)
        case '<':
          return Number(left) < Number(right)
        case '>=':
          return Number(left) >= Number(right)
        case '<=':
          return Number(left) <= Number(right)
      }
    }
  }

  // Bare truthiness: ${form.opt_in}
  const val = resolveOperand(e, form, data)
  return Boolean(val)
}

function resolveOperand(raw: string, form: FlowFormValues, data: FlowScreenData): unknown {
  const t = raw.trim()
  if (t.includes('${')) {
    // Replace embedded refs inside a larger string, or single ref
    const single = t.match(/^\$\{(form|data)\.[a-zA-Z0-9_]+\}$/)
    if (single) return resolveFlowRef(t, form, data)
    return t.replace(/\$\{(form|data)\.[a-zA-Z0-9_]+\}/g, (m) => {
      const v = resolveFlowRef(m, form, data)
      return v == null ? '' : String(v)
    })
  }
  return parseLiteral(t)
}

function splitTopLevel(expr: string, sep: string): string[] {
  const parts: string[] = []
  let depth = 0
  let inQuote: "'" | '"' | null = null
  let start = 0
  for (let i = 0; i < expr.length; i++) {
    const ch = expr[i]
    if (inQuote) {
      if (ch === inQuote && expr[i - 1] !== '\\') inQuote = null
      continue
    }
    if (ch === "'" || ch === '"') {
      inQuote = ch
      continue
    }
    if (ch === '(') depth++
    else if (ch === ')') depth--
    else if (depth === 0 && expr.startsWith(sep, i)) {
      parts.push(expr.slice(start, i))
      i += sep.length - 1
      start = i + 1
    }
  }
  parts.push(expr.slice(start))
  return parts.map(p => p.trim()).filter(Boolean)
}

/** Evaluate a Meta Flow If condition string. Empty / unknown → false. */
export function evaluateFlowCondition(
  condition: string | undefined | null,
  form: FlowFormValues = {},
  data: FlowScreenData = {},
): boolean {
  if (!condition || !String(condition).trim()) return false
  try {
    return evalAtom(String(condition), form, data)
  } catch {
    return false
  }
}

/** Resolve Switch value expression to a string case key. */
export function resolveSwitchValue(
  valueExpr: string | undefined | null,
  form: FlowFormValues = {},
  data: FlowScreenData = {},
): string {
  if (!valueExpr) return ''
  const t = String(valueExpr).trim()
  const v = t.includes('${') ? resolveFlowRef(t, form, data) : parseLiteral(t)
  return v == null ? '' : String(v)
}

export interface ResolvableComponent {
  id?: string
  type: string
  name?: string
  condition?: string
  value?: string
  then?: ResolvableComponent[]
  else?: ResolvableComponent[]
  cases?: Record<string, ResolvableComponent[]>
  [key: string]: any
}

/**
 * Expand If/Switch into the components Meta would show for the current form/data.
 * Footers nested inside the winning branch are included in the result.
 */
export function resolveVisibleFlowComponents(
  components: ResolvableComponent[] | undefined | null,
  form: FlowFormValues = {},
  data: FlowScreenData = {},
): ResolvableComponent[] {
  if (!Array.isArray(components)) return []
  const out: ResolvableComponent[] = []
  for (const comp of components) {
    if (!comp) continue
    if (comp.type === 'If') {
      const ok = evaluateFlowCondition(comp.condition, form, data)
      const branch = ok
        ? (Array.isArray(comp.then) ? comp.then : [])
        : (Array.isArray(comp.else) ? comp.else : [])
      out.push(...resolveVisibleFlowComponents(branch, form, data))
      continue
    }
    if (comp.type === 'Switch') {
      const key = resolveSwitchValue(comp.value, form, data)
      const cases = comp.cases || {}
      const branch = cases[key] || cases.default || cases[''] || []
      out.push(...resolveVisibleFlowComponents(Array.isArray(branch) ? branch : [], form, data))
      continue
    }
    out.push(comp)
  }
  return out
}
