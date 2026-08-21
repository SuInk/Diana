<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="plugin-dependencies-body">
    <p v-if="loading && dependencies.length === 0" class="plugin-dependencies-empty">正在检测依赖...</p>
    <p v-else-if="dependencies.length === 0" class="plugin-dependencies-empty">暂时无法读取依赖状态</p>
    <div v-else class="plugin-dependency-list">
      <div v-for="dep in dependencies" :key="dep.name" class="plugin-dependency-row">
        <div class="plugin-dependency-main">
          <strong class="mono">{{ dep.name }}</strong>
          <span>{{ dep.purpose }}</span>
          <!-- 装不了的依赖只显示「需手动安装」等于让人自己猜是没装、装错架构
               还是路径没配；后端探到什么就照说什么。 -->
          <span v-if="!dep.available && dep.detail" class="plugin-dependency-detail">{{ dep.detail }}</span>
        </div>
        <span
          v-if="dep.available"
          class="badge accent plugin-dependency-status"
          :title="[dep.version, dep.path].filter(Boolean).join(' · ')"
        >
          {{ dep.version || "已安装" }}
        </span>
        <button
          v-else-if="dep.installable"
          class="btn small"
          type="button"
          :disabled="busy !== ''"
          :title="`使用 ${dep.installer || '系统包管理器'} 安装 ${dep.name}`"
          @click="emit('install', dep)"
        >
          <LoaderCircle v-if="busy === dep.name" class="spin" :size="14" aria-hidden="true" />
          <Download v-else :size="14" aria-hidden="true" />
          {{ busy === dep.name ? "安装中" : "安装" }}
        </button>
        <span v-else class="badge warn">需手动安装</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { Download, LoaderCircle } from "@lucide/vue";
import type { ResolverDependency } from "../api";

defineProps<{ dependencies: ResolverDependency[]; loading: boolean; busy: string }>();
const emit = defineEmits<{ install: [dependency: ResolverDependency] }>();
</script>

<style scoped>
/* 和名称、用途一样是 11px，但用告警色：这行讲的是「为什么现在用不了」，
   埋在灰字里就等于没写。 */
.plugin-dependency-detail {
  color: var(--warn);
}
</style>
