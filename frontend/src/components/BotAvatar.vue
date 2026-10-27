<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <span class="bot-avatar" :style="{ width: `${size}px`, height: `${size}px`, fontSize: `${Math.round(size * 0.4)}px` }" aria-hidden="true">
    <img v-if="src && !broken" :src="src" alt="" @error="broken = true" />
    <template v-else>{{ initial }}</template>
  </span>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";

// 圆形头像：有图用图，图挂了或没有就用名字的第一个字。
const props = withDefaults(defineProps<{ name: string; src?: string; size?: number }>(), { src: "", size: 40 });
const broken = ref(false);
const initial = computed(() => Array.from(props.name.trim())[0]?.toUpperCase() || "?");
watch(() => props.src, () => (broken.value = false));
</script>

<style scoped>
.bot-avatar {
  display: inline-grid;
  place-items: center;
  flex-shrink: 0;
  overflow: hidden;
  border-radius: 50%;
  background: color-mix(in srgb, var(--accent) 22%, var(--surface-2));
  color: var(--accent);
  font-weight: 600;
  line-height: 1;
}

.bot-avatar img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}
</style>
