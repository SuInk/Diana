<!-- Copyright (c) 2025-now SuInk. Licensed under the Limited Redistribution License. -->
<!--
  浏览器这一页只回答一个问题：机器人要不要用浏览器，用谁的。

  Diana 内置和用户自己的 Chrome（扩展）做的是同一件事——带登录态、只有主人能驱动、
  能点能输入——区别只在用谁的，所以二选一，后端保证同一时间只有一个生效（见
  model/browsersource）。一次性无头渲染不在这里：它不带登录态，读公开网页、出图都
  靠它，一直可用，依赖和参数在插件页的「网页渲染」里。以前三者并排成「三档」，
  用户得先弄懂三者区别才能开始用。
-->
<template>
  <section class="stack">
    <div class="card">
      <div class="card-header">
        <h2>浏览器</h2>
        <span class="card-sub">机器人要不要用浏览器、用谁的。选中的那个带着登录态，只有主人能让机器人驱动它</span>
      </div>
      <div class="card-body stack">
        <div class="browser-sources" role="radiogroup" aria-label="机器人用哪个浏览器">
          <label v-for="item in sources" :key="item.key" class="browser-source">
            <input
              type="radio"
              name="browser-source"
              :value="item.key"
              :checked="source === item.key"
              :disabled="switching || source === null"
              @change="chooseSource(item.key)"
            />
            <span class="browser-source-name">{{ item.label }}</span>
            <span class="browser-source-hint">{{ item.hint }}</span>
          </label>
        </div>
        <p class="muted" style="margin: 0; font-size: 12.5px">
          读公开网页、出图、渲染 PDF 用的是另一个一次性无头浏览器，不带登录态、群成员也能用，一直开着，不用在这里选；
          它缺什么依赖在「扩展」页的「网页渲染」里看。
        </p>
      </div>
    </div>

    <div v-if="source === 'box' && !botID" class="card">
      <div class="card-header">
        <h2>Diana 内置浏览器</h2>
        <span class="card-sub">每台机器人各用一个浏览器、各有一份登录态，互相看不到</span>
      </div>
      <div class="card-body">
        <p class="muted" style="margin: 0; font-size: 13px">
          在顶部选一台机器人，就能看到它的浏览器画面、在里面登录或接管。
        </p>
      </div>
    </div>

    <template v-else-if="source === 'box'">
      <div class="card">
        <div class="card-header">
          <h2>Diana 内置浏览器</h2>
          <span class="badge" :class="status.running ? 'ok' : 'warn'">
            {{ status.running ? (status.takeover ? "你在操作" : "运行中") : "未启动" }}
          </span>
          <span class="card-sub">这台机器人自己的浏览器，登录态只属于它；机器人要用时会自动启动</span>
        </div>
        <div class="card-body stack">
          <p v-if="!status.available" class="muted" style="margin: 0; font-size: 13px">
            这台机器上没找到 Chrome/Chromium。容器完整版镜像自带 chromium；slim 版可以在宿主机执行
            <code class="mono">docker exec -u root &lt;容器名&gt; sh -c 'apt-get update &amp;&amp; apt-get install -y chromium fonts-noto-cjk'</code>。
          </p>
          <div class="field">
            <label class="switch-row">
              <input v-model="settings.headful" type="checkbox" :disabled="saving" @change="saveSettings" />
              <span>开一个真窗口（新装时按本机条件自动选：有显示器、或能自己拉起虚拟屏时打开；无头也有实时画面）</span>
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
            这台机器人的登录态目录：<code class="mono">{{ status.profile_dir }}</code>
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

    <BrowserControlPanel v-else-if="source === 'extension'" />

    <div v-if="source !== null" class="card">
      <div class="card-header">
        <h2>操作记录</h2>
        <span class="card-sub">
          {{ botID ? "这台机器人" : "所有机器人" }}在浏览器里做过什么、你什么时候启停和接管过；输入的文字只记字数
        </span>
        <button class="btn small ghost" type="button" style="margin-left: auto" @click="navigateToView('logs', { q: 'browser' })">
          查看全部
        </button>
      </div>
      <div class="card-body" style="padding-top: 4px">
        <article v-for="log in activity" :key="log.id" class="log-row">
          <span class="log-time">{{ formatTime(log.created_at) }}</span>
          <div class="log-main">
            <div class="cluster" style="gap: 6px; margin-bottom: 2px">
              <span class="badge" :class="log.level === 'error' ? 'err' : log.action === 'browser_action' ? 'ok' : ''">
                {{ activityWho(log) }}
              </span>
              <span class="log-message" style="margin: 0">{{ log.message }}</span>
              <span v-if="activityTarget(log)" class="muted mono browser-activity-target" :title="activityTarget(log)">
                {{ activityTarget(log) }}
              </span>
            </div>
            <p v-if="log.detail && log.detail !== log.message" class="log-detail">{{ log.detail }}</p>
          </div>
        </article>
        <p v-if="!activity.length" class="muted" style="margin: 8px 0 0; font-size: 13px">
          {{ activityLoaded ? "还没有记录。机器人用浏览器、或你在这里启停和接管时会记下来。" : "正在加载……" }}
        </p>
      </div>
    </div>
    <!-- 外接 CDP 是给自己另起了一个带调试端口的 Chrome 的人用的，属于技术细节，默认收起。
         它原来是扩展页的一个标签，后来挪到这里当第四档；改成二选一之后不再算一档。 -->
    <button class="btn ghost small browser-advanced-toggle" type="button" :aria-expanded="advancedOpen" @click="advancedOpen = !advancedOpen">
      <ChevronDown :size="14" :class="{ 'browser-advanced-open': advancedOpen }" aria-hidden="true" />
      高级：外接浏览器（CDP）
    </button>
    <template v-if="advancedOpen">
      <p class="muted" style="margin: 0; font-size: 12.5px">
        给某台机器人指一个你自己另起的、开着调试端口的 Chrome。只在上面没选「Diana 内置」，或这台机器人关掉了内置浏览器时生效。
      </p>
      <AgentBrowserPanel />
    </template>
  </section>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, reactive, ref } from "vue";
