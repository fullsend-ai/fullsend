<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'

const props = defineProps<{
  graph: string
  id: string
}>()

const svg = ref<string | null>(null)
const error = ref<string | null>(null)
let observer: MutationObserver | null = null
let lastTheme: string | null = null
let rendering = false
let renderSeq = 0

async function renderChart() {
  if (rendering) return
  rendering = true
  const seq = ++renderSeq
  try {
    const mermaid = (await import('mermaid')).default
    const isDark = document.documentElement.classList.contains('dark')
    const theme = isDark ? 'dark' : 'default'
    if (theme !== lastTheme) {
      mermaid.initialize({
        securityLevel: 'strict',
        startOnLoad: false,
        theme,
      })
      lastTheme = theme
    }
    const { svg: rendered } = await mermaid.render(
      `${props.id}-${seq}`,
      decodeURIComponent(props.graph),
    )
    svg.value = rendered
    error.value = null
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Failed to render diagram'
    svg.value = null
  } finally {
    rendering = false
  }
}

onMounted(async () => {
  await renderChart()
  observer = new MutationObserver(() => renderChart())
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
})

onUnmounted(() => observer?.disconnect())
</script>

<template>
  <div v-if="error" class="mermaid-error">{{ error }}</div>
  <div v-else-if="svg" v-html="svg" />
  <div v-else class="mermaid-loading">Loading diagram...</div>
</template>
