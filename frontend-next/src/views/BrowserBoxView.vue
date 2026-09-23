<!-- Copyright (c) 2025-now SuInk. Licensed under the Limited Redistribution License. -->
<!--
  浏览器这一页是各档浏览器的唯一入口。以前它们各在一处——内置浏览器在这里、浏览器
  控制扩展在「设置」里、一次性无头渲染在插件页——而用户要回答的第一个问题恰恰是
  「我该用哪一档」，三处分开就没人能回答。这里按「谁的浏览器、带不带登录态」并排
  列出来，配置跟着各自那一档走。
-->
<template>
  <section class="stack">
    <div class="card">
      <div class="card-header">
        <h2>浏览器</h2>
        <span class="card-sub">几档浏览器，区别在于用谁的浏览器、带不带你的登录态</span>
      </div>
      <div class="card-body">
        <div class="browser-tiers">
          <button
            v-for="item in tiers"
            :key="item.key"
            class="browser-tier"
            :class="{ active: tab === item.key }"
            type="button"
            @click="tab = item.key"
          >
            <span class="browser-tier-name">{{ item.label }}</span>
            <span class="browser-tier-hint">{{ item.hint }}</span>
            <span class="browser-tier-who">{{ item.who }}</span>
          </button>
        </div>
      </div>
    </div>

    <template v-if="tab === 'render'">
      <div class="card">
        <div class="card-header">
          <h2>一次性无头渲染</h2>
          <span class="badge" :class="browserDependency?.available ? 'ok' : 'warn'">
            {{ browserDependency?.available ? "可用" : "缺浏览器" }}
          </span>
          <span class="card-sub">每次开一个全新 profile，用完即删，不带任何登录态</span>
        </div>
        <div class="card-body stack">
          <p class="muted" style="margin: 0; font-size: 13px">
            Markdown / Mermaid / SVG 出图、PDF 渲染、网页读取与截图走的都是它，链接解析器抓 JS 渲染的页面也一样。
            因为不带登录态，它是几档里唯一对群成员开放的（工具名 <code class="mono">browser_render</code>）。
            渲染尺寸、窗口模式这些参数在插件页的「网页渲染」里。
          </p>
          <PluginDependencyList
            :dependencies="browserDependencies"
            :loading="dependenciesLoading"
            :busy="busyDependency"
            @install="installDependency"
          />
          <p class="muted" style="margin: 0; font-size: 12.5px">
            容器里 WebUI 的一键安装会因为进程不是 root 而失败，报错里会附上在宿主机执行的那条命令。
          </p>
        </div>
      </div>
    </template>

    <template v-if="tab === 'box'">
    <div class="card">
      <div class="card-header">
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
      <div class="card-header">
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
    </template>

    <BrowserControlPanel v-if="tab === 'control'" />
    <!-- 外接 CDP 原来是扩展页的一个标签，和「装了什么」那几项并列却说的是浏览器，
         挪到这里跟另外三档放在一起。 -->
    <AgentBrowserPanel v-if="tab === 'cdp'" />
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from "vue";
import AgentBrowserPanel from "../components/AgentBrowserPanel.vue";
import BrowserControlPanel from "../components/BrowserControlPanel.vue";
import PluginDependencyList from "../components/PluginDependencyList.vue";
import {
  installResolverDependency,
  listPluginDependencies,
  type ResolverDependency,
  browserBoxLiveURL,
  getBrowserBoxStatus,
  saveBrowserBoxSettings,
  setBrowserBoxTakeover,
  startBrowserBox,
  stopBrowserBox,
  type BrowserBoxSettings,
  type BrowserBoxStatus
} from "../api";
import { toastError, toastSuccess } from "../toast";

interface LiveFrame {
  data: string;
  width: number;
  height: number;
  scale: number;
}

// 三档并排：名字之外还要说清「用谁的浏览器、谁能驱动」，这正是用户在这一页要
// 回答的问题。
const tiers = [
  { key: "render" as const, label: "一次性无头渲染", hint: "全新 profile，用完即删", who: "群成员也能用" },
  { key: "box" as const, label: "内置浏览器", hint: "Diana 自己的常驻浏览器", who: "只有主人" },
  { key: "control" as const, label: "浏览器控制扩展", hint: "你自己日常用的浏览器", who: "只有主人" },
  { key: "cdp" as const, label: "外接浏览器（CDP）", hint: "你另起的专用 Chrome", who: "按机器人配置" }
];
const tab = ref<(typeof tiers)[number]["key"]>("box");

// 浏览器依赖探测复用插件页那套接口：装不装得上、装在哪，答案只该有一处。
const sandboxedBrowserPluginID = "official.sandboxed-browser-renderer";
const dependencyGroups = ref<Record<string, ResolverDependency[]>>({});
const dependenciesLoading = ref(true);
const busyDependency = ref("");
const browserDependencies = computed(() => dependencyGroups.value[sandboxedBrowserPluginID] ?? []);
const browserDependency = computed(() => browserDependencies.value.find((item) => item.name === "browser") ?? browserDependencies.value[0]);

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

async function loadDependencies(refresh = false): Promise<void> {
  dependenciesLoading.value = true;
  try {
    const response = await listPluginDependencies(refresh);
    dependencyGroups.value = response.plugins;
  } catch {
    // 依赖探测只是辅助信息，失败不该打断这一页。
    dependencyGroups.value = {};
  } finally {
    dependenciesLoading.value = false;
  }
}

async function installDependency(dependency: ResolverDependency): Promise<void> {
  busyDependency.value = dependency.name;
  try {
    const result = await installResolverDependency(dependency.name);
    dependencyGroups.value = { ...dependencyGroups.value, ...result.plugins };
    toastSuccess(`已安装 ${dependency.name}`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : `安装 ${dependency.name} 失败`);
    await loadDependencies(true);
  } finally {
    busyDependency.value = "";
  }
}

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
  void loadDependencies();
  statusTimer = window.setInterval(() => void refresh(), 5000);
});

onBeforeUnmount(() => {
  if (statusTimer) window.clearInterval(statusTimer);
  disconnectLive();
});
</script>

<style scoped>
/* 四档一排：auto-fit 在常见宽度下会排成 3+1，剩下那张孤零零挂在第二行。 */
.browser-tiers {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 10px;
}

@media (max-width: 960px) {
  .browser-tiers {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 480px) {
  .browser-tiers {
    grid-template-columns: 1fr;
  }
}

.browser-tier {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 12px 14px;
  text-align: left;
  border: 1px solid var(--border, rgba(127, 127, 127, 0.3));
  border-radius: 10px;
  background: transparent;
  cursor: pointer;
}

.browser-tier.active {
  border-color: var(--accent, #7a5cff);
  background: color-mix(in srgb, var(--accent, #7a5cff) 10%, transparent);
}

.browser-tier-name {
  font-weight: 600;
  font-size: 13.5px;
}

.browser-tier-hint {
  font-size: 12px;
  color: var(--muted);
}

/* 谁能用是选档时最要紧的那句，单独成一枚小标签，不和说明混成同一种灰字。 */
.browser-tier-who {
  align-self: flex-start;
  margin-top: 6px;
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 11.5px;
  color: var(--text-secondary);
  background: var(--surface-2, rgba(127, 127, 127, 0.12));
}

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
