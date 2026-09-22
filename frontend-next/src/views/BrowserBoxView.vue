<!-- Copyright (c) 2025-now SuInk. Licensed under the Limited Redistribution License. -->
<template>
  <section class="stack">
    <div class="card">
      <div class="card-head">
        <h2>内置浏览器</h2>
        <span class="badge" :class="status.running ? 'ok' : 'warn'">
          {{ status.running ? (status.takeover ? "你在操作" : "运行中") : status.settings.enabled ? "未启动" : "未启用" }}
        </span>
        <span class="card-sub">Diana 自己的浏览器，登录态留在数据目录里，你随时可以直接上手</span>
      </div>
      <div class="card-body stack">
        <p v-if="!status.available" class="muted" style="margin: 0; font-size: 13px">
          这台机器上没找到 Chrome/Chromium。容器完整版镜像自带 chromium；slim 版可以在宿主机执行
          <code class="mono">docker exec -u root &lt;容器名&gt; sh -c 'apt-get update &amp;&amp; apt-get install -y chromium fonts-noto-cjk'</code>。
        </p>
        <div class="field">
          <label class="switch-row">
            <input v-model="settings.enabled" type="checkbox" :disabled="saving" @change="saveSettings" />
            <span>启用内置浏览器（关掉会结束进程，登录态仍保留在 profile 目录）</span>
          </label>
          <label class="switch-row">
            <input v-model="settings.headful" type="checkbox" :disabled="saving || !settings.enabled" @change="saveSettings" />
            <span>开一个真窗口（只有本机有显示器时才有意义；容器里保持关闭，实时画面照常）</span>
          </label>
        </div>

        <div class="row gap">
          <button class="btn small" type="button" :disabled="busy || !settings.enabled" @click="start">启动</button>
          <button class="btn small ghost" type="button" :disabled="busy || !status.running" @click="stop">停止</button>
          <button
            class="btn small"
            :class="status.takeover ? 'warn' : 'ghost'"
            type="button"
            :disabled="busy || !status.running"
            @click="toggleTakeover"
          >
            {{ status.takeover ? "交还给机器人" : "我来操作" }}
          </button>
          <span class="muted" style="font-size: 12.5px">
            你在画面上点一下就自动接管；交还之前机器人不会碰这个浏览器。
          </span>
        </div>

        <p v-if="status.last_error" class="muted" style="margin: 0; font-size: 12.5px">
          最近一次错误：{{ status.last_error }}
        </p>
        <p v-if="status.profile_dir" class="muted" style="margin: 0; font-size: 12.5px">
          登录态目录：<code class="mono">{{ status.profile_dir }}</code>
        </p>
      </div>
    </div>

    <div v-if="status.running" class="card">
      <div class="card-head">
        <h2>画面</h2>
        <span class="card-sub">{{ currentTitle || "空白页" }}</span>
      </div>
      <div class="card-body stack">
        <div class="row gap">
          <button class="btn small ghost" type="button" @click="send({ type: 'back' })">后退</button>
          <button class="btn small ghost" type="button" @click="send({ type: 'reload' })">刷新</button>
          <input
            v-model="addressInput"
            class="input"
            style="flex: 1; min-width: 220px"
            placeholder="https://example.com"
            @keydown.enter.prevent="navigate"
          />
          <button class="btn small" type="button" @click="navigate">打开</button>
        </div>

        <div class="browser-stage" @contextmenu.prevent>
          <img
            v-if="frame"
            ref="screen"
            class="browser-screen"
            :src="`data:image/jpeg;base64,${frame.data}`"
            alt="内置浏览器画面"
            tabindex="0"
            @mousedown.prevent="onMouse($event, 'mousePressed')"
            @mouseup.prevent="onMouse($event, 'mouseReleased')"
            @mousemove="onMouseMove"
            @wheel.prevent="onWheel"
            @keydown.prevent="onKey($event, 'keyDown')"
            @keyup.prevent="onKey($event, 'keyUp')"
          />
          <p v-else class="muted" style="margin: 0; font-size: 13px">正在连接画面……</p>
        </div>
        <p class="muted" style="margin: 0; font-size: 12.5px">
          点一下画面再打字，键盘事件才会送到页面。密码这类东西你自己输，机器人看不到你敲了什么——它只能看到页面最终长什么样。
        </p>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, reactive, ref } from "vue";
import {
  browserBoxLiveURL,
  getBrowserBoxStatus,
  saveBrowserBoxSettings,
  setBrowserBoxTakeover,
  startBrowserBox,
  stopBrowserBox,
  type BrowserBoxSettings,
  type BrowserBoxStatus
} from "../api";
import { toastError } from "../toast";

interface LiveFrame {
  data: string;
  width: number;
  height: number;
  scale: number;
}

const status = reactive<BrowserBoxStatus>({
  settings: { enabled: false },
  running: false,
  takeover: false,
  available: false
});
const settings = reactive<BrowserBoxSettings>({ enabled: false });
const frame = ref<LiveFrame | null>(null);
const screen = ref<HTMLImageElement | null>(null);
const addressInput = ref("");
const currentTitle = ref("");
const saving = ref(false);
const busy = ref(false);

let socket: WebSocket | null = null;
let statusTimer: number | undefined;

async function refresh(): Promise<void> {
  try {
    const next = await getBrowserBoxStatus();
    Object.assign(status, next);
    Object.assign(settings, next.settings);
    if (next.running && !socket) connectLive();
    if (!next.running && socket) disconnectLive();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "读取内置浏览器状态失败");
  }
}

