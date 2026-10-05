import { ref, computed } from 'vue'
import type { ChatFlowGraph } from '@/services/api'

const MAX_STACK = 20

/**
 * Canvas undo stack for the chatbot flow builder.
 * Stores ChatFlowGraph JSON snapshots (separate from InteractivePreview undo).
 */
export function useChatFlowUndo() {
  const stack = ref<string[]>([])
  const canUndo = computed(() => stack.value.length > 0)

  function pushSnapshot(graph: ChatFlowGraph) {
    stack.value.push(JSON.stringify(graph))
    if (stack.value.length > MAX_STACK) {
      stack.value.shift()
    }
  }

  function popSnapshot(): ChatFlowGraph | null {
    const raw = stack.value.pop()
    if (!raw) return null
    try {
      return JSON.parse(raw) as ChatFlowGraph
    } catch {
      return null
    }
  }

  function clear() {
    stack.value = []
  }

  return { pushSnapshot, popSnapshot, clear, canUndo }
}
