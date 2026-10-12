<template>
  <span class="skeleton skeleton-block" :class="{ 'skeleton-inline': inline }" :style="{ width, height, borderRadius: rounded ? '999px' : undefined }" aria-hidden="true" />
</template>

<script setup lang="ts">
withDefaults(defineProps<{ width?: string; height?: string; rounded?: boolean; inline?: boolean }>(), {
  width: "100%",
  height: "1em"
});
</script>
