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
      <button class="btn primary" type="submit" :disabled="busy || ownerBusy || challengeAction !== null || password.length === 0">
        <LogIn :size="15" aria-hidden="true" />
        {{ busy ? "登录中…" : "登录" }}
      </button>
      <p v-if="error" class="login-error">{{ error }}</p>

      <template v-if="ownerAvailable">
        <hr class="divider" style="margin: 4px 0" />
        <p class="muted owner-login-title">管理员快速登录</p>
        <div v-if="pairAvailable && codeAvailable" class="segmented owner-login-methods" role="tablist" aria-label="管理员登录方式">
          <button type="button" :class="{ active: ownerMethod === 'pair' }" role="tab" :aria-selected="ownerMethod === 'pair'" @click="selectOwnerMethod('pair')">
            <MessageCircle :size="14" aria-hidden="true" />
            私聊确认
          </button>
          <button type="button" :class="{ active: ownerMethod === 'code' }" role="tab" :aria-selected="ownerMethod === 'code'" @click="selectOwnerMethod('code')">
            <ShieldCheck :size="14" aria-hidden="true" />
            验证码登录
          </button>
        </div>

        <template v-if="ownerMethod === 'code'">
          <div class="input-group owner-code-input">
            <input
              ref="ownerCodeInput"
              v-model="ownerCode"
              class="input mono"
              inputmode="numeric"
              maxlength="6"
              placeholder="6 位验证码"
              autocomplete="one-time-code"
              @input="normalizeOwnerCode"
              @keydown.enter.prevent="verifyOwnerCode"
            />
            <button class="btn" type="button" :disabled="busy || challengeAction !== null || challengeCooldown > 0" @click="sendOwnerCode">
              <Send :size="14" aria-hidden="true" />
              {{ challengeAction === "send" ? "发送中…" : challengeCooldown > 0 ? `${challengeCooldown}s 后重发` : challengeToken ? "重新发送" : "发送验证码" }}
            </button>
          </div>
          <button class="btn" type="button" :disabled="busy || challengeAction !== null || !challengeToken || ownerCode.length !== 6" @click="verifyOwnerCode">
            <LogIn :size="15" aria-hidden="true" />
            {{ challengeAction === "verify" ? "验证中…" : "验证码登录" }}
          </button>
          <p v-if="challengeToken" class="owner-pair-status">
            <ShieldCheck :size="14" aria-hidden="true" />
            验证码已发送 · {{ challengeRemaining }}s
          </p>
        </template>

        <template v-else>
          <template v-if="pairingCode">
            <button class="owner-pair-code mono" type="button" title="复制验证码" @click="copyPairingCode">
              <span>{{ pairingCode }}</span>
              <Copy :size="15" aria-hidden="true" />
            </button>
            <p class="muted owner-pair-hint">将验证码私聊发送给机器人，保持此页面开启</p>
            <p class="owner-pair-status">
              <LoaderCircle class="spin" :size="14" aria-hidden="true" />
              等待主人确认 · {{ pairingRemaining }}s
            </p>
          </template>
          <button v-else class="btn" type="button" :disabled="busy || ownerBusy || challengeAction !== null" @click="startOwnerPairing">
            <MessageCircle :size="15" aria-hidden="true" />
            {{ ownerBusy ? "正在获取…" : "获取私聊验证码" }}
          </button>
        </template>
      </template>
    </form>
  </div>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from "vue";