import { botScope } from "../bot-scope";
import { formatTime } from "../format";
import { navigate as navigateToView } from "../router";
import { ChevronDown } from "@lucide/vue";
import AgentBrowserPanel from "../components/AgentBrowserPanel.vue";
import BrowserControlPanel from "../components/BrowserControlPanel.vue";
import {
  browserBoxLiveURL,
  getBrowserSource,
  saveBrowserSource,
  type BrowserSource,
  getBrowserBoxStatus,
  saveBrowserBoxSettings,
  setBrowserBoxTakeover,
  startBrowserBox,
  stopBrowserBox,
  type BrowserBoxSettings,
  type BrowserBoxStatus,
  listBrowserActivity,
  type AppLogEntry
} from "../api";
import { toastError } from "../toast";

interface LiveFrame {
  data: string;
  width: number;
  height: number;
  scale: number;
}

const sources: { key: BrowserSource; label: string; hint: string }[] = [
  { key: "box", label: "Diana 内置（推荐）", hint: "Diana 自己的常驻浏览器，你能看画面、随时上手" },
  { key: "extension", label: "我自己的 Chrome", hint: "装一个扩展，机器人用你日常浏览器的登录态" },
  { key: "off", label: "不用", hint: "机器人只读公开网页，不碰任何登录态" }
];
// 当前机器人。页面按作用域重建（App 里 KeepAlive 以它为 key），这里取挂载时的值即可。
// 空串是「全部机器人」：各台的浏览器互相隔离，没法合在一起显示。
const botID = botScope.value;
// null 表示还没读到：读到之前不显示任何一边的配置，也不让切换。
const source = ref<BrowserSource | null>(null);
const switching = ref(false);
const activity = ref<AppLogEntry[]>([]);
const activityLoaded = ref(false);

