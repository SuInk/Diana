<!-- Copyright (c) 2025-now SuInk. Licensed under the Limited Redistribution License. -->
<!--
  浏览器这一页只回答一个问题：机器人用哪个浏览器。

  Diana 内置和用户自己的 Chrome（扩展）做的是同一件事——带登录态、只有主人能驱动、
  能点能输入——区别只在用谁的。一行一个勾选框，打勾就是启用，可以都勾上；都勾上
  时可以调优先级，排在上面的先用，它用不了时自动换另一个（见 model/browsersource）。一次性无头
  渲染不在这里：它不带登录态，读公开网页、出图都靠它，一直可用，依赖和参数在插件页
  的「网页渲染」里。以前三者并排成「三档」，用户得先弄懂三者区别才能开始用。
-->
<template>
  <section class="stack">
    <div class="card">
      <div class="card-header">
        <h2>浏览器</h2>
        <span class="card-sub">机器人要登录、点按钮时用的浏览器，只有主人能让它用</span>
      </div>
      <div class="card-body stack">
        <!-- 一行一项，打勾就是启用（和「上下文」页的勾选列表同一种写法），按优先级从上往下排；
             两个都启用时才出现「优先用」，排在上面的先用，它用不了时自动换下一个。 -->
        <div class="browser-toggle-list">
          <div v-for="(key, index) in orderedKeys" :key="key" class="browser-toggle-row">
            <input
              :id="`browser-source-${key}`"
              type="checkbox"
              :checked="sourceState?.[key].enabled"
              :disabled="savingSource || !sourceState"
              @change="toggleSource(key, ($event.target as HTMLInputElement).checked)"
            />
            <div class="browser-toggle-copy">
              <div class="browser-toggle-title">
                <label :for="`browser-source-${key}`">{{ sourceMeta[key].title }}</label>
                <span v-if="enabledCount > 1 && sourceState?.[key].enabled" class="browser-toggle-rank" :title="`优先级 ${index + 1}`">
                  第 {{ index + 1 }} 优先
                </span>
                <span v-if="sourceState && sourceState.active === key" class="badge ok">正在用</span>
                <span v-else-if="sourceState?.[key].enabled && sourceState[key].usable" class="badge">备用</span>
                <span v-else-if="sourceState?.[key].enabled" class="badge warn">{{ key === "box" ? "没找到 Chrome" : "等扩展连接" }}</span>
              </div>
              <p class="browser-toggle-desc">{{ sourceMeta[key].hint }}</p>
              <div v-if="sourceState" class="browser-toggle-meta">
                <button type="button" :class="{ warn: dependencyProblem(key) }" @click="dependenciesTarget = key">
                  运行依赖 {{ sourceState[key].dependencies.filter((dep) => dep.available).length }}/{{ sourceState[key].dependencies.length }}
                </button>
                <button
                  v-if="enabledCount > 1 && sourceState[key].enabled && index > 0"
                  type="button"
                  :disabled="savingSource"
                  @click="moveSource(index, -1)"
                >
                  <ArrowUp :size="13" aria-hidden="true" />
                  优先用
                </button>
                <a v-if="key === 'extension' && sourceState.extension.enabled && !sourceState.extension.detected" href="/api/browser-control/extension.zip" download>
                  下载扩展
                </a>
              </div>
              <!-- 内置浏览器的启停和接管属于这一行，别飘在列表外面。默认归机器人用，不设「我来操作」：
                   在画面上点一下、敲一下键或在地址栏跳转就自动转为你接管，这时才出现「交还给机器人」。 -->
              <div v-if="key === 'box' && sourceState?.box.enabled && botID" class="browser-toggle-actions">
                <button class="btn small" type="button" :disabled="busy || status.running" @click="start">启动</button>
                <button class="btn small ghost" type="button" :disabled="busy || !status.running" @click="stop">停止</button>
                <template v-if="status.running && status.takeover">
                  <button class="btn small warn" type="button" :disabled="busy" @click="handBack">交还给机器人</button>
                  <span class="browser-toggle-note">你在画面上动过手，机器人暂时用不了这个浏览器</span>
                </template>
                <span v-if="status.last_error" class="browser-toggle-error">最近一次错误：{{ status.last_error }}</span>
              </div>
            </div>
          </div>
        </div>

        <p v-if="sourceState?.box.enabled && !botID" class="muted" style="margin: 0; font-size: 13px">
          每台机器人各用一个内置浏览器，登录态互不相通。在顶部选一台机器人，就能看到它的画面、在里面登录。
        </p>
      </div>
    </div>
    <div v-if="sourceState?.box.enabled && botID && status.running" class="card">
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
          <div v-else-if="liveNotice" class="browser-live-notice">
            <p style="margin: 0; font-size: 13px">{{ liveNotice }}</p>
            <button class="btn small ghost" type="button" @click="reconnectLive">重新连接</button>
          </div>
          <p v-else class="muted" style="margin: 0; font-size: 13px">正在连接画面……</p>
        </div>
        <p class="muted" style="margin: 0; font-size: 12.5px">
          点一下画面再打字，键盘事件才会送到页面。密码这类东西你自己输，机器人看不到你敲了什么——它只能看到页面最终长什么样。
        </p>
      </div>
    </div>

    <Modal
      v-if="dependenciesTarget && sourceState"
      :title="`${sourceMeta[dependenciesTarget].label} · 运行依赖`"
      @close="dependenciesTarget = null"
    >
      <p class="plugin-dependencies-hint">
        {{
          dependenciesTarget === "box"
            ? "内置浏览器要一个 Chrome/Chromium，中文页面截图要中文字体；显示器只影响能不能开真窗口，没有也能无头跑。"
            : "扩展装在你自己的 Chrome 里、反向连到这里。勾上「我自己的 Chrome」后，点「下载扩展」拿到扩展源码包。"
        }}
      </p>
      <PluginDependencyList
        :dependencies="sourceState[dependenciesTarget].dependencies"
        :loading="detecting"
        :busy="busyDependency"
        @install="installDependency"
      />
      <template #footer>
        <button class="btn" type="button" :disabled="detecting" @click="redetect">重新检测</button>
        <button class="btn primary" type="button" @click="dependenciesTarget = null">完成</button>
      </template>
    </Modal>

    <div v-if="sourceState !== null" class="card">
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
    <!-- 开箱即用：默认什么都不用配。其余的（开真窗口、扩展的令牌和网站名单、外接 CDP）
         都收在这里。 -->
    <button class="btn ghost small browser-advanced-toggle" type="button" :aria-expanded="advancedOpen" @click="advancedOpen = !advancedOpen">
      <ChevronDown :size="14" :class="{ 'browser-advanced-open': advancedOpen }" aria-hidden="true" />
      更多设置
    </button>
    <template v-if="advancedOpen">
      <div class="card">
        <div class="card-body stack">
          <div v-if="sourceState?.box.enabled && botID" class="field wide">
            <label class="switch">
              <input v-model="settings.headful" type="checkbox" :disabled="saving" @change="saveSettings" />
              <span class="track" aria-hidden="true"></span>
              <span class="switch-label">开一个真窗口</span>
            </label>
            <span class="hint">
              默认按本机条件自动选；无头也有实时画面。这台机器人的登录态在
              <code class="mono">{{ status.profile_dir }}</code>。
            </span>
          </div>
        </div>
      </div>
      <BrowserControlPanel v-if="sourceState?.extension.enabled || preferred === 'extension'" />
      <AgentBrowserPanel />
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from "vue";
import { botScope } from "../bot-scope";
import { formatTime } from "../format";
import { navigate as navigateToView } from "../router";
import { ArrowUp, ChevronDown } from "@lucide/vue";
import AgentBrowserPanel from "../components/AgentBrowserPanel.vue";
import Modal from "../components/Modal.vue";
import PluginDependencyList from "../components/PluginDependencyList.vue";
import BrowserControlPanel from "../components/BrowserControlPanel.vue";
import {
  browserBoxLiveURL,
  getBrowserSource,
  saveBrowserSource,
  type BrowserSource,
  type BrowserSourceState,
  installResolverDependency,
  listPluginDependencies,
  type ResolverDependency,
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
import { toastError, toastSuccess } from "../toast";

interface LiveFrame {
  data: string;
  width: number;
  height: number;
  scale: number;
}

type SourceKey = Exclude<BrowserSource, "off">;
const sourceKeys: SourceKey[] = ["box", "extension"];
const sourceMeta: Record<SourceKey, { label: string; short: string; title: string; hint: string }> = {
  box: {
    label: "Diana 内置",
    short: "内置浏览器",
    title: "Diana 内置浏览器",
    hint: "Diana 自己的浏览器，每台机器人一份登录态，你能看画面、随时接管。"
  },
  extension: {
    label: "我自己的 Chrome",
    short: "你的 Chrome",
    title: "我自己的 Chrome",
    hint: "装一个扩展，机器人用你日常 Chrome 的登录态，只能碰你允许的网站。"
  }
};
// null 表示还没读到：读到之前不显示任何一边的配置。
const sourceState = ref<BrowserSourceState | null>(null);
const savingSource = ref(false);
const dependenciesTarget = ref<SourceKey | null>(null);
const detecting = ref(false);
const busyDependency = ref("");

// 显示器是可选的：没有它照样能无头跑，不算「缺依赖」。
function dependencyProblem(key: SourceKey): boolean {
  return (sourceState.value?.[key].dependencies ?? []).some((dep) => !dep.available && dep.name !== "display");
}

// 选中的那个：排在第一位、而且开着。都关着就是 null，页面显示「浏览器关着」。
const preferred = computed<SourceKey | null>(() => {
  const state = sourceState.value;
  if (!state) return null;
  const first = state.order[0];
  if (first && state[first].enabled) return first;
  const next = state.order.find((key) => state[key].enabled);
  return next ?? null;
});

// 重新检测复用插件页那套：刷新浏览器探测的缓存，再把这一页的状态读一遍。
async function redetect(): Promise<void> {
  detecting.value = true;
  try {
    await listPluginDependencies(true);
    await loadSource();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "检测失败");
  } finally {
    detecting.value = false;
  }
}

