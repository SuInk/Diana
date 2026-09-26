<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="vrchat-status">
    <div class="vrchat-status-head">
      <div>
        <h3>实时状态</h3>
        <p>OSC 桥是整个程序共用的：任意一台机器人启用插件就会运行。这里每几秒刷新一次，改完设置保存后生效。</p>
      </div>
      <span class="badge" :class="status?.enabled ? 'ok' : ''">{{ status?.enabled ? "运行中" : "未运行" }}</span>
    </div>

    <p v-if="error" class="vrchat-status-error">{{ error }}</p>
    <template v-else-if="status">
      <dl class="vrchat-status-grid">
        <dt>发送到</dt>
        <dd>{{ status.send_address || "—" }}</dd>
        <dt>监听</dt>
        <dd>
          <span v-if="status.listening">{{ status.listen_address }}</span>
          <span v-else-if="status.listen_error" class="vrchat-status-warn">监听失败：{{ status.listen_error }}</span>
          <span v-else>未监听</span>
        </dd>
        <dt>最后收到</dt>
        <dd>{{ status.last_packet_at ? formatRelative(status.last_packet_at) : "还没收到 VRChat 的数据" }}</dd>
        <dt>当前 Avatar</dt>
        <dd>{{ status.avatar_id || "未知（切换一次 Avatar 后显示）" }}</dd>
        <dt>表情</dt>
        <dd>
          {{ status.expression || "—" }}
          <span v-if="status.expression" class="vrchat-status-muted">{{ status.expression_by_mood ? "（心情）" : "（Agent）" }}</span>
        </dd>
        <dt>聊天框</dt>
        <dd>
          {{ status.last_chatbox ? truncate(status.last_chatbox, 60) : "—" }}
          <span v-if="status.chatbox_pending" class="vrchat-status-muted">排队 {{ status.chatbox_pending }} 段</span>
        </dd>
        <template v-if="status.active_inputs?.length">
          <dt>正在移动</dt>
          <dd>{{ status.active_inputs.join("、") }}</dd>
        </template>
      </dl>

      <div v-if="problems.length" class="vrchat-status-problems">
        <strong>映射表有 {{ problems.length }} 行没生效</strong>
        <ul>
          <li v-for="problem in problems" :key="problem">{{ problem }}</li>
        </ul>
      </div>
      <p v-if="status.last_error || status.apply_error" class="vrchat-status-warn">{{ status.apply_error || status.last_error }}</p>

      <div v-if="status.params?.length" class="vrchat-status-params">
        <strong>最近变化的 Avatar 参数</strong>
        <ul>
          <li v-for="param in status.params" :key="param.name">
            <code>{{ param.name }}</code> = {{ formatValue(param.value) }}
            <span class="vrchat-status-muted">{{ formatRelative(param.updated_at) }}</span>
          </li>
        </ul>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { getVRChatStatus, type VRChatStatus } from "../api";
import { formatRelative, truncate } from "../format";

const status = ref<VRChatStatus | null>(null);
const error = ref("");
let timer: ReturnType<typeof setInterval> | undefined;

const problems = computed(() => status.value?.mapping_problems ?? []);

function formatValue(value: unknown): string {
  if (typeof value === "number") return Number.isInteger(value) ? String(value) : value.toFixed(2);
  return String(value);
}

async function refresh() {
  try {
    status.value = await getVRChatStatus();
    error.value = "";
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  }
}

onMounted(() => {
  void refresh();
  timer = setInterval(() => void refresh(), 3000);
});

onBeforeUnmount(() => {
  if (timer) clearInterval(timer);
});
</script>

<style scoped>
.vrchat-status {
  display: grid;
  gap: 12px;
  margin-top: 16px;
  padding-top: 16px;
  border-top: 1px solid var(--border);
}

.vrchat-status-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.vrchat-status-head h3 {
  margin: 0 0 4px;
  font-size: 14px;
}

.vrchat-status-head p {
  margin: 0;
  color: var(--muted);
  font-size: 12.5px;
}

.vrchat-status-grid {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: 6px 16px;
  margin: 0;
  font-size: 13px;
}

.vrchat-status-grid dt {
  color: var(--muted);
}

.vrchat-status-grid dd {
  margin: 0;
  overflow-wrap: anywhere;
}

.vrchat-status-muted {
  color: var(--muted);
  font-size: 12px;
}

.vrchat-status-warn,
.vrchat-status-error {
  margin: 0;
  color: var(--err);
  font-size: 13px;
}

.vrchat-status-problems,
.vrchat-status-params {
  font-size: 13px;
}

.vrchat-status-problems ul,
.vrchat-status-params ul {
  margin: 6px 0 0;
  padding-left: 18px;
}

.vrchat-status-problems li {
  color: var(--err);
}
</style>
