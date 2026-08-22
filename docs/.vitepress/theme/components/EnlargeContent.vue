<script setup lang="ts">
import { ref, onMounted, nextTick } from 'vue'
import { onContentUpdated } from 'vitepress'
import EnlargeDialog from './EnlargeDialog.vue'

// Mounted in the doc-after slot: gives markdown tables and fenced code
// blocks an "Enlarge" pill that opens them in the shared dialog, where
// they get the whole viewport instead of the content column's scroll
// box. Mermaid diagrams do the same through Mermaid.vue.
//
// Runs on mount and whenever VitePress swaps the page content
// (onContentUpdated fires after the new markdown DOM is in place).
// Nodes from the previous page are detached by that swap, so nothing is
// retained here; an open dialog is closed so it cannot outlive its page.
//
// Code blocks are enhanced only when they overflow sideways or are long;
// a three-line snippet does not need it and the pill would be noise.
const CODE_MIN_LINES = 15

const enlarge = ref<InstanceType<typeof EnlargeDialog> | null>(null)

function makeButton(label: string, onClick: () => void, extraClass = ''): HTMLButtonElement {
  const button = document.createElement('button')
  button.type = 'button'
  button.className = `enlargeable__hint ${extraClass}`.trim()
  button.textContent = '⤢ Enlarge'
  button.setAttribute('aria-label', label)
  button.addEventListener('click', onClick)
  return button
}

function insideDialog(el: Element): boolean {
  return el.closest('.enlarge-dialog') !== null
}

function enhanceTables() {
  document.querySelectorAll<HTMLTableElement>('.vp-doc table').forEach((table) => {
    if (insideDialog(table)) return
    if (table.parentElement?.classList.contains('enlargeable--table')) return
    const wrap = document.createElement('div')
    wrap.className = 'enlargeable enlargeable--table'
    table.parentNode?.insertBefore(wrap, table)
    wrap.appendChild(table)
    wrap.appendChild(
      makeButton('Enlarge table', () => enlarge.value?.show(table.outerHTML, null, 'Table')),
    )
  })
}

function enhanceCodeBlocks() {
  document.querySelectorAll<HTMLElement>(".vp-doc div[class*='language-']").forEach((block) => {
    if (insideDialog(block)) return
    if (block.classList.contains('enlargeable')) return
    const pre = block.querySelector('pre')
    if (!pre) return
    const lines = (pre.textContent ?? '').split('\n').length
    const overflows = pre.scrollWidth > pre.clientWidth + 1
    if (!overflows && lines < CODE_MIN_LINES) return
    block.classList.add('enlargeable', 'enlargeable--code')
    block.appendChild(
      makeButton(
        'Enlarge code block',
        () => {
          const clone = block.cloneNode(true) as HTMLElement
          clone.classList.remove('enlargeable', 'enlargeable--code')
          clone.querySelectorAll('button').forEach((b) => b.remove())
          enlarge.value?.show(clone.outerHTML, null, 'Code')
        },
        'enlargeable__hint--code',
      ),
    )
  })
}

async function refresh() {
  enlarge.value?.close()
  await nextTick()
  enhanceTables()
  enhanceCodeBlocks()
}

onMounted(refresh)
onContentUpdated(refresh)
</script>

<template>
  <EnlargeDialog ref="enlarge" title="Content" />
</template>
