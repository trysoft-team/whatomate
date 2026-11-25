import { describe, expect, it } from 'vitest'
import { ensureComponentIds, normalizeScreensForBuilder, screensNeedNormalize, wrapScreensForMeta } from './whatsappFlowNormalize'

describe('normalizeScreensForBuilder', () => {
  it('unwraps Meta Form children into a flat editable list', () => {
    const screens = normalizeScreensForBuilder([
      {
        id: 'CLIENT',
        title: 'Register Client',
        layout: {
          type: 'SingleColumnLayout',
          children: [
            {
              type: 'Form',
              name: 'client_form',
              children: [
                { type: 'TextHeading', text: 'Register a client' },
                { type: 'TextInput', name: 'phone', label: 'Client number' },
                { type: 'Footer', label: 'Continue' },
              ],
            },
          ],
        },
      },
    ])

    expect(screens).toHaveLength(1)
    expect(screens[0].formName).toBe('client_form')
    expect(screens[0].layout.children.map((c) => c.type)).toEqual([
      'TextHeading',
      'TextInput',
      'Footer',
    ])
    expect(screens[0].layout.children.every((c) => c.id)).toBe(true)
  })

  it('is idempotent for already-flat builder screens', () => {
    const flat = [
      {
        id: 'A',
        title: 'A',
        formName: 'form',
        layout: {
          type: 'SingleColumnLayout',
          children: [
            { id: 'id_keep', type: 'TextBody', text: 'Hi' },
            { id: 'id_foot', type: 'Footer', label: 'Go' },
          ],
        },
      },
    ]
    const once = normalizeScreensForBuilder(flat)
    const twice = normalizeScreensForBuilder(once)
    expect(twice[0].layout.children.map((c) => c.id)).toEqual(['id_keep', 'id_foot'])
    expect(twice[0].formName).toBe('form')
  })

  it('assigns ids to nested If/Switch children', () => {
    const [screen] = normalizeScreensForBuilder([
      {
        id: 'S',
        title: 'S',
        layout: {
          type: 'SingleColumnLayout',
          children: [
            {
              type: 'Form',
              name: 'f',
              children: [
                {
                  type: 'If',
                  condition: "${form.x} == 'a'",
                  then: [{ type: 'TextBody', text: 'yes' }],
                  else: [{ type: 'TextBody', text: 'no' }],
                },
              ],
            },
          ],
        },
      },
    ])
    const iff = screen.layout.children[0]
    expect(iff.id).toBeTruthy()
    expect(iff.then?.[0].id).toBeTruthy()
    expect(iff.else?.[0].id).toBeTruthy()
  })
})

describe('wrapScreensForMeta', () => {
  it('wraps flat children in Form using preserved formName when fields exist', () => {
    const wrapped = wrapScreensForMeta([
      {
        id: 'CLIENT',
        title: 'Register Client',
        formName: 'client_form',
        layout: {
          type: 'SingleColumnLayout',
          children: [
            { id: 'a', type: 'TextHeading', text: 'Hi' },
            { id: 'i', type: 'TextInput', name: 'phone', label: 'Phone' },
            { id: 'b', type: 'Footer', label: 'Go' },
          ],
        },
      },
    ])

    expect(wrapped[0].formName).toBeUndefined()
    expect(wrapped[0].layout.children).toHaveLength(1)
    expect(wrapped[0].layout.children[0]).toMatchObject({
      type: 'Form',
      name: 'client_form',
    })
    expect(wrapped[0].layout.children[0].children.map((c: any) => c.type)).toEqual([
      'TextHeading',
      'TextInput',
      'Footer',
    ])
  })

  it('does not wrap NavigationList screens in Form', () => {
    const wrapped = wrapScreensForMeta([
      {
        id: 'NAV',
        title: 'Pick',
        layout: {
          type: 'SingleColumnLayout',
          children: [
            {
              id: 'n',
              type: 'NavigationList',
              name: 'nav',
              'list-items': [{ id: 'a', 'main-content': { title: 'A' } }],
            },
          ],
        },
      },
    ])
    expect(wrapped[0].layout.children[0].type).toBe('NavigationList')
  })

  it('round-trips with normalizeScreensForBuilder', () => {
    const original = [
      {
        id: 'CLIENT',
        title: 'Register Client',
        layout: {
          type: 'SingleColumnLayout',
          children: [
            {
              type: 'Form',
              name: 'client_form',
              children: [
                { type: 'TextInput', name: 'phone', label: 'Phone' },
                { type: 'Footer', label: 'Next' },
              ],
            },
          ],
        },
      },
    ]
    const normalized = normalizeScreensForBuilder(original)
    const wrapped = wrapScreensForMeta(normalized)
    expect(wrapped[0].layout.children[0].type).toBe('Form')
    expect(wrapped[0].layout.children[0].name).toBe('client_form')
    expect(wrapped[0].layout.children[0].children.map((c: any) => c.type)).toEqual([
      'TextInput',
      'Footer',
    ])
  })
})

describe('ensureComponentIds', () => {
  it('keeps existing ids', () => {
    expect(ensureComponentIds({ id: 'keep', type: 'TextBody' }).id).toBe('keep')
  })
})

describe('screensNeedNormalize', () => {
  it('detects Meta Form wrappers', () => {
    expect(
      screensNeedNormalize([
        {
          layout: {
            children: [{ type: 'Form', children: [{ type: 'TextBody', text: 'x' }] }],
          },
        },
      ]),
    ).toBe(true)
  })

  it('is false for flat builder screens with ids', () => {
    expect(
      screensNeedNormalize([
        {
          layout: {
            children: [
              { id: 'a', type: 'TextBody', text: 'x' },
              { id: 'b', type: 'Footer', label: 'Go' },
            ],
          },
        },
      ]),
    ).toBe(false)
  })
})