function activityWho(log: AppLogEntry): string {
  if (log.action === "browser_action") return "机器人";
  if (log.action === "browser_control_connect" || log.action === "browser_control_disconnect") return "扩展";
  return "你";
}

// 只有网址和元素值得显示：启停、接管那几条的 target 是机器人 ID，页面上已经知道了。
function activityTarget(log: AppLogEntry): string {
  return log.action === "browser_action" || log.action === "browser_box_navigate" ? (log.target ?? "") : "";
}

// 操作记录跟着状态一起刷：机器人正在用浏览器时，这里应当看得见它刚做了什么。
async function loadActivity(): Promise<void> {
  try {
    activity.value = (await listBrowserActivity(botID || undefined, 20)).logs;
  } catch {
    // 记录只是辅助信息，读不到不打断这一页。
  } finally {
    activityLoaded.value = true;
  }
}
const advancedOpen = ref(false);

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

async function loadSource(): Promise<void> {
  try {
    source.value = (await getBrowserSource()).source;
  } catch (err) {
    toastError(err instanceof Error ? err.message : "读取浏览器来源失败");
  }
}

// 切换来源由后端同时改两边的总开关；切完重新读一遍，内置浏览器的状态和画面跟着变。
async function chooseSource(next: BrowserSource): Promise<void> {
  if (next === source.value || switching.value) return;
  switching.value = true;
  try {
    source.value = (await saveBrowserSource(next)).source;
    await refresh();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "切换浏览器来源失败");
    await loadSource();
  } finally {
    switching.value = false;
  }
}

async function refresh(): Promise<void> {
  try {
    const next = await getBrowserBoxStatus(botID || undefined);
    Object.assign(status, next);
    Object.assign(settings, next.settings);
    if (next.running && !socket && botID) connectLive();
    if (!next.running && socket) disconnectLive();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "读取内置浏览器状态失败");
  }
}

async function saveSettings(): Promise<void> {
  saving.value = true;
  try {
    const result = await saveBrowserBoxSettings({ ...settings }, botID || undefined);
    Object.assign(status, result.status);
    Object.assign(settings, result.settings);
    if (status.running && botID) connectLive();
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
    const result = await startBrowserBox(botID);
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
    const result = await stopBrowserBox(botID);
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
    const result = await setBrowserBoxTakeover(botID, !status.takeover);
    status.takeover = result.active;
  } catch (err) {
    toastError(err instanceof Error ? err.message : "切换失败");
  } finally {
    busy.value = false;
  }
}

function connectLive(): void {
  disconnectLive();
  const ws = new WebSocket(browserBoxLiveURL(botID));
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
  void loadSource();
  void loadActivity();
  statusTimer = window.setInterval(() => {
    void refresh();
    void loadActivity();
  }, 5000);
});

onBeforeUnmount(() => {
  if (statusTimer) window.clearInterval(statusTimer);
  disconnectLive();
});
</script>

<style scoped>
.browser-sources {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

/* 单选一行：圆点、名字、说明排成一行，窄屏时说明折到名字下面。 */
.browser-source {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  column-gap: 8px;
  row-gap: 2px;
  cursor: pointer;
}

.browser-source input {
  flex: none;
  margin: 0;
  accent-color: var(--accent);
  align-self: center;
}

.browser-source:has(input:disabled) {
  cursor: default;
}

.browser-source-name {
  font-weight: 600;
  font-size: 13.5px;
}

.browser-source-hint {
  font-size: 12.5px;
  color: var(--muted);
}

.browser-activity-target {
  font-size: 11.5px;
  max-width: 360px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.browser-advanced-toggle {
  align-self: flex-start;
}

.browser-advanced-toggle > svg {
  transition: transform 0.15s ease;
}

.browser-advanced-open {
  transform: rotate(180deg);
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
