<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { basicSetup, EditorView } from 'codemirror'
import { json } from '@codemirror/lang-json'

const value = defineModel<string>({ required: true })
const element = ref<HTMLElement>()
let editor: EditorView | undefined
onMounted(() => {
  editor = new EditorView({
    parent: element.value,
    doc: value.value,
    extensions: [
      basicSetup,
      json(),
      EditorView.contentAttributes.of({
        'aria-label': 'Конфигурация Xray JSON',
      }),
      EditorView.theme(
        {
          '&': { minHeight: '200px' },
          '.cm-scroller': { maxHeight: '42vh', overflow: 'auto' },
        },
        { dark: true },
      ),
      EditorView.updateListener.of((update) => {
        if (update.docChanged) value.value = update.state.doc.toString()
      }),
    ],
  })
})
onBeforeUnmount(() => editor?.destroy())
</script>
<template><div ref="element" class="editor" /></template>
