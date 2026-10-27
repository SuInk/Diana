<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="owner-bind">
    <span v-if="!profile" class="hint">QQ 官方机器人收不到 QQ 号，主人要绑定一次。先保存机器人并连上，再回这里生成绑定验证码。</span>
    <template v-else-if="!modelValue">
      <span class="hint">QQ 官方机器人收不到 QQ 号，主人要绑定一次。没绑定前，机器人会在每个会话里提醒一次。</span>
      <div v-if="command" class="owner-bind-code" role="status">
        <span>把这句发给机器人（私聊或在群里 @ 它）：</span>
        <div class="input-group">
          <input class="input mono" :value="command" readonly aria-label="绑定指令" />
          <button class="btn icon-only" type="button" aria-label="复制绑定指令" @click="copyCommand">
            <Copy :size="14" aria-hidden="true" />
          </button>
        </div>
        <span class="hint">{{ remaining > 0 ? `${Math.ceil(remaining / 60)} 分钟内有效，绑定后自动填入主人。` : "验证码已过期，请重新生成。" }}</span>
      </div>
      <span v-if="error" class="hint owner-bind-error">{{ error }}</span>
      <button class="btn small" type="button" :disabled="busy" @click="generate">
        <KeyRound :size="13" aria-hidden="true" />
        {{ command ? "重新生成验证码" : "生成绑定验证码" }}
      </button>
    </template>
    <span v-else-if="justBound" class="hint">已绑定。主人以后私聊或在群里说话，机器人都能认出来。</span>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import { Copy, KeyRound } from "@lucide/vue";
import { createOwnerBindCode, fetchOwnerBindStatus } from "../api";
import { toastError, toastSuccess } from "../lib/toast";

// QQ 官方机器人只给 openid，主人不知道自己的 openid，只能让机器人认：这里生成验证码，
// 主人发给机器人，后端把发送者存成主人，这边轮询到了就填回表单。
const props = defineProps<{
  modelValue?: string;
  /** 已保存的机器人 ID；新建还没保存时为空。 */
  profile?: string;
}>();
const emit = defineEmits<{ "update:modelValue": [id: string] }>();

const pollMs = 3000;
const command = ref("");
const remaining = ref(0);
const busy = ref(false);
const error = ref("");
const justBound = ref(false);
let timer: ReturnType<typeof setInterval> | null = null;

function stop(): void {
  if (timer !== null) {
    clearInterval(timer);
    timer = null;
  }
}

async function poll(): Promise<void> {
  const profile = props.profile ?? "";
  if (!profile) return stop();
  remaining.value = Math.max(0, remaining.value - pollMs / 1000);
  try {
    const status = await fetchOwnerBindStatus(profile);
    if (status.owner_id) {
      stop();
      command.value = "";
      justBound.value = true;
      emit("update:modelValue", status.owner_id);
      return;
    }
    if (!status.pending) {
      remaining.value = 0;
      stop();
    }
  } catch {
    // 轮询失败不打扰：下一轮再试，验证码本身还有效。
  }
}

async function generate(): Promise<void> {
  const profile = props.profile ?? "";
  if (!profile) return;
  busy.value = true;
  error.value = "";
  try {
    const result = await createOwnerBindCode(profile);
    command.value = result.command;
    remaining.value = result.expires_in_seconds;
    stop();
    timer = setInterval(() => void poll(), pollMs);
  } catch (err) {
    error.value = `生成验证码失败：${err instanceof Error ? err.message : String(err)}`;
  } finally {
    busy.value = false;
  }
}

async function copyCommand(): Promise<void> {
  try {
    await navigator.clipboard.writeText(command.value);
    toastSuccess("绑定指令已复制");
  } catch {
    toastError("复制失败，请手动选中");
  }
}

watch(
  () => props.profile,
  () => {
    stop();
    command.value = "";
    justBound.value = false;
    error.value = "";
  }
);

onBeforeUnmount(stop);
</script>

<style scoped>
.owner-bind {
  display: grid;
  gap: 6px;
  justify-items: start;
  min-width: 0;
}

.owner-bind-error {
  color: var(--err);
}

.owner-bind-code {
  display: grid;
  gap: 4px;
  width: 100%;
  min-width: 0;
}
</style>
