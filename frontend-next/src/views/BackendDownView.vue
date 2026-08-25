<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="backend-down">
    <div class="backend-down-card">
      <span class="backend-down-mark">
        <PlugZap :size="22" aria-hidden="true" />
      </span>
      <h1>连不上 Diana 后端</h1>
      <p class="muted">页面本身是静态文件，浏览器能打开；但它背后的服务没有应答，所以这里没有任何数据可显示。</p>

      <p v-if="message" class="backend-down-detail mono">{{ message }}</p>

      <ul class="backend-down-hints">
        <li>确认 Diana 进程还在运行，没有崩掉或正在重启。</li>
        <li>确认访问的地址和端口没写错；用了 Nginx 之类的反代时，检查它能不能连上后端。</li>
        <li>后端刚重启的话等几秒——下面会自动重试。</li>
      </ul>

      <button class="btn primary" type="button" :disabled="retrying" @click="emit('retry')">
        <RefreshCw :size="15" :class="{ spin: retrying }" aria-hidden="true" />
        {{ retrying ? "正在重试…" : "重新连接" }}
      </button>
      <p class="muted backend-down-countdown">{{ retrying ? "　" : `${countdown} 秒后自动重试` }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import { PlugZap, RefreshCw } from "@lucide/vue";

const props = defineProps<{ message?: string; retrying?: boolean; retryInSeconds: number }>();
const emit = defineEmits<{ retry: [] }>();

const countdown = ref(props.retryInSeconds);
let timer: ReturnType<typeof setInterval> | null = null;

function restart(): void {
  if (timer) clearInterval(timer);
  countdown.value = props.retryInSeconds;
  timer = setInterval(() => {
    if (countdown.value > 0) countdown.value -= 1;
  }, 1000);
}

// 每次重试失败后 App 会把等待时间退避加长，倒计时跟着重新开始。
watch(() => props.retryInSeconds, restart, { immediate: true });

onBeforeUnmount(() => {
  if (timer) clearInterval(timer);
});
</script>
