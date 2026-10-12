<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import type { WidgetData } from '@/services/api'
import { Line } from '@/lib/charts'

const props = defineProps<{
  displayType: string
  data?: WidgetData
  color?: string
  config?: Record<string, any>
  formatNumber: (n: number) => string
}>()

const sparklineChart = computed(() => {
  const points = props.data?.chart_data || []
  return {
    labels: points.map(p => p.label),
    datasets: [{
      data: points.map(p => p.value),
      borderColor: 'rgb(59, 130, 246)',
      backgroundColor: 'rgba(59, 130, 246, 0.1)',
      fill: true,
      tension: 0.3,
      pointRadius: 0,
    }],
  }
})

const sparklineOptions = {
  responsive: true,
  maintainAspectRatio: false,
  plugins: { legend: { display: false } },
  scales: { x: { display: false }, y: { display: false } },
}

const gaugePct = computed(() => {
  const target = Number(props.config?.target || 100)
  const value = props.data?.value || 0
  if (!target) return 0
  return Math.max(0, Math.min(100, (value / target) * 100))
})

const maxHeat = computed(() => {
  const cells = props.data?.heatmap?.cells || []
  let max = 0
  for (const row of cells) for (const v of row) if (v > max) max = v
  return max || 1
})

function heatColor(v: number) {
  const intensity = v / maxHeat.value
  return `rgba(59, 130, 246, ${0.08 + intensity * 0.85})`
}

function severityClass(sev: string) {
  if (sev === 'error') return 'text-red-400'
  if (sev === 'warning') return 'text-amber-400'
  return 'text-white/50'
}
</script>

