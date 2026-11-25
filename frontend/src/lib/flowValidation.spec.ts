import { describe, expect, it } from 'vitest'
import {
  hasBlockingErrors,
  validateFlowScreens,
  type ValidatableScreen,
} from './flowValidation'

function screen(children: any[], extras: Partial<ValidatableScreen> = {}): ValidatableScreen {
  return {
    id: extras.id || 'SCREEN_1',
    title: extras.title || 'Screen',
    terminal: extras.terminal,
    layout: { type: 'SingleColumnLayout', children },
  }
}

describe('validateFlowScreens', () => {
  it('allows a simple text + footer screen', () => {
    const issues = validateFlowScreens([
      screen([
        { id: 'h1', type: 'TextHeading', text: 'Hello' },
        {
          id: 'f1',
          type: 'Footer',
          label: 'Done',
          'on-click-action': { name: 'complete', payload: {} },
        },
      ]),
    ])
    expect(hasBlockingErrors(issues)).toBe(false)
  })

  it('flags NavigationList combined with other components', () => {
    const issues = validateFlowScreens([
      screen([
        {
          id: 'n1',
          type: 'NavigationList',
          name: 'nav',
          'list-items': [
            {
              id: 'a',
              'main-content': { title: 'A' },
              'on-click-action': {
                name: 'navigate',
                next: { type: 'screen', name: 'S2' },
                payload: {},
              },
            },
          ],
        },
        { id: 'h1', type: 'TextHeading', text: 'Nope' },
      ]),
    ])
    expect(issues.some(i => i.code === 'X_004')).toBe(true)
  })

  it('requires Footer in both If branches when present', () => {
    const issues = validateFlowScreens([
      screen([
        {
          id: 'if1',
          type: 'If',
          condition: '`${form.x}` == true',
          then: [
            { id: 't1', type: 'TextBody', text: 'A' },
            { id: 'f1', type: 'Footer', label: 'Go', 'on-click-action': { name: 'complete', payload: {} } },
          ],
          else: [{ id: 'e1', type: 'TextBody', text: 'B' }],
        },
      ]),
    ])
    expect(issues.some(i => i.code === 'X_006')).toBe(true)
  })

  it('rejects PhotoPicker and DocumentPicker on the same screen', () => {
    const issues = validateFlowScreens([
      screen([
        { id: 'p1', type: 'PhotoPicker', name: 'photo', label: 'Photo' },
        { id: 'd1', type: 'DocumentPicker', name: 'doc', label: 'Doc' },
        { id: 'f1', type: 'Footer', label: 'Done', 'on-click-action': { name: 'complete', payload: {} } },
      ]),
    ])
    expect(issues.some(i => i.code === 'X_003')).toBe(true)
  })

  it('enforces ChipsSelector min options and label', () => {
    const issues = validateFlowScreens([
      screen([
        { id: 'c1', type: 'ChipsSelector', name: 'chips', label: 'Pick', 'data-source': [{ id: '1', title: 'Only' }] },
        { id: 'f1', type: 'Footer', label: 'Done', 'on-click-action': { name: 'complete', payload: {} } },
      ]),
    ])
    expect(issues.some(i => i.code === 'OPTS_MIN')).toBe(true)
  })

  it('enforces ImageCarousel max 3 images and alt-text', () => {
    const issues = validateFlowScreens([
      screen([
        {
          id: 'ic1',
          type: 'ImageCarousel',
          images: [
            { src: 'a.jpg' },
            { src: 'b.jpg', 'alt-text': 'B' },
            { src: 'c.jpg', 'alt-text': 'C' },
            { src: 'd.jpg', 'alt-text': 'D' },
          ],
        },
        { id: 'f1', type: 'Footer', label: 'Done', 'on-click-action': { name: 'complete', payload: {} } },
      ]),
    ])
    expect(issues.some(i => i.code === 'IC_MAX')).toBe(true)
    expect(issues.some(i => i.code === 'IC_ALT')).toBe(true)
  })

  it('flags TextInput pattern without helper-text', () => {
    const issues = validateFlowScreens([
      screen([
        { id: 't1', type: 'TextInput', name: 'id_num', label: 'ID', pattern: '^[0-9]+$' },
        { id: 'f1', type: 'Footer', label: 'Done', 'on-click-action': { name: 'complete', payload: {} } },
      ]),
    ])
    expect(issues.some(i => i.code === 'PATTERN_HELPER')).toBe(true)
  })

  it('caps components per screen at 50', () => {
    const children = Array.from({ length: 51 }, (_, i) => ({
      id: `c${i}`,
      type: 'TextCaption',
      text: `c${i}`,
    }))
    children.push({
      id: 'f1',
      type: 'Footer',
      label: 'Done',
      'on-click-action': { name: 'complete', payload: {} },
    } as any)
    const issues = validateFlowScreens([screen(children)])
    expect(issues.some(i => i.code === 'U_007')).toBe(true)
  })

  it('flags NavigationList on terminal screens', () => {
    const issues = validateFlowScreens([
      screen(
        [
          {
            id: 'n1',
            type: 'NavigationList',
            name: 'nav',
            'list-items': [
              {
                id: 'a',
                'main-content': { title: 'A' },
                'on-click-action': { name: 'navigate', next: { type: 'screen', name: 'S2' }, payload: {} },
              },
            ],
          },
        ],
        { terminal: true },
      ),
    ])
    expect(issues.some(i => i.code === 'X_015')).toBe(true)
  })
})
