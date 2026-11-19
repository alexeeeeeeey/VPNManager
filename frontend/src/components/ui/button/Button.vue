<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { Primitive, type PrimitiveProps } from 'reka-ui'
import { cva } from 'class-variance-authority'
import { clsx } from 'clsx'
import { twMerge } from 'tailwind-merge'

const props = defineProps<
  PrimitiveProps & {
    variant?: 'default' | 'primary' | 'destructive'
    class?: HTMLAttributes['class']
  }
>()

const variants = cva('button', {
  variants: {
    variant: { default: '', primary: 'primary', destructive: 'destructive' },
  },
  defaultVariants: { variant: 'default' },
})
</script>

<template>
  <Primitive
    :as="props.as || 'button'"
    :as-child="props.asChild"
    :class="twMerge(clsx(variants({ variant: props.variant }), props.class))"
  >
    <slot />
  </Primitive>
</template>