async function installDependency(dependency: ResolverDependency): Promise<void> {
  busyDependency.value = dependency.name;
  try {
    await installResolverDependency(dependency.name);
    toastSuccess(`已安装 ${dependency.name}`);
  } catch (err) {
    toastError(err instanceof Error ? err.message : `安装 ${dependency.name} 失败`);
  } finally {
    busyDependency.value = "";
    await redetect();
  }
}
// 当前机器人。页面按作用域重建（App 里 KeepAlive 以它为 key），这里取挂载时的值即可。
// 空串是「全部机器人」：各台的浏览器互相隔离，没法合在一起显示。
const botID = botScope.value;
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
// 画面连不上或断掉的原因。只在还没有画面时顶替「正在连接画面……」；重连时不清空，
// 标签页一直卡着的话，用户看到的是原因而不是一闪一闪的「正在连接」。
const liveNotice = ref("");
let firstFrameTimer: number | undefined;
// 连上之后多久还没有第一帧就提示：卡死的页面出不了帧，Page.startScreencast 照样成功。
const firstFrameTimeoutMS = 10_000;

function clearFirstFrameTimer(): void {
  if (firstFrameTimer !== undefined) window.clearTimeout(firstFrameTimer);
  firstFrameTimer = undefined;
}

async function loadSource(): Promise<void> {
  try {
    sourceState.value = await getBrowserSource();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "读取浏览器来源失败");
  }
}