<template>
  <div class="h-full w-full overflow-auto">
    <!-- Sparkline -->
    <div v-if="displayType === 'sparkline'" class="flex flex-col h-full">
      <div class="text-3xl font-bold text-white light:text-gray-900">{{ formatNumber(data?.value || 0) }}</div>
      <div class="flex-1 min-h-[48px] mt-2">
        <Line v-if="(data?.chart_data?.length || 0) > 0" :data="sparklineChart" :options="sparklineOptions" />
      </div>
    </div>

    <!-- Gauge -->
    <div v-else-if="displayType === 'gauge'" class="flex flex-col items-center justify-center h-full gap-2">
      <div class="relative h-24 w-24">
        <svg viewBox="0 0 36 36" class="h-24 w-24 -rotate-90">
          <path d="M18 2.0845 a 15.9155 15.9155 0 0 1 0 31.831 a 15.9155 15.9155 0 0 1 0 -31.831" fill="none" stroke="currentColor" stroke-width="3" class="text-white/10" />
          <path d="M18 2.0845 a 15.9155 15.9155 0 0 1 0 31.831 a 15.9155 15.9155 0 0 1 0 -31.831" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" class="text-blue-400" :stroke-dasharray="`${gaugePct}, 100`" />
        </svg>
        <div class="absolute inset-0 flex items-center justify-center text-lg font-bold text-white light:text-gray-900 rotate-0">{{ Math.round(gaugePct) }}%</div>
      </div>
      <div class="text-sm text-white/50">{{ formatNumber(data?.value || 0) }} / {{ formatNumber(Number(config?.target || 100)) }}</div>
    </div>

    <!-- Leaderboard -->
    <div v-else-if="displayType === 'leaderboard'" class="space-y-2">
      <div v-for="row in (data?.leaderboard || [])" :key="row.rank" class="flex items-center justify-between text-sm">
        <div class="flex items-center gap-2 text-white/80 light:text-gray-700">
          <span class="w-5 text-white/40">{{ row.rank }}</span>
          <span class="truncate">{{ row.label }}</span>
        </div>
        <span class="font-medium text-white light:text-gray-900">{{ formatNumber(row.value) }}</span>
      </div>
      <div v-if="!(data?.leaderboard?.length)" class="text-sm text-white/40">No data</div>
    </div>

    <!-- Heatmap -->
    <div v-else-if="displayType === 'heatmap'" class="overflow-auto">
      <div class="grid gap-0.5" :style="{ gridTemplateColumns: `32px repeat(${data?.heatmap?.x_labels?.length || 7}, minmax(18px, 1fr))` }">
        <div />
        <div v-for="x in (data?.heatmap?.x_labels || [])" :key="x" class="text-[10px] text-center text-white/40">{{ x }}</div>
        <template v-for="(row, yi) in (data?.heatmap?.cells || [])" :key="yi">
          <div class="text-[10px] text-white/40 pr-1 text-right">{{ data?.heatmap?.y_labels?.[yi] }}</div>
          <div
            v-for="(cell, xi) in row"
            :key="`${yi}-${xi}`"
            class="h-3 rounded-sm"
            :style="{ backgroundColor: heatColor(cell) }"
            :title="`${cell}`"
          />
        </template>
      </div>
    </div>

    <!-- Funnel -->
    <div v-else-if="displayType === 'funnel'" class="space-y-2">
      <div v-for="stage in (data?.funnel || [])" :key="stage.stage" class="space-y-1">
        <div class="flex justify-between text-xs text-white/50">
          <span>{{ stage.stage }}</span>
          <span>{{ formatNumber(stage.value) }}</span>
        </div>
        <div class="h-2 rounded bg-white/10 overflow-hidden">
          <div
            class="h-full bg-blue-500/80"
            :style="{ width: `${Math.max(4, (stage.value / Math.max(...(data?.funnel || []).map(s => s.value), 1)) * 100)}%` }"
          />
        </div>
      </div>
    </div>

    <!-- KPI strip -->
    <div v-else-if="displayType === 'kpi_strip'" class="grid grid-cols-2 md:grid-cols-4 gap-3 h-full">
      <div v-for="kpi in (data?.kpis || [])" :key="kpi.label" class="rounded-lg bg-white/[0.04] p-3 light:bg-gray-50">
        <div class="text-xs text-white/40">{{ kpi.label }}</div>
        <div class="text-xl font-bold text-white light:text-gray-900">{{ kpi.unit === '%' ? `${kpi.value.toFixed(1)}%` : formatNumber(kpi.value) }}</div>
        <div v-if="kpi.change != null" class="text-xs text-white/40">{{ kpi.change.toFixed(1) }}{{ kpi.unit === '%' ? ' pts' : '%' }}</div>
      </div>
    </div>

    <!-- Alert list -->
    <div v-else-if="displayType === 'alert_list'" class="space-y-2">
      <div v-for="(a, i) in (data?.alerts || [])" :key="i" class="flex items-start justify-between gap-2 text-sm border-b border-white/[0.06] pb-2">
        <div>
          <div :class="['font-medium', severityClass(a.severity)]">{{ a.title }}</div>
          <div class="text-white/50 text-xs">{{ a.subtitle }}</div>
        </div>
        <RouterLink v-if="a.href" :to="a.href" class="text-xs text-blue-400 hover:underline">View</RouterLink>
      </div>
      <div v-if="!(data?.alerts?.length)" class="text-sm text-white/40">No alerts</div>
    </div>

    <!-- Queue -->
    <div v-else-if="displayType === 'queue'" class="space-y-2">
      <div v-for="q in (data?.queue_items || [])" :key="q.id" class="flex items-center justify-between text-sm">
        <RouterLink :to="`/chat/${q.contact_id}`" class="truncate text-white/80 hover:text-white">{{ q.label }}</RouterLink>
        <span class="text-xs text-amber-400 whitespace-nowrap">{{ Math.round(q.wait_mins) }}m · {{ q.source }}</span>
      </div>
      <div v-if="!(data?.queue_items?.length)" class="text-sm text-white/40">Queue empty</div>
    </div>

    <!-- Agent status -->
    <div v-else-if="displayType === 'agent_status'" class="grid grid-cols-1 sm:grid-cols-2 gap-2">
      <div v-for="ag in (data?.agents || [])" :key="ag.id" class="flex items-center justify-between rounded-lg bg-white/[0.04] px-3 py-2 text-sm">
        <div class="flex items-center gap-2">
          <span :class="['h-2 w-2 rounded-full', ag.is_available ? 'bg-emerald-400' : 'bg-amber-400']" />
          <span class="text-white/80">{{ ag.name }}</span>
        </div>
        <span class="text-xs text-white/40">{{ ag.active_transfers }} active</span>
      </div>
      <div v-if="!(data?.agents?.length)" class="text-sm text-white/40">No agents</div>
    </div>

    <!-- Iframe -->
    <div v-else-if="displayType === 'iframe'" class="h-full w-full">
      <iframe
        v-if="config?.url"
        :src="String(config.url)"
        class="h-full w-full rounded-md border-0 bg-white"
        sandbox="allow-scripts allow-same-origin allow-forms allow-popups"
        referrerpolicy="no-referrer"
        title="Embedded widget"
      />
      <div v-else class="text-sm text-white/40">No URL configured</div>
    </div>
  </div>
</template>
