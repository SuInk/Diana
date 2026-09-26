<script setup lang="ts">
/**
 * 微信（iLink）扫码登录。
 *
 * 凭据不能手填，只能扫码拿：后端生成二维码，页面循环调轮询接口（每次最长约
 * 25 秒的长轮询）直到确认。页面关掉轮询就停，后端不会替没人看的二维码续期。
 */
import { onBeforeUnmount, ref } from "vue";
import { LogOut, QrCode, RefreshCw } from "@lucide/vue";
import { logoutWeixin, pollWeixinLogin, startWeixinLogin, type BotProfileConfig, type WeixinLoginStatus } from "../api";
import { askConfirm } from "../confirm";
import { toastError, toastSuccess } from "../toast";

const props = defineProps<{
  /** 已保存的机器人 ID；新建还没保存时为空，扫码要挂在一台存好的机器人上。 */
  profileId?: string;
  botId?: string;
  userId?: string;
  loggedIn?: boolean;
}>();

const emit = defineEmits<{ (event: "updated", config: BotProfileConfig): void }>();

const status = ref<WeixinLoginStatus | null>(null);
const qrImage = ref("");
const qrURL = ref("");
const verifyCode = ref("");
const busy = ref(false);
let sessionID = "";
let controller: AbortController | null = null;
let pendingCode = "";

const statusText: Record<string, string> = {
  wait: "请用手机微信扫描二维码",
  scaned: "已扫码，请在手机上确认",
  need_verifycode: "请输入手机微信上显示的数字",
  confirmed: "登录成功",
  binded_redirect: "这个微信号已经连接过本机器人",
  expired: "二维码已失效，请重新生成",
  failed: "登录失败"
};

function stop(): void {
  controller?.abort();
  controller = null;
  sessionID = "";
}

async function start(): Promise<void> {
  if (!props.profileId || busy.value) return;
  stop();
  busy.value = true;
  try {
    const first = await startWeixinLogin(props.profileId);
    sessionID = first.session_id;
    apply(first);
    void loop(sessionID);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "获取二维码失败");
  } finally {
    busy.value = false;
  }
}

function apply(next: WeixinLoginStatus): void {
  status.value = next;
  if (next.qrcode_image) {
    qrImage.value = next.qrcode_image;
    qrURL.value = next.qrcode_url || "";
  }
}

async function loop(current: string): Promise<void> {
  controller = new AbortController();
  const signal = controller.signal;
  while (sessionID === current && !signal.aborted) {
    try {
      const code = pendingCode;
      pendingCode = "";
      const next = await pollWeixinLogin(props.profileId || "", current, code, signal);
      if (sessionID !== current) return;
      apply(next);
      if (next.status === "confirmed" || next.status === "binded_redirect") {
        qrImage.value = "";
        sessionID = "";
        if (next.config) emit("updated", next.config);
        toastSuccess(next.message || statusText[next.status]);
        return;
      }
      if (next.status === "expired" || next.status === "failed") {
        qrImage.value = "";
        sessionID = "";
        return;
      }
      if (next.status === "need_verifycode" && !pendingCode) {
        // 等用户把数字填进来再发下一轮，不然会带着空码把手机那边的输入冲掉。
        await waitForCode(current, signal);
      }
    } catch (error) {
      if (signal.aborted || sessionID !== current) return;
      status.value = { session_id: current, status: "failed", message: error instanceof Error ? error.message : "轮询失败" };
      qrImage.value = "";
      sessionID = "";
      return;
    }
  }
}

let resolveCode: (() => void) | null = null;

function waitForCode(current: string, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    resolveCode = resolve;
    signal.addEventListener("abort", () => resolve(), { once: true });
    if (sessionID !== current) resolve();
  });
}

function submitCode(): void {
  const code = verifyCode.value.trim();
  if (!code) return;
  pendingCode = code;
  verifyCode.value = "";
  resolveCode?.();
  resolveCode = null;
}

async function logout(): Promise<void> {
  if (!props.profileId) return;
  const ok = await askConfirm({
    title: "解绑微信？",
    message: "解绑后这台机器人停止收发微信消息，需要重新扫码才能恢复。",
    confirmLabel: "解绑",
    danger: true
  });
  if (!ok) return;
  busy.value = true;
  try {
    emit("updated", await logoutWeixin(props.profileId));
    toastSuccess("已解绑微信");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "解绑失败");
  } finally {
    busy.value = false;
  }
}

onBeforeUnmount(stop);
</script>

<template>
  <div class="field wide weixin-login">
    <label>微信登录</label>
    <p v-if="!profileId" class="hint">先把这台机器人按微信平台保存，再扫码绑定。</p>
    <template v-else>
      <p v-if="loggedIn" class="hint">
        已绑定 <span class="mono">{{ botId }}</span><template v-if="userId">，扫码人 <span class="mono">{{ userId }}</span></template>。
        登录失效时这里重新扫码即可，游标和会话会接着用。
      </p>
      <p v-else class="hint">尚未登录。用要当作机器人的那个微信号扫码，确认后自动保存并开始收消息。</p>
      <div class="actions">
        <button class="btn small" type="button" :disabled="busy" @click="start">
          <RefreshCw v-if="qrImage" :size="14" aria-hidden="true" />
          <QrCode v-else :size="14" aria-hidden="true" />
          {{ qrImage ? "换一张二维码" : loggedIn ? "重新扫码登录" : "扫码登录" }}
        </button>
        <button v-if="loggedIn" class="btn small" type="button" :disabled="busy" @click="logout">
          <LogOut :size="14" aria-hidden="true" />
          解绑
        </button>
      </div>
      <div v-if="qrImage" class="qr">
        <img :src="qrImage" alt="微信登录二维码" width="200" height="200" />
        <a v-if="qrURL" class="hint" :href="qrURL" target="_blank" rel="noopener noreferrer">扫不了？在手机上打开这个链接</a>
      </div>
      <p v-if="status" class="hint" role="status" :class="{ 'warn-text': status.status === 'failed' || status.status === 'expired' }">
        {{ status.message || statusText[status.status] }}
      </p>
      <form v-if="status?.status === 'need_verifycode'" class="input-group" @submit.prevent="submitCode">
        <input v-model="verifyCode" class="input mono" inputmode="numeric" autocomplete="one-time-code" placeholder="手机上显示的数字" aria-label="配对数字" />
        <button class="btn small" type="submit">提交</button>
      </form>
      <span class="hint">走腾讯 iLink Bot 接口，只支持私聊：文字、图片收发，语音取服务端转写文字，文件和视频只显示占位。</span>
    </template>
  </div>
</template>

<style scoped>
.weixin-login .actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.weixin-login .qr {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 6px;
}

.weixin-login .qr img {
  width: 200px;
  height: 200px;
  max-width: 100%;
  padding: 8px;
  border-radius: 8px;
  /* 二维码要白底才扫得出，深色主题下也保持白底。 */
  background: #fff;
  image-rendering: pixelated;
}
</style>
