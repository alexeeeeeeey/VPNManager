<script setup lang="ts">
import {
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
} from 'reka-ui'
import { X } from 'lucide-vue-next'
defineProps<{ title: string; description: string }>()
const open = defineModel<boolean>({ required: true })
</script>

<template>
  <DialogRoot v-model:open="open">
    <DialogPortal>
      <DialogOverlay class="overlay" />
      <DialogContent class="dialog panel">
        <div class="section-header">
          <div>
            <DialogTitle as="h2">{{ title }}</DialogTitle><DialogDescription
              as="p"
              >{{ description }}</DialogDescription
            >
          </div>
          <DialogClose class="icon-button" aria-label="Закрыть"
            ><X :size="20"
          /></DialogClose>
        </div>
        <slot />
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
