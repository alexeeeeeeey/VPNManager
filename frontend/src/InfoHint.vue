<script setup lang="ts">
import { ref } from 'vue'
import { Info } from 'lucide-vue-next'
import {
  TooltipContent,
  TooltipPortal,
  TooltipProvider,
  TooltipRoot,
  TooltipTrigger,
} from 'reka-ui'

defineProps<{ text: string }>()
const open = ref(false)
</script>

<template>
  <TooltipProvider :delay-duration="180">
    <TooltipRoot v-model:open="open" disable-closing-trigger>
      <TooltipTrigger as-child>
        <span
          class="info-hint"
          role="button"
          tabindex="0"
          aria-label="Пояснение"
          @click.stop.prevent="open = true"
          @keydown.enter.stop.prevent="open = true"
          @keydown.space.stop.prevent="open = true"
        >
          <Info :size="15" aria-hidden="true" />
        </span>
      </TooltipTrigger>
      <TooltipPortal>
        <TooltipContent
          class="info-tooltip"
          :side-offset="8"
          :collision-padding="12"
        >
          {{ text }}
        </TooltipContent>
      </TooltipPortal>
    </TooltipRoot>
  </TooltipProvider>
</template>
