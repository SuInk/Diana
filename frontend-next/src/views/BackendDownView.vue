<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="backend-down">
    <div class="backend-down-card">
      <span class="backend-down-mark" :class="{ waiting }">
        <LoaderCircle v-if="waiting" class="spin" :size="22" aria-hidden="true" />
        <PlugZap v-else :size="22" aria-hidden="true" />
      </span>

      <!-- 等待期和故障期是两件事：一次 10 秒的升级重启不该配一张红色报错页，
           而一台真的关掉的机器也不该被写成「马上就好」。 -->
      <template v-if="waiting">
        <h1>{{ updating ? "正在重启并更新" : "正在连接 Diana" }}</h1>
        <p class="muted">
          {{ updating
            ? "新版本装好后旧进程会退出，由新进程接管，中间有十几秒没人应答。连上就会自动回到刚才的页面。"
            : "后端暂时没有应答，可能正在重启。稍等一下，连上会自动继续。" }}
        </p>
      </template>
      <template v-else>
        <h1>连不上 Diana 后端</h1>
        <p class="muted">页面本身是静态文件，浏览器能打开；但它背后的服务已经有一会儿没有应答了。</p>

        <p v-if="message" class="backend-down-detail mono">{{ message }}</p>

        <ul class="backend-down-hints">
          <li>确认 Diana 进程还在运行，没有崩掉或正在重启。</li>
          <li>确认访问的地址和端口没写错；用了 Nginx 之类的反代时，检查它能不能连上后端。</li>
          <li>刚触发过升级的话，装完还要跑健康检查，可能要再多等一会儿。</li>
        </ul>
      </template>

      <button class="btn" :class="{ primary: !waiting }" type="button" :disabled="retrying" @click="emit('retry')">
        <RefreshCw :size="15" :class="{ spin: retrying }" aria-hidden="true" />
        {{ retrying ? "正在重试…" : "立即重试" }}
      </button>
      <!-- 宽限期里每秒都在悄悄重试，倒计时说的是「数到 0 就整页重载一次」。 -->
      <p class="muted backend-down-countdown">
        {{ waiting ? `${graceLeft} 秒后自动重新加载` : retrying ? "　" : `${countdown} 秒后自动重试` }}
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import { LoaderCircle, PlugZap, RefreshCw } from "@lucide/vue";

const props = defineProps<{
  message?: string;
  retrying?: boolean;
  retryInSeconds: number;
  // waiting 是宽限期：还在按秒重试，先别把话说死。
  waiting?: boolean;
  // updating 表示这次断线是刚点过的升级造成的，可以直接说清楚在等什么。
  updating?: boolean;
  // graceLeft 是宽限期剩余秒数，归零时整页重载一次。
  graceLeft?: number;
}>();
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

// 宽限期过后 App 会把等待时间退避加长，倒计时跟着重新开始。
watch(() => props.retryInSeconds, restart, { immediate: true });

onBeforeUnmount(() => {
  if (timer) clearInterval(timer);
});
</script>