async function patchSource(patch: Parameters<typeof saveBrowserSource>[0]): Promise<void> {
  savingSource.value = true;
  try {
    sourceState.value = await saveBrowserSource(patch);
    await refresh();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "保存浏览器设置失败");
    await loadSource();
  } finally {
    savingSource.value = false;
  }
}

// 打勾只管启用，不动顺序；顺序用「优先用」调。
function toggleSource(key: SourceKey, checked: boolean): void {
  void patchSource(key === "box" ? { box_enabled: checked } : { extension_enabled: checked });
}

// 按优先级排的行；还没读到时按默认顺序。
const orderedKeys = computed<SourceKey[]>(() => sourceState.value?.order ?? sourceKeys);
const enabledCount = computed(() => sourceKeys.filter((key) => sourceState.value?.[key].enabled).length);

function moveSource(index: number, delta: -1 | 1): void {
  const order = [...orderedKeys.value];
  const target = index + delta;
  if (target < 0 || target >= order.length) return;
  [order[index], order[target]] = [order[target], order[index]];
  void patchSource({ order });
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

async function handBack(): Promise<void> {
  busy.value = true;
  try {
    const result = await setBrowserBoxTakeover(botID, false);
    status.takeover = result.active;
  } catch (err) {
    toastError(err instanceof Error ? err.message : "切换失败");
  } finally {
    busy.value = false;
  }
}

function connectLive(): void {
  closeLiveSocket();
  const ws = new WebSocket(browserBoxLiveURL(botID));
  socket = ws;
  ws.onmessage = (event) => {
    const message = JSON.parse(event.data) as {
      type: string;
      frame?: LiveFrame;
      tab?: { url?: string; title?: string };
      message?: string;
    };
    if (message.type === "frame" && message.frame) {
      clearFirstFrameTimer();
      liveNotice.value = "";
      frame.value = message.frame;
    } else if (message.type === "ready" && message.tab) {
      addressInput.value = message.tab.url ?? "";
      currentTitle.value = message.tab.title ?? "";
      clearFirstFrameTimer();
      firstFrameTimer = window.setTimeout(() => {
        if (socket === ws && !frame.value) {
          liveNotice.value = "画面 10 秒还没出来：这个页面可能卡住了（脚本卡死或渲染进程崩溃）。可以点「刷新」、在地址栏换个网址，或者重新连接。";
        }
      }, firstFrameTimeoutMS);
    } else if (message.type === "error") {
      // 标签页卡死或崩溃时后端会说明原因再断开；换掉之前的画面，别让人对着最后一帧干等。
      clearFirstFrameTimer();
      frame.value = null;
      liveNotice.value = message.message || "画面连接出错了";
    }
  };
  ws.onclose = () => {
    if (socket !== ws) return;
    socket = null;
    clearFirstFrameTimer();
    // 状态轮询会在几秒内自动重连；还没有画面时先把断开说清楚。
    if (!frame.value && !liveNotice.value) liveNotice.value = "画面连接断开了，几秒后自动重连。";
  };
}

function closeLiveSocket(): void {
  clearFirstFrameTimer();
  if (!socket) return;
  socket.onclose = null;
  socket.close();
  socket = null;
  frame.value = null;
}

function disconnectLive(): void {
  closeLiveSocket();
  liveNotice.value = "";
}

function reconnectLive(): void {
  liveNotice.value = "";
  connectLive();
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
  // 扩展连上、断开或被接管都会改变「这一轮用哪个」，跟着状态一起刷。
  statusTimer = window.setInterval(() => {
    void refresh();
    void loadSource();
    void loadActivity();
  }, 5000);
});

