<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<script setup lang="ts">
import { LoaderCircle, TriangleAlert } from '@lucide/vue'

defineProps<{ error: string }>()
const emit = defineEmits<{ retry: []; 'use-remote': [] }>()
</script>

<template>
  <main class="login">
    <div class="titlebar-drag" aria-hidden="true" />
    <div class="login-form" aria-live="polite">
      <div class="login-brand">
        <span class="login-logo" aria-hidden="true">D</span>
        <div>
          <h1>Diana</h1>
          <p class="muted">{{ error ? '本机 Diana 没能启动' : '正在启动本机 Diana…' }}</p>
        </div>
      </div>
      <template v-if="error">
        <p class="form-error startup-error" role="alert"><TriangleAlert :size="15" aria-hidden="true" /> {{ error }}</p>
        <button class="button primary" type="button" @click="emit('retry')">重试</button>
      </template>
      <p v-else class="startup-progress muted"><LoaderCircle :size="16" class="spin" aria-hidden="true" /> 第一次打开要初始化数据，稍等几秒</p>
      <button id="use-remote" class="button link" type="button" @click="emit('use-remote')">连接其他服务器上的 Diana</button>
    </div>
  </main>
</template>
