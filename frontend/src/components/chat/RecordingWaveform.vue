<script setup lang="ts">
import { ref, watch, onBeforeUnmount } from 'vue'

const props = withDefaults(defineProps<{
  stream: MediaStream | null
  bars?: number
}>(), {
  bars: 40
})

const MIN_LEVEL = 0.08
const SAMPLE_MS = 60

const levels = ref<number[]>(Array(props.bars).fill(MIN_LEVEL))

let audioCtx: AudioContext | null = null
let analyser: AnalyserNode | null = null
let source: MediaStreamAudioSourceNode | null = null
let buffer: Uint8Array | null = null
let rafId = 0
let lastSample = 0

function teardown() {
  if (rafId) cancelAnimationFrame(rafId)
  rafId = 0
  source?.disconnect()
  source = null
  analyser = null
  buffer = null
  if (audioCtx && audioCtx.state !== 'closed') void audioCtx.close()
  audioCtx = null
  levels.value = Array(props.bars).fill(MIN_LEVEL)
}

function tick(now: number) {
  rafId = requestAnimationFrame(tick)
  if (!analyser || !buffer || now - lastSample < SAMPLE_MS) return
  lastSample = now

  analyser.getByteTimeDomainData(buffer)
  let sum = 0
  for (let i = 0; i < buffer.length; i++) {
    const v = (buffer[i] - 128) / 128
    sum += v * v
  }
  const rms = Math.sqrt(sum / buffer.length)
  // Speech RMS rarely exceeds ~0.3; scale so normal talking fills the bar.
  const level = Math.min(1, Math.max(MIN_LEVEL, rms * 3.5))
  levels.value = [...levels.value.slice(1), level]
}

function setup(stream: MediaStream) {
  const Ctx = window.AudioContext || (window as any).webkitAudioContext
  if (!Ctx) return
  audioCtx = new Ctx()
  analyser = audioCtx.createAnalyser()
  analyser.fftSize = 1024
  analyser.smoothingTimeConstant = 0.6
  source = audioCtx.createMediaStreamSource(stream)
  source.connect(analyser)
  buffer = new Uint8Array(analyser.fftSize)
  lastSample = 0
  rafId = requestAnimationFrame(tick)
}

watch(() => props.stream, (stream) => {
  teardown()
  if (stream) setup(stream)
}, { immediate: true })

onBeforeUnmount(teardown)
</script>

<template>
  <div class="flex h-7 flex-1 items-center gap-[2px] overflow-hidden" aria-hidden="true">
    <span
      v-for="(level, i) in levels"
      :key="i"
      class="w-[3px] shrink-0 rounded-full bg-red-400 light:bg-red-500 transition-[height] duration-75 ease-out"
      :style="{ height: `${Math.round(level * 100)}%` }"
    />
  </div>
</template>
