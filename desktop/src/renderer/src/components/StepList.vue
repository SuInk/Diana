<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<script setup lang="ts">
// Agent 干活的步骤：每个工具调用一行，工具名、在干什么（路径、命令、网址）、耗时、成没成。
// 回复上方默认收起成一行「执行了 N 步」，正在跑的那轮直接展开。
import { computed, ref } from 'vue'
import { ChevronRight, CircleCheck, CircleX, LoaderCircle } from '@lucide/vue'
import type { TaskStep } from '../../../shared/types.ts'

const props = defineProps<{ steps: TaskStep[]; current?: TaskStep | null; live?: boolean }>()
const open = ref(false)
const expanded = computed(() => props.live || open.value)
const failed = computed(() => props.steps.filter((step) => step.failed).length)

function duration(ms?: number): string {
  if (!ms) return ''
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)} s`
}
</script>

<template>
  <div v-if="steps.length || current" class="steps" :class="{ live }">
    <button v-if="!live" class="steps-toggle" type="button" :aria-expanded="expanded" @click="open = !open">
      <ChevronRight :size="14" class="steps-chevron" aria-hidden="true" />
      执行了 {{ steps.length }} 步<span v-if="failed" class="steps-failed">，{{ failed }} 步出错</span>
    </button>
    <ol v-if="expanded" class="step-list">
      <li v-for="(step, index) in steps" :key="index" class="step" :class="{ failed: step.failed }">
        <CircleX v-if="step.failed" :size="14" class="step-icon" aria-label="出错" />
        <CircleCheck v-else :size="14" class="step-icon" aria-label="完成" />
        <span class="step-tool">{{ step.tool }}</span>
        <span class="step-summary" :title="step.error || step.summary">{{ step.error || step.summary }}</span>
        <span class="step-duration">{{ duration(step.duration_ms) }}</span>
      </li>
      <li v-if="current" class="step running">
        <LoaderCircle :size="14" class="step-icon spin" aria-label="进行中" />
        <span class="step-tool">{{ current.tool }}</span>
        <span class="step-summary" :title="current.summary">{{ current.summary }}</span>
      </li>
    </ol>
  </div>
</template>