async function saveSettings(): Promise<void> {
  saving.value = true;
  try {
    const result = await saveBrowserBoxSettings({ ...settings });
    Object.assign(status, result.status);
    Object.assign(settings, result.settings);
    if (status.running) connectLive();
    else disconnectLive();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "保存失败");
    await refresh();
  } finally {
    saving.value = false;
  }
}

async function start(): Promise<void> {
  busy.value = true;
  try {
    const result = await startBrowserBox();
    Object.assign(status, result.status);
    connectLive();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "启动失败");
  } finally {
    busy.value = false;
  }
}

async function stop(): Promise<void> {
  busy.value = true;
  try {
    const result = await stopBrowserBox();
    Object.assign(status, result.status);
    disconnectLive();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "停止失败");
  } finally {
    busy.value = false;
  }
}

async function toggleTakeover(): Promise<void> {
  busy.value = true;
  try {
    const result = await setBrowserBoxTakeover(!status.takeover);
    status.takeover = result.active;
  } catch (err) {
    toastError(err instanceof Error ? err.message : "切换失败");
  } finally {
    busy.value = false;
  }
}

function connectLive(): void {
  disconnectLive();
  const ws = new WebSocket(browserBoxLiveURL());
  socket = ws;
  ws.onmessage = (event) => {
    const message = JSON.parse(event.data) as { type: string; frame?: LiveFrame; tab?: { url?: string; title?: string } };
    if (message.type === "frame" && message.frame) {
      frame.value = message.frame;
    } else if (message.type === "ready" && message.tab) {
      addressInput.value = message.tab.url ?? "";
      currentTitle.value = message.tab.title ?? "";
    }
  };
  ws.onclose = () => {
    if (socket === ws) socket = null;
  };
}

function disconnectLive(): void {
  if (!socket) return;
  socket.onclose = null;
  socket.close();
  socket = null;
  frame.value = null;
}

function send(payload: Record<string, unknown>): void {
  if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify(payload));
}

/** 把画面上的坐标换算成页面坐标：画面被 CSS 缩放过，点的位置得按比例还原。 */
function pagePoint(event: MouseEvent): { x: number; y: number } {
  const element = screen.value;
  if (!element || !frame.value) return { x: 0, y: 0 };
  const rect = element.getBoundingClientRect();
  const scaleX = frame.value.width / rect.width;
  const scaleY = frame.value.height / rect.height;
  return { x: (event.clientX - rect.left) * scaleX, y: (event.clientY - rect.top) * scaleY };
}

function modifiers(event: MouseEvent | KeyboardEvent): number {
  return (event.altKey ? 1 : 0) | (event.ctrlKey ? 2 : 0) | (event.metaKey ? 4 : 0) | (event.shiftKey ? 8 : 0);
}

const mouseButtons = ["left", "middle", "right"];

function onMouse(event: MouseEvent, type: "mousePressed" | "mouseReleased"): void {
  const point = pagePoint(event);
  screen.value?.focus();
  send({
    type: "mouse",
    mouse: {
      type,
      x: point.x,
      y: point.y,
      button: mouseButtons[event.button] ?? "left",
      buttons: event.buttons,
      click_count: 1,
      modifiers: modifiers(event)
    }
  });
  status.takeover = true;
}

let lastMove = 0;
function onMouseMove(event: MouseEvent): void {
  // 移动事件按 20ms 节流：不节流的话一次拖动能发出几百条，画面反而更卡。
  const now = Date.now();
  if (now - lastMove < 20) return;
  lastMove = now;
  if (!frame.value) return;
  const point = pagePoint(event);
  send({ type: "mouse", mouse: { type: "mouseMoved", x: point.x, y: point.y, buttons: event.buttons, modifiers: modifiers(event) } });
}

function onWheel(event: WheelEvent): void {
  const point = pagePoint(event);
  send({
    type: "mouse",
    mouse: { type: "mouseWheel", x: point.x, y: point.y, delta_x: -event.deltaX, delta_y: -event.deltaY, modifiers: modifiers(event) }
  });
  status.takeover = true;
}

function onKey(event: KeyboardEvent, type: "keyDown" | "keyUp"): void {
  // 可打印字符走 insertText：中文输入法上屏的是整段文字，不是一串按键。
  if (type === "keyDown" && event.key.length === 1 && !event.ctrlKey && !event.metaKey) {
    send({ type: "text", text: event.key });
    status.takeover = true;
    return;
  }
  send({
    type: "key",
    key: {
      type,
      key: event.key,
      code: event.code,
      windows_virtual_key_code: event.keyCode,
      modifiers: modifiers(event)
    }
  });
  status.takeover = true;
}

function navigate(): void {
  const target = addressInput.value.trim();
  if (!target) return;
  send({ type: "navigate", url: /^https?:\/\//i.test(target) ? target : `https://${target}` });
  status.takeover = true;
}

onMounted(() => {
  void refresh();
  statusTimer = window.setInterval(() => void refresh(), 5000);
});

onBeforeUnmount(() => {
  if (statusTimer) window.clearInterval(statusTimer);
  disconnectLive();
});
</script>

<style scoped>
.browser-stage {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 240px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--surface-2, rgba(0, 0, 0, 0.15));
  overflow: hidden;
}

.browser-screen {
  width: 100%;
  max-width: 100%;
  display: block;
  cursor: default;
  outline: none;
}

.row.gap {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}
</style>
