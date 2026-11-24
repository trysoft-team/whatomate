<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Loader2, Pause, Play } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { formatPlaybackTime, unmaskPlayback } from '@/lib/playback'
import { getRequestHeaders } from '@/services/api'

const props = defineProps<{ src: string }>()
const { t } = useI18n()

const playing = ref(false)
const loading = ref(false)
const failed = ref(false)
const current = ref(0)
const duration = ref(0)

let ctx: AudioContext | null = null
let buffer: AudioBuffer | null = null
let source: AudioBufferSourceNode | null = null
let gain: GainNode | null = null
let startedAt = 0
let offset = 0
let raf = 0
let token = 0
let disposed = false

function audioContext(): AudioContext {
  if (!ctx) {
    const Ctor = window.AudioContext || (window as unknown as { webkitAudioContext: typeof AudioContext }).webkitAudioContext
    ctx = new Ctor()
  }
  return ctx
}

function stopSource() {
  token += 1
  if (source) {
    source.onended = null
    try {
      source.stop()
    } catch {
      // Already stopped.
    }
    source.disconnect()
    source = null
  }
  cancelAnimationFrame(raf)
}

async function load() {
  if (buffer || loading.value) return
  loading.value = true
  failed.value = false
  try {
    const res = await fetch(props.src, {
      credentials: 'include',
      cache: 'no-store',
      headers: getRequestHeaders(),
    })
    if (!res.ok) throw new Error('playback')
    const mask = res.headers.get('X-Playback-Mask') ?? ''
    const masked = new Uint8Array(await res.arrayBuffer())
    const audio = unmaskPlayback(masked, mask)
    const copy = audio.buffer.slice(audio.byteOffset, audio.byteOffset + audio.byteLength)
    const decoded = await audioContext().decodeAudioData(copy)
    if (disposed) return
    buffer = decoded
    duration.value = decoded.duration
  } catch {
    failed.value = true
    buffer = null
  } finally {
    loading.value = false
  }
}

function tick() {
  if (!ctx || !playing.value) return
  current.value = Math.min(duration.value, offset + (ctx.currentTime - startedAt))
  raf = requestAnimationFrame(tick)
}

async function playFrom(position: number) {
  await load()
  if (!buffer || failed.value) return
  const context = audioContext()
  if (context.state === 'suspended') await context.resume()
  stopSource()
  offset = Math.min(Math.max(0, position), buffer.duration)
  if (!gain) {
    gain = context.createGain()
    gain.connect(context.destination)
  }
  const node = context.createBufferSource()
  node.buffer = buffer
  node.connect(gain)
  const myToken = token
  node.onended = () => {
    if (myToken !== token) return
    playing.value = false
    offset = 0
    current.value = 0
    stopSource()
  }
  startedAt = context.currentTime
  node.start(0, offset)
  source = node
  playing.value = true
  current.value = offset
  tick()
}

function pause() {
  if (!ctx || !playing.value) return
  offset = Math.min(duration.value, offset + (ctx.currentTime - startedAt))
  current.value = offset
  playing.value = false
  stopSource()
}

function toggle() {
  if (playing.value) pause()
  else void playFrom(offset)
}

function onSeek(event: Event) {
  const value = Number((event.target as HTMLInputElement).value)
  current.value = value
  offset = value
  if (playing.value) void playFrom(value)
}

function reset() {
  playing.value = false
  stopSource()
  buffer = null
  offset = 0
  current.value = 0
  duration.value = 0
  failed.value = false
}

watch(() => props.src, () => {
  reset()
  void load()
})

onMounted(() => { void load() })

onBeforeUnmount(() => {
  disposed = true
  reset()
  void ctx?.close()
  ctx = null
})
</script>

<template>
  <div
    class="flex items-center gap-3"
    @contextmenu.prevent
    @dragstart.prevent
  >
    <Button
      type="button"
      variant="outline"
      size="icon"
      class="h-9 w-9 shrink-0"
      :disabled="loading || failed"
      :aria-label="playing ? t('calling.pauseRecording') : t('calling.playRecording')"
      @click="toggle"
    >
      <Loader2 v-if="loading" class="h-4 w-4 animate-spin" />
      <Pause v-else-if="playing" class="h-4 w-4" />
      <Play v-else class="h-4 w-4" />
    </Button>
    <div class="min-w-0 flex-1">
      <input
        type="range"
        class="h-1.5 w-full cursor-pointer accent-primary disabled:cursor-default"
        min="0"
        :max="duration || 0"
        step="0.1"
        :value="current"
        :disabled="!duration || failed"
        :aria-label="t('calling.recording')"
        @input="onSeek"
      />
      <div class="mt-1 flex justify-between text-[11px] tabular-nums text-muted-foreground">
        <span>{{ formatPlaybackTime(current) }}</span>
        <span v-if="failed">{{ t('calling.playbackFailed') }}</span>
        <span v-else>{{ formatPlaybackTime(duration) }}</span>
      </div>
    </div>
  </div>
</template>
