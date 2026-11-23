<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { mediaURL } from '../lib/media.ts'

const props = defineProps<{ name: string; src?: string; size?: number }>()
const failed = ref(false)
watch(() => props.src, () => (failed.value = false))
const url = computed(() => (failed.value ? undefined : mediaURL(props.src)))
const initial = computed(() => Array.from(props.name.trim())[0] ?? '?')
</script>

<template>
  <span class="avatar" :style="{ width: `${size ?? 36}px`, height: `${size ?? 36}px`, fontSize: `${(size ?? 36) * 0.42}px` }">
    <img v-if="url" :src="url" alt="" @error="failed = true" />
    <span v-else aria-hidden="true">{{ initial }}</span>
  </span>
</template>
