import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import type { ChatFlowGraph, ChatNode } from '@/services/api'
import { useFlowGraphSimulation } from './useFlowGraphSimulation'

function node(id: string, type: string, config: Record<string, any> = {}): ChatNode {
  return { id, type: type as ChatNode['type'], label: id, position: { x: 0, y: 0 }, config }
}

function graph(nodes: ChatNode[], edges: [string, string, string][]): ChatFlowGraph {
  return {
    version: 2,
    entry_node: nodes[0].id,
    nodes,
    edges: edges.map(([from, condition, to]) => ({ from, condition, to })),
  }
}

function simulate(g: ChatFlowGraph) {
  return useFlowGraphSimulation(ref(g), ref({ name: 'Test' }))
}

async function settle<T>(p: Promise<T>): Promise<T> {
  await vi.runAllTimersAsync()
  return p
}

const menuList = node('menu', 'list', {
  body: 'How can we help?',
  button: 'Open menu',
  store_as: 'menu_choice',
  sections: [
    {
      title: 'Options',
      rows: [
        { id: 'book', title: 'Book appointment', description: 'Nails, hair' },
        { id: 'services', title: 'Our services' },
      ],
    },
  ],
})

describe('useFlowGraphSimulation', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('shows a list step as a list picker and follows the chosen row', async () => {
    const sim = simulate(graph(
      [menuList, node('booked', 'message', { message: 'Booking {{menu_choice}}' })],
      [['menu', 'button:book', 'booked']],
    ))

    await settle(sim.startSimulation())

    expect(sim.state.status).toBe('waiting_input')
    expect(sim.expectedInputType.value).toBe('button')
    const listMsg = sim.state.messages.find((m) => m.type === 'bot')!
    expect(listMsg.content).toBe('How can we help?')
    expect(listMsg.isList).toBe(true)
    expect(listMsg.listButtonText).toBe('Open menu')
    expect(listMsg.buttons?.map((b) => b.id)).toEqual(['book', 'services'])
    expect(listMsg.buttons?.[0].description).toBe('Nails, hair')

    await settle(sim.processUserInput({ id: 'book', title: 'Book appointment' }))

    expect(sim.state.variables.menu_choice).toBe('Book appointment')
    expect(sim.state.messages.some((m) => m.type === 'bot' && m.content === 'Booking Book appointment')).toBe(true)
  })

  it('restarts from the entry step when a reply matches no branch', async () => {
    const sim = simulate(graph(
      [node('hello', 'message', { message: 'Hello' }), menuList],
      [['hello', 'default', 'menu']],
    ))

    await settle(sim.startSimulation())
    await settle(sim.processUserInput('something random'))

    const hellos = sim.state.messages.filter((m) => m.type === 'bot' && m.content === 'Hello')
    expect(hellos).toHaveLength(2)
    expect(sim.state.status).toBe('waiting_input')
    expect(sim.state.currentStepName).toBe('menu')
  })

  it('routes rules-mode conditions by the first matching rule handle', async () => {
    const cond = node('cond', 'condition', {
      mode: 'rules',
      variable: 'service',
      rules: [
        { name: 'Nails', operator: 'text_contains', value: 'NAIL', handle: 'nails' },
        { name: 'Big spend', operator: 'number_between', value: '10', value_to: '20' },
      ],
    })
    const build = (service: string) => simulate(graph(
      [
        node('set', 'set_variable', { set: { service } }),
        cond,
        node('nails', 'message', { message: 'nails branch' }),
        node('big', 'message', { message: 'big branch' }),
        node('other', 'message', { message: 'else branch' }),
      ],
      [
        ['set', 'default', 'cond'],
        ['cond', 'nails', 'nails'],
        ['cond', 'big_spend', 'big'],
        ['cond', 'else', 'other'],
      ],
    ))

    const lastBot = (sim: ReturnType<typeof simulate>) =>
      sim.state.messages.filter((m) => m.type === 'bot').pop()?.content

    const nails = build('Gel nails')
    await settle(nails.startSimulation())
    expect(lastBot(nails)).toBe('nails branch')

    const big = build('15')
    await settle(big.startSimulation())
    expect(lastBot(big)).toBe('big branch')

    const other = build('Facial')
    await settle(other.startSimulation())
    expect(lastBot(other)).toBe('else branch')
  })

  it('simulates side-effect steps without stopping the flow', async () => {
    const sim = simulate(graph(
      [
        node('set', 'set_variable', { set: { booking_name: 'Ann' } }),
        node('labels', 'set_labels', { add: ['vip', 'lead'], remove: [] }),
        node('phonebook', 'phonebook', { action: 'add', list_id: 'abc' }),
        node('email', 'send_email', { to: 'x@example.com', subject: 'Booking {{booking_name}}' }),
        node('delay', 'delay', { seconds: 2 }),
        node('google', 'google_service', { action: 'sheets_append' }),
        node('tpl', 'wa_template', { template_name: 'appointment_reminder' }),
        node('mute', 'disable_bot', { hours: 1, minutes: 30 }),
        node('reset', 'reset_session', {}),
        node('done', 'message', { message: 'All done' }),
      ],
      [
        ['set', 'default', 'labels'],
        ['labels', 'default', 'phonebook'],
        ['phonebook', 'default', 'email'],
        ['email', 'default', 'delay'],
        ['delay', 'default', 'google'],
        ['google', 'default', 'tpl'],
        ['tpl', 'default', 'mute'],
        ['mute', 'default', 'reset'],
        ['reset', 'default', 'done'],
      ],
    ))

    await settle(sim.startSimulation())

    const contents = sim.state.messages.map((m) => m.content)
    expect(contents.some((c) => c.startsWith('Unknown node type'))).toBe(false)
    expect(contents).toContain('All done')
    expect(contents.some((c) => c.includes('Booking Ann'))).toBe(true)
    expect(contents.some((c) => c.includes('appointment_reminder'))).toBe(true)
    expect(sim.state.status).toBe('completed')
    // reset_session drops custom variables but keeps the built-ins.
    expect(sim.state.variables.booking_name).toBeUndefined()
    expect(sim.state.variables.tags).toBeUndefined()
    expect(sim.state.variables.phone_number).toBeTruthy()
    expect(sim.state.variables.contact_name).toBeTruthy()
  })

  it('records labels in the tags variable', async () => {
    const sim = simulate(graph(
      [node('labels', 'set_labels', { add: ['vip', 'lead'] })],
      [],
    ))

    await settle(sim.startSimulation())

    expect(sim.state.variables.tags).toEqual(['lead', 'vip'])
  })

  it('waits for a reply after a template when wait_for_reply is set', async () => {
    const sim = simulate(graph(
      [
        node('tpl', 'wa_template', { template_name: 'promo', wait_for_reply: true }),
        node('after', 'message', { message: 'Thanks for replying' }),
      ],
      [['tpl', 'default', 'after']],
    ))

    await settle(sim.startSimulation())
    expect(sim.state.status).toBe('waiting_input')
    expect(sim.expectedInputType.value).toBe('text')

    await settle(sim.processUserInput('ok'))
    expect(sim.state.messages.some((m) => m.content === 'Thanks for replying')).toBe(true)
  })
})
