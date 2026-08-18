<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

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
        placeholder="管理员账号"
        autocomplete="username"
      />
      <div class="password-field">
        <input
          ref="passwordInput"
          v-model="password"
          class="input"
          :type="show ? 'text' : 'password'"
          placeholder="管理密码"
          autocomplete="current-password"
        />
        <button class="password-toggle" type="button" :aria-label="show ? '隐藏密码' : '显示密码'" @click="show = !show">
          <EyeOff v-if="show" :size="16" aria-hidden="true" />
          <Eye v-else :size="16" aria-hidden="true" />
        </button>
      </div>
      <button class="btn primary" type="submit" :disabled="busy || ownerBusy || claiming || password.length === 0">
        <LogIn :size="15" aria-hidden="true" />
        {{ busy ? "登录中…" : "登录" }}
      </button>
      <p v-if="error" class="login-error">{{ error }}</p>

      <template v-if="ownerAvailable">
        <hr class="divider" style="margin: 4px 0" />
        <p class="muted owner-login-title">管理员快速登录</p>

        <template v-if="pairingCode">
          <button class="owner-pair-code mono" type="button" title="复制验证码" @click="copyPairingCode">
            <span>{{ pairingCode }}</span>
            <Copy :size="15" aria-hidden="true" />
          </button>
          <p class="muted owner-pair-hint">将验证码私聊发送给机器人，确认后会自动登录</p>
          <p class="owner-pair-status">
            <LoaderCircle class="spin" :size="14" aria-hidden="true" />
            等待主人确认 · {{ pairingRemaining }}s
          </p>
        </template>
        <button v-else class="btn" type="button" :disabled="busy || ownerBusy || claiming" @click="startOwnerPairing">
          <MessageCircle :size="15" aria-hidden="true" />
          {{ ownerBusy ? "正在获取…" : "获取私聊验证码" }}
        </button>

        <!-- 兜底入口：轮询被网络掐断、页面被手机浏览器回收、或者换了个标签页
             打开时都用得上。验证码就在主人自己发出去的那条私聊里，抄回来即可。 -->
        <div class="input-group owner-code-input">
          <input
            v-model="claimCode"
            class="input mono"
            inputmode="numeric"
            maxlength="6"
            placeholder="6 位验证码"
            autocomplete="one-time-code"
            @input="normalizeClaimCode"
            @keydown.enter.prevent="claimPairing"
          />
          <button class="btn" type="button" :disabled="busy || claiming || claimCode.length !== 6" @click="claimPairing">
            <LogIn :size="14" aria-hidden="true" />
            {{ claiming ? "登录中…" : "登录" }}
          </button>
        </div>
        <p class="muted owner-pair-hint">没有自动跳转时，把已经私聊发出的验证码填在这里</p>
      </template>
    </form>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { BotMessageSquare, Copy, Eye, EyeOff, LoaderCircle, LogIn, MessageCircle } from "@lucide/vue";
import {
  claimOwnerLoginPairing,
  createOwnerLoginPairing,
  getOwnerLoginStatus,
  login,
  pollOwnerLoginPairing
} from "../api";
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
const claimCode = ref("");
const claiming = ref(false);
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

function normalizeClaimCode(): void {
  claimCode.value = claimCode.value.replace(/\D/g, "").slice(0, 6);
}

async function claimPairing(): Promise<void> {
  if (claimCode.value.length !== 6) return;
  claiming.value = true;
  try {
    await claimOwnerLoginPairing(claimCode.value);
    clearOwnerPairing();
    toastSuccess("主人私聊确认登录成功");
    emit("success");
  } catch (err) {
    claimCode.value = "";
    toastError(err instanceof Error ? err.message : "验证码登录失败");
  } finally {
    claiming.value = false;
  }
}

async function pollPairing(): Promise<void> {
  if (!pairingToken.value) return;
  try {
    const status = await pollOwnerLoginPairing(pairingToken.value);
    if (status.approved) {
      clearOwnerPairing();
      toastSuccess("主人私聊确认登录成功");
      emit("success");
      return;
    }
    if (status.expired) {
      clearOwnerPairing();
      toastError("验证码已失效，请重新获取");
      return;
    }
    if (typeof status.expires_in_seconds === "number") {
      pairingRemaining.value = status.expires_in_seconds;
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
    const status = await getOwnerLoginStatus();
    ownerAvailable.value = status.available;
  } catch {
    ownerAvailable.value = false;
  }
});

onBeforeUnmount(() => {
  clearOwnerPairing();
});
</script>
