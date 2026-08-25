<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <!-- 查不到就整行不渲染：输入框里那串号码本身没问题，没必要为「查不到」占一行。 -->
  <span v-if="name" class="account-name-hint" :title="`${userID} · ${name}`">
    <UserRound :size="12" aria-hidden="true" />
    <span class="account-name-text">{{ name }}</span>
  </span>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import { UserRound } from "@lucide/vue";
import { fetchAssistantUserNames } from "../api";

// 「一串数字换个看得懂的名字」。控制台里每个填 QQ 号 / 用户 ID 的输入框旁边挂一个，
// 填完不用再切到别处去确认自己有没有填错号。
const props = defineProps<{
  /** 要解析的用户 ID；空值或非纯数字都直接不查。 */
  userId?: string;
  /** 机器人作用域，留空表示当前选中的那台。 */
  profile?: string;
}>();

const name = ref("");
const userID = ref("");
// 边打字边查会把没写完的号码也发出去，等手停下来再查。
const debounceMs = 400;
let timer: ReturnType<typeof setTimeout> | null = null;
// 输入变化比响应回来快是常态，只认最后一次发出的请求。
let latest = 0;

function clearTimer(): void {
  if (timer !== null) {
    clearTimeout(timer);
    timer = null;
  }
}

async function resolve(id: string, token: number): Promise<void> {
  try {
    const response = await fetchAssistantUserNames([id], props.profile ?? "");
    if (token !== latest) return;
    name.value = response.names?.[id] ?? "";
    userID.value = id;
  } catch {
    // 查不到名字不该在界面上报错——这只是个锦上添花的回显。
    if (token === latest) name.value = "";
  }
}

watch(
  () => [props.userId, props.profile],
  () => {
    clearTimer();
    const id = (props.userId ?? "").trim();
    latest += 1;
    if (!/^\d+$/.test(id)) {
      name.value = "";
      return;
    }
    if (id !== userID.value) name.value = "";
    const token = latest;
    timer = setTimeout(() => void resolve(id, token), debounceMs);
  },
  { immediate: true }
);

onBeforeUnmount(() => {
  clearTimer();
  latest += 1;
});
</script>

<style scoped>
.account-name-hint {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
  max-width: 100%;
  color: var(--muted);
  font-size: 11.5px;
  line-height: 1.4;
}

.account-name-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
