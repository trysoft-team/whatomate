<script setup lang="ts">
import type { DropdownMenuTriggerProps } from 'reka-ui'
import { DropdownMenuTrigger as DropdownMenuTriggerPrimitive } from 'reka-ui'

const props = defineProps<DropdownMenuTriggerProps>()
</script>

<template>
  <DropdownMenuTriggerPrimitive v-bind="props">
    <slot />
  </DropdownMenuTriggerPrimitive>
</template>