onBeforeUnmount(() => {
  if (statusTimer) window.clearInterval(statusTimer);
  disconnectLive();
});
</script>

<style scoped>
.browser-activity-target {
  font-size: 11.5px;
  max-width: 360px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.browser-toggle-list {
  display: flex;
  flex-direction: column;
}

/* 一行一项：勾选框在行首，和标题第一行对齐；右边是标题、说明、小链接。 */
.browser-toggle-row {
  display: grid;
  grid-template-columns: 18px minmax(0, 1fr);
  align-items: start;
  gap: 10px;
  padding: 14px 0;
  border-top: 1px solid var(--border);
}

.browser-toggle-row:first-child {
  border-top: 0;
  padding-top: 0;
}

.browser-toggle-row:last-child {
  padding-bottom: 0;
}

.browser-toggle-row > input[type="checkbox"] {
  width: 16px;
  height: 16px;
  margin: 4px 0 0;
  accent-color: var(--accent);
  cursor: pointer;
}

.browser-toggle-title label {
  font-weight: 600;
  cursor: pointer;
}

.browser-toggle-copy {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.browser-toggle-title {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  min-height: 24px;
  font-size: 14px;
}

.browser-toggle-rank {
  font-size: 12px;
  color: var(--muted);
}

.browser-toggle-desc {
  margin: 0;
  font-size: 13px;
  line-height: 1.55;
  color: var(--muted);
}

.browser-toggle-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 14px;
  font-size: 12.5px;
}

.browser-toggle-meta button,
.browser-toggle-meta a {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 0;
  border: 0;
  background: none;
  color: var(--accent);
  font: inherit;
  text-decoration: none;
  cursor: pointer;
}

.browser-toggle-meta button.warn {
  color: var(--warn);
}

.browser-toggle-meta button:disabled {
  opacity: 0.5;
  cursor: default;
}

.browser-toggle-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-top: 6px;
}

.browser-toggle-note {
  color: var(--muted);
  font-size: 12.5px;
}

.browser-toggle-error {
  font-size: 12.5px;
  color: var(--warn);
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

.browser-live-notice {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  max-width: 520px;
  padding: 16px;
  text-align: center;
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
