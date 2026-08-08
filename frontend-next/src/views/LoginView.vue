<template>
  <div class="login-screen">
    <form class="login-card" @submit.prevent="submit">
      <span class="brand-mark login-mark">
        <BotMessageSquare :size="22" aria-hidden="true" />
      </span>
      <h1>Diana</h1>
      <p class="muted">请输入管理员账号和密码</p>
      <input
        v-model="username"
        class="input"
        placeholder="diana#账号"
        autocomplete="username"
      />
      <div class="input-group">
        <input
          ref="passwordInput"
          v-model="password"
          class="input"
          :type="show ? 'text' : 'password'"
          placeholder="管理密码"
          autocomplete="current-password"
        />
        <button class="btn icon-only" type="button" :aria-label="show ? '隐藏密码' : '显示密码'" @click="show = !show">
          <EyeOff v-if="show" :size="14" aria-hidden="true" />
          <Eye v-else :size="14" aria-hidden="true" />
        </button>
      </div>
      <button class="btn primary" type="submit" :disabled="busy || password.length === 0">
        <LogIn :size="15" aria-hidden="true" />
        {{ busy ? "登录中…" : "登录" }}
      </button>
      <p v-if="error" class="login-error">{{ error }}</p>

      <template v-if="ownerAvailable">
        <hr class="divider" style="margin: 4px 0" />
        <p class="muted owner-login-title">QQ 私聊配对登录</p>
        <template v-if="pairingCode">
          <button class="owner-pair-code mono" type="button" title="复制验证码" @click="copyPairingCode">
            <span>{{ pairingCode }}</span>
            <Copy :size="15" aria-hidden="true" />
          </button>
          <p class="muted owner-pair-hint">
            将验证码私聊发送给机器人，保持此页面开启
          </p>
          <p class="owner-pair-status">
            <LoaderCircle class="spin" :size="14" aria-hidden="true" />
            等待主人确认 · {{ pairingRemaining }}s
          </p>
        </template>
        <button v-else class="btn" type="button" :disabled="ownerBusy" @click="startOwnerPairing">
          <MessageCircle :size="15" aria-hidden="true" />
          {{ ownerBusy ? "正在获取…" : "获取验证码" }}
        </button>
      </template>
    </form>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { BotMessageSquare, Copy, Eye, EyeOff, LoaderCircle, LogIn, MessageCircle } from "@lucide/vue";
import { createOwnerLoginPairing, getOwnerLoginStatus, login, pollOwnerLoginPairing } from "../api";
import { toastError, toastSuccess } from "../toast";

const emit = defineEmits<{ success: [] }>();

const username = ref("");
const password = ref("");
const show = ref(false);
const busy = ref(false);
const error = ref("");
const passwordInput = ref<HTMLInputElement | null>(null);
const ownerAvailable = ref(false);
const ownerBusy = ref(false);
const pairingCode = ref("");
const pairingToken = ref("");
const pairingRemaining = ref(0);
let pairingPollTimer: ReturnType<typeof setInterval> | null = null;
let pairingCountdownTimer: ReturnType<typeof setInterval> | null = null;

async function submit(): Promise<void> {
  busy.value = true;
  error.value = "";
  try {
    await login(username.value, password.value);
    password.value = "";
    emit("success");
  } catch (err) {
    error.value = err instanceof Error ? err.message : "登录失败";
  } finally {
    busy.value = false;
  }
}

function clearOwnerPairing(): void {
  if (pairingPollTimer) clearInterval(pairingPollTimer);
  if (pairingCountdownTimer) clearInterval(pairingCountdownTimer);
  pairingPollTimer = null;
  pairingCountdownTimer = null;
  pairingCode.value = "";
  pairingToken.value = "";
  pairingRemaining.value = 0;
}

async function pollPairing(): Promise<void> {
  if (!pairingToken.value) return;
  try {
    const status = await pollOwnerLoginPairing(pairingToken.value);
    if (status.approved) {
      clearOwnerPairing();
      toastSuccess("QQ 私聊确认成功");
      emit("success");
    } else if (status.expired) {
      clearOwnerPairing();
      toastError("验证码已失效，请重新获取");
    }
  } catch (err) {
    clearOwnerPairing();
    toastError(err instanceof Error ? err.message : "检查登录状态失败");
  }
}

async function startOwnerPairing(): Promise<void> {
  ownerBusy.value = true;
  try {
    const pairing = await createOwnerLoginPairing();
    pairingCode.value = pairing.code;
    pairingToken.value = pairing.poll_token;
    pairingRemaining.value = pairing.expires_in_seconds;
    pairingPollTimer = setInterval(() => void pollPairing(), 1500);
    pairingCountdownTimer = setInterval(() => {
      pairingRemaining.value = Math.max(0, pairingRemaining.value - 1);
      if (pairingRemaining.value === 0) {
        clearOwnerPairing();
        toastError("验证码已失效，请重新获取");
      }
    }, 1000);
  } catch (err) {
    toastError(err instanceof Error ? err.message : "获取验证码失败");
  } finally {
    ownerBusy.value = false;
  }
}

async function copyPairingCode(): Promise<void> {
  try {
    await navigator.clipboard.writeText(pairingCode.value);
    toastSuccess("验证码已复制");
  } catch {
    toastError("复制失败，请手动记下验证码");
  }
}

onMounted(async () => {
  passwordInput.value?.focus();
  try {
    ownerAvailable.value = (await getOwnerLoginStatus()).available;
  } catch {
    ownerAvailable.value = false;
  }
});

onBeforeUnmount(() => {
  clearOwnerPairing();
});
</script>
