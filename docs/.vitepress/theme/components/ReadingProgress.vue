<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'

const width = ref(0)

function onScroll() {
  const { scrollTop, scrollHeight, clientHeight } = document.documentElement
  const total = scrollHeight - clientHeight
  width.value = total > 0 ? (scrollTop / total) * 100 : 0
}

onMounted(() => {
  window.addEventListener('scroll', onScroll, { passive: true })
})

onUnmounted(() => {
  window.removeEventListener('scroll', onScroll)
})
</script>

<template>
  <div class="reading-progress" :style="{ width: `${width}%` }" />
</template>
