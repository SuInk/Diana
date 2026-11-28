<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<script setup lang="ts">
import { computed } from 'vue'
import { linkify } from '../../../shared/linkify.ts'

const props = defineProps<{ text: string }>()
const parts = computed(() => linkify(props.text))
</script>

<template>
  <span class="rich-text"><template v-for="(part, index) in parts" :key="index"><a v-if="part.href" :href="part.href" target="_blank" rel="noreferrer">{{ part.text }}</a><template v-else>{{ part.text }}</template></template></span>
</template>
