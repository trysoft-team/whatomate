import type { ChatFlowGraph, ChatNode, ChatEdge } from '@/services/api'

export type FlowTemplateId = 'blank' | 'booking' | 'lead_email' | 'vip_branch' | 'agent_handoff'

export type FlowTemplateMeta = {
  id: FlowTemplateId
  name: string
  description: string
  defaultName: string
  defaultKeywords: string
}

export const FLOW_TEMPLATES: FlowTemplateMeta[] = [
  {
    id: 'blank',
    name: 'Blank',
    description: 'Start with only a Start node.',
    defaultName: '',
    defaultKeywords: '',
  },
  {
    id: 'booking',
    name: 'Booking',
    description: 'Ask for service and preferred time, then confirm.',
    defaultName: 'Booking',
    defaultKeywords: 'book, appointment, schedule',
  },
  {
    id: 'lead_email',
    name: 'Lead + email',
    description: 'Capture name and email, then send a notification email.',
    defaultName: 'Lead capture',
    defaultKeywords: 'interest, quote, demo',
  },
  {
    id: 'vip_branch',
    name: 'VIP list branch',
    description: 'Check phonebook VIP list and branch to different messages.',
    defaultName: 'VIP check',
    defaultKeywords: 'vip, member',
  },
  {
    id: 'agent_handoff',
    name: 'Agent handoff',
    description: 'Greet, then transfer to an agent with a note.',
    defaultName: 'Talk to agent',
    defaultKeywords: 'agent, human, help',
  },
]

function node(
  id: string,
  type: ChatNode['type'],
  label: string,
  x: number,
  y: number,
  config: Record<string, any> = {},
): ChatNode {
  return { id, type, label, position: { x, y }, config }
}

function edge(from: string, to: string, condition = 'default'): ChatEdge {
  return { from, to, condition }
}

export function buildTemplateGraph(id: FlowTemplateId): ChatFlowGraph {
  switch (id) {
    case 'booking':
      return {
        version: 2,
        entry_node: '__start__',
        nodes: [
          node('__start__', 'start', 'Start', 100, 80),
          node('n_ask', 'message', 'Ask service', 100, 220, {
            message: 'Hi {{contact_name}}! What service would you like to book?',
            wait_for_reply: true,
            store_as: 'service',
          }),
          node('n_time', 'message', 'Ask time', 100, 380, {
            message: 'What day and time works for you?',
            wait_for_reply: true,
            store_as: 'preferred_time',
          }),
          node('n_confirm', 'message', 'Confirm', 100, 540, {
            message: 'Got it — {{service}} at {{preferred_time}}. We will confirm shortly.',
            wait_for_reply: false,
          }),
          node('n_end', 'end', 'End', 100, 700, {}),
        ],
        edges: [
          edge('__start__', 'n_ask'),
          edge('n_ask', 'n_time'),
          edge('n_time', 'n_confirm'),
          edge('n_confirm', 'n_end'),
        ],
      }
    case 'lead_email':
      return {
        version: 2,
        entry_node: '__start__',
        nodes: [
          node('__start__', 'start', 'Start', 100, 80),
          node('n_name', 'message', 'Ask name', 100, 220, {
            message: 'Thanks for your interest! What is your name?',
            wait_for_reply: true,
            store_as: 'lead_name',
          }),
          node('n_email', 'message', 'Ask email', 100, 380, {
            message: 'And your email address?',
            wait_for_reply: true,
            store_as: 'lead_email',
          }),
          node('n_mail', 'send_email', 'Notify team', 100, 540, {
            to: 'team@example.com',
            subject: 'New lead: {{lead_name}}',
            body: 'Name: {{lead_name}}\nEmail: {{lead_email}}\nPhone: {{phone_number}}',
          }),
          node('n_thanks', 'message', 'Thanks', 100, 700, {
            message: 'Thanks {{lead_name}}! We will be in touch soon.',
            wait_for_reply: false,
          }),
          node('n_end', 'end', 'End', 100, 860, {}),
        ],
        edges: [
          edge('__start__', 'n_name'),
          edge('n_name', 'n_email'),
          edge('n_email', 'n_mail'),
          edge('n_mail', 'n_thanks'),
          edge('n_thanks', 'n_end'),
        ],
      }
    case 'vip_branch':
      return {
        version: 2,
        entry_node: '__start__',
        nodes: [
          node('__start__', 'start', 'Start', 220, 60),
          node('n_check', 'phonebook', 'VIP check', 220, 200, {
            action: 'check',
            list_name: 'VIP',
            store_as: 'is_vip',
          }),
          node('n_cond', 'condition', 'Is VIP?', 220, 360, {
            expression: 'is_vip == true || is_vip == "true"',
          }),
          node('n_vip', 'message', 'VIP welcome', 40, 520, {
            message: 'Welcome back, valued VIP member!',
            wait_for_reply: false,
          }),
          node('n_std', 'message', 'Standard welcome', 400, 520, {
            message: 'Welcome! How can we help you today?',
            wait_for_reply: false,
          }),
          node('n_end', 'end', 'End', 220, 680, {}),
        ],
        edges: [
          edge('__start__', 'n_check'),
          edge('n_check', 'n_cond'),
          edge('n_cond', 'n_vip', 'true'),
          edge('n_cond', 'n_std', 'false'),
          edge('n_vip', 'n_end'),
          edge('n_std', 'n_end'),
        ],
      }
    case 'agent_handoff':
      return {
        version: 2,
        entry_node: '__start__',
        nodes: [
          node('__start__', 'start', 'Start', 100, 80),
          node('n_greet', 'message', 'Greeting', 100, 220, {
            message: 'Hi {{contact_name}}! Connecting you with a team member.',
            wait_for_reply: false,
          }),
          node('n_xfer', 'transfer', 'Transfer', 100, 380, {
            notes: 'Customer requested human help via flow.',
          }),
          node('n_end', 'end', 'End', 100, 540, {}),
        ],
        edges: [
          edge('__start__', 'n_greet'),
          edge('n_greet', 'n_xfer'),
          edge('n_xfer', 'n_end'),
        ],
      }
    case 'blank':
    default:
      return {
        version: 2,
        entry_node: '__start__',
        nodes: [node('__start__', 'start', 'Start', 100, 100)],
        edges: [],
      }
  }
}
