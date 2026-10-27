<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="owner-bind">
    <span v-if="!profile" class="hint">QQ 官方机器人收不到 QQ 号，主人要绑定一次。先保存机器人并连上，再回这里绑定。</span>
    <template v-else-if="!modelValue">
      <span class="hint">QQ 官方机器人收不到 QQ 号，主人要绑定一次：给机器人发「绑定主人」（私聊或在群里 @ 它），把它回的验证码填在这里。</span>
      <form class="input-group" @submit.prevent="bind">
        <input
          v-model.trim="code"
          class="input mono"
          inputmode="numeric"
          maxlength="6"
          placeholder="6 位验证码"
          aria-label="主人绑定验证码"
        />
        <button class="btn" type="submit" :disabled="busy || code.length !== 6">
          <KeyRound :size="13" aria-hidden="true" />
          绑定
        </button>
      </form>
      <span v-if="error" class="hint owner-bind-error">{{ error }}</span>
    </template>
    <span v-else-if="justBound" class="hint">已绑定。主人以后私聊或在群里说话，机器人都能认出来。</span>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import { KeyRound } from "@lucide/vue";
import { bindOwnerByCode } from "../api";
import { toastSuccess } from "../lib/toast";

// QQ 官方机器人只给 openid，主人不知道自己的 openid：主人在聊天里发「绑定主人」，
// 机器人回一个只属于他的验证码，这里填进去，后端把对应的 openid 存成主人。
const props = defineProps<{
  modelValue?: string;
  /** 已保存的机器人 ID；新建还没保存时为空。 */
  profile?: string;
}>();
const emit = defineEmits<{ "update:modelValue": [id: string] }>();

const code = ref("");
const busy = ref(false);
const error = ref("");
const justBound = ref(false);

async function bind(): Promise<void> {
  const profile = props.profile ?? "";
  if (!profile || code.value.length !== 6) return;
  busy.value = true;
  error.value = "";
  try {
    const result = await bindOwnerByCode(profile, code.value);
    code.value = "";
    justBound.value = true;
    emit("update:modelValue", result.owner_id);
    toastSuccess("主人已绑定");
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    busy.value = false;
  }
}

watch(
  () => props.profile,
  () => {
    code.value = "";
    justBound.value = false;
    error.value = "";
  }
);
</script>

<style scoped>
.owner-bind {
  display: grid;
  gap: 6px;
  min-width: 0;
}

.owner-bind .input-group {
  max-width: 260px;
}

.owner-bind-error {
  color: var(--err);
}
</style>