import { BotMessageSquare, Copy, Eye, EyeOff, LoaderCircle, LogIn, MessageCircle, Send, ShieldCheck } from "@lucide/vue";
import {
  createOwnerLoginPairing,
  getOwnerLoginStatus,
  login,
  pollOwnerLoginPairing,
  requestOwnerLoginCode,
  verifyOwnerLoginCode
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
const pairAvailable = ref(false);
const codeAvailable = ref(false);
const ownerMethod = ref<"code" | "pair">("pair");
const ownerCodeInput = ref<HTMLInputElement | null>(null);
const ownerCode = ref("");
const challengeToken = ref("");
const challengeAction = ref<"send" | "verify" | null>(null);
const challengeCooldown = ref(0);
const challengeRemaining = ref(0);
const ownerBusy = ref(false);
const pairingCode = ref("");
const pairingToken = ref("");
const pairingRemaining = ref(0);
let pairingPollTimer: ReturnType<typeof setInterval> | null = null;
let pairingCountdownTimer: ReturnType<typeof setInterval> | null = null;
let challengeCooldownTimer: ReturnType<typeof setInterval> | null = null;
let challengeExpiryTimer: ReturnType<typeof setInterval> | null = null;

// 验证码下发是给「只有一台设备」的场景用的：点发送、切到聊天软件看码、再切
// 回来填。切走的这段时间手机浏览器很可能把页面丢掉，token 只存在内存里的话
// 回来就作废了，而重新发送要等 60 秒冷却、拿到的还是另一个码——之前收到的那
// 条就白发了。存一份到 sessionStorage，按绝对时间恢复倒计时。
const OWNER_CHALLENGE_KEY = "diana:owner-login-challenge";

interface StoredChallenge {
  token: string;
  expiresAt: number;
  cooldownUntil: number;
}

function saveChallenge(token: string, expiresInSeconds: number, cooldownSeconds: number): void {
  const now = Date.now();
  const payload: StoredChallenge = {
    token,
    expiresAt: now + expiresInSeconds * 1000,
    cooldownUntil: now + cooldownSeconds * 1000
  };
  try {
    window.sessionStorage.setItem(OWNER_CHALLENGE_KEY, JSON.stringify(payload));
  } catch {
    /* 无痕模式等场景写不了，降级成仅内存保存 */
  }
}

function clearStoredChallenge(): void {
  try {
    window.sessionStorage.removeItem(OWNER_CHALLENGE_KEY);
  } catch {
    /* 同上 */
  }
}

function restoreChallenge(): boolean {
  let raw: string | null = null;
  try {
    raw = window.sessionStorage.getItem(OWNER_CHALLENGE_KEY);
  } catch {
    return false;
  }
  if (!raw) return false;
  try {
    const stored = JSON.parse(raw) as StoredChallenge;
    const remaining = Math.floor((stored.expiresAt - Date.now()) / 1000);
    if (!stored.token || remaining <= 0) {
      clearStoredChallenge();
      return false;
    }
    challengeToken.value = stored.token;
    startChallengeTimers(Math.max(0, Math.ceil((stored.cooldownUntil - Date.now()) / 1000)), remaining);
    return true;
  } catch {
    clearStoredChallenge();
    return false;
  }
}

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

function clearChallengeTimers(): void {
  if (challengeCooldownTimer) clearInterval(challengeCooldownTimer);
  if (challengeExpiryTimer) clearInterval(challengeExpiryTimer);
  challengeCooldownTimer = null;
  challengeExpiryTimer = null;
}

function clearOwnerChallenge(): void {
  clearChallengeTimers();
  clearStoredChallenge();
  ownerCode.value = "";
  challengeToken.value = "";
  challengeCooldown.value = 0;
  challengeRemaining.value = 0;
}

function startChallengeTimers(cooldownSeconds: number, expiresInSeconds: number): void {
  clearChallengeTimers();
  challengeCooldown.value = cooldownSeconds;
  challengeRemaining.value = expiresInSeconds;
  challengeCooldownTimer = setInterval(() => {
    challengeCooldown.value = Math.max(0, challengeCooldown.value - 1);
    if (challengeCooldown.value === 0 && challengeCooldownTimer) {
      clearInterval(challengeCooldownTimer);
      challengeCooldownTimer = null;
    }
  }, 1000);
  challengeExpiryTimer = setInterval(() => {
    challengeRemaining.value = Math.max(0, challengeRemaining.value - 1);
    if (challengeRemaining.value === 0) {
      clearOwnerChallenge();
    }
  }, 1000);
}

function normalizeOwnerCode(): void {
  ownerCode.value = ownerCode.value.replace(/\D/g, "").slice(0, 6);
}

function selectOwnerMethod(method: "code" | "pair"): void {
  // 切到私聊确认时不清验证码：那个码已经发出去了，冷却也已经烧掉，丢了就得
  // 再等一轮。私聊配对反过来随时可以重开，切走时收掉轮询更省事。
  if (method === "code") {
    clearOwnerPairing();
  }
  ownerMethod.value = method;
}

async function sendOwnerCode(): Promise<void> {
  challengeAction.value = "send";
  try {
    const challenge = await requestOwnerLoginCode();
    ownerCode.value = "";
    challengeToken.value = challenge.challenge_token;
    startChallengeTimers(challenge.cooldown_seconds, challenge.expires_in_seconds);
    saveChallenge(challenge.challenge_token, challenge.expires_in_seconds, challenge.cooldown_seconds);
    await nextTick();
    ownerCodeInput.value?.focus();
    toastSuccess("验证码已发送到管理员账号");
  } catch (err) {
    toastError(err instanceof Error ? err.message : "验证码发送失败");
  } finally {
    challengeAction.value = null;
  }
}

async function verifyOwnerCode(): Promise<void> {
  if (!challengeToken.value || ownerCode.value.length !== 6) return;
  challengeAction.value = "verify";
  try {
    await verifyOwnerLoginCode(challengeToken.value, ownerCode.value);
    clearOwnerChallenge();
    clearOwnerPairing();
    toastSuccess("管理员验证码登录成功");
    emit("success");
  } catch (err) {
    ownerCode.value = "";
    await nextTick();
    ownerCodeInput.value?.focus();
    toastError(err instanceof Error ? err.message : "验证码登录失败");
  } finally {
    challengeAction.value = null;
  }
}

async function pollPairing(): Promise<void> {
  if (!pairingToken.value) return;
  try {
    const status = await pollOwnerLoginPairing(pairingToken.value);
    if (status.approved) {
      clearOwnerPairing();
      toastSuccess("主人私聊确认成功");
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
    pairAvailable.value = status.pair_available;
    codeAvailable.value = status.code_available;
    ownerMethod.value = status.pair_available ? "pair" : "code";
    if (status.code_available && restoreChallenge()) {
      // 有还没用掉的验证码，直接停在这一栏，省得用户以为要重新发一次。
      ownerMethod.value = "code";
    }
  } catch {
    ownerAvailable.value = false;
    pairAvailable.value = false;
    codeAvailable.value = false;
  }
});

onBeforeUnmount(() => {
  clearOwnerPairing();
  clearOwnerChallenge();
});
</script>
