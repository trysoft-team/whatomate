import { describe, expect, it } from 'vitest'
import {
  evaluateFlowCondition,
  resolveSwitchValue,
  resolveVisibleFlowComponents,
} from './flowCondition'

describe('evaluateFlowCondition', () => {
  it('evaluates form equality', () => {
    expect(evaluateFlowCondition("${form.id_type} == 'national_id'", { id_type: 'national_id' })).toBe(true)
    expect(evaluateFlowCondition("${form.id_type} == 'national_id'", { id_type: 'passport' })).toBe(false)
  })

  it('evaluates boolean form fields', () => {
    expect(evaluateFlowCondition('${form.opt_in} == true', { opt_in: true })).toBe(true)
    expect(evaluateFlowCondition('!${form.opt_in}', { opt_in: false })).toBe(true)
  })

  it('supports && and ||', () => {
    expect(evaluateFlowCondition("${form.a} == 'x' && ${form.b} == 'y'", { a: 'x', b: 'y' })).toBe(true)
    expect(evaluateFlowCondition("${form.a} == 'x' || ${form.b} == 'y'", { a: 'no', b: 'y' })).toBe(true)
  })

  it('falls back to screen data __example__', () => {
    expect(
      evaluateFlowCondition("${form.network} == 'econet'", {}, {
        network: { type: 'string', __example__: 'econet' },
      }),
    ).toBe(true)
  })
})

describe('resolveVisibleFlowComponents', () => {
  it('shows then branch when condition is true', () => {
    const visible = resolveVisibleFlowComponents(
      [
        { type: 'TextHeading', text: 'Hi' },
        {
          type: 'If',
          condition: "${form.id_type} == 'national_id'",
          then: [{ type: 'TextInput', name: 'national_id', label: 'National ID' }],
          else: [{ type: 'TextInput', name: 'passport', label: 'Passport' }],
        },
      ],
      { id_type: 'national_id' },
    )
    expect(visible.map(c => c.type)).toEqual(['TextHeading', 'TextInput'])
    expect(visible[1].name).toBe('national_id')
  })

  it('shows else branch when condition is false', () => {
    const visible = resolveVisibleFlowComponents(
      [
        {
          type: 'If',
          condition: "${form.id_type} == 'national_id'",
          then: [{ type: 'TextBody', text: 'then' }],
          else: [{ type: 'TextBody', text: 'else' }],
        },
      ],
      { id_type: 'passport' },
    )
    expect(visible[0].text).toBe('else')
  })

  it('resolves Switch cases', () => {
    const visible = resolveVisibleFlowComponents(
      [
        {
          type: 'Switch',
          value: '${form.plan}',
          cases: {
            basic: [{ type: 'TextBody', text: 'Basic' }],
            pro: [{ type: 'TextBody', text: 'Pro' }],
          },
        },
      ],
      { plan: 'pro' },
    )
    expect(visible[0].text).toBe('Pro')
  })
})

describe('resolveSwitchValue', () => {
  it('reads form refs', () => {
    expect(resolveSwitchValue('${form.plan}', { plan: 'pro' })).toBe('pro')
  })
})
