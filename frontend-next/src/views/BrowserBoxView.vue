<!-- Copyright (c) 2025-now SuInk. Licensed under the Limited Redistribution License. -->
<!--
  浏览器这一页只回答一个问题：机器人用哪个浏览器。

  Diana 内置和用户自己的 Chrome（扩展）做的是同一件事——带登录态、只有主人能驱动、
  能点能输入——区别只在用谁的。两个可以同时开着，排一个优先级：每一轮用排在前面、
  开着而且用得上的那个，前一个用不了就换下一个（见 model/browsersource）。一次性无头
  渲染不在这里：它不带登录态，读公开网页、出图都靠它，一直可用，依赖和参数在插件页
  的「网页渲染」里。以前三者并排成「三档」，用户得先弄懂三者区别才能开始用。
-->
<template>
  <section class="stack">
    <div class="card">
      <div class="card-header">
        <h2>浏览器</h2>
        <span class="card-sub">带着登录态、只有主人能让机器人驱动的浏览器。两个可以都开着，按下面的顺序先用排在前面的</span>
      </div>
      <div class="card-body stack">
        <!-- 和插件页同一种卡片：开关、状态、说明，左下角是运行依赖。序号就是优先级。 -->
        <div v-if="sourceState" class="plugin-tiles browser-source-tiles">
          <article
            v-for="(key, index) in sourceState.order"
            :key="key"
            class="plugin-card"
            :class="{ off: !sourceState[key].enabled }"
          >
            <div class="plugin-card-head">
              <h2 class="plugin-card-name">
                <span class="browser-source-rank" :title="`优先级 ${index + 1}`">{{ index + 1 }}</span>
                {{ sourceMeta[key].label }}
              </h2>
              <label class="switch" :title="switchTitle(key)">
                <input
                  type="checkbox"
                  :checked="sourceState[key].enabled"
                  :disabled="savingSource || (key === 'box' && !sourceState.box.detected && !sourceState.box.enabled)"
                  @change="toggleSource(key, ($event.target as HTMLInputElement).checked)"
                />
                <span class="track" aria-hidden="true"></span>
              </label>
            </div>
            <div class="cluster plugin-card-badges">
              <span class="badge" :class="sourceStatus(key).tone">{{ sourceStatus(key).text }}</span>
              <span v-if="key === 'box'" class="badge">推荐</span>
              <span class="badge">只有主人能用</span>
            </div>
            <p class="plugin-card-desc">{{ sourceMeta[key].hint }}</p>
            <div class="plugin-card-bottom">
              <div class="plugin-card-meta">
                <button class="plugin-dependencies-head" type="button" title="查看运行依赖" @click="dependenciesTarget = key">
                  <span>运行依赖</span>
                  <span class="plugin-dependency-count" :class="{ warn: dependencyProblem(key) }">
                    {{ sourceState[key].dependencies.filter((dep) => dep.available).length }}/{{ sourceState[key].dependencies.length }}
                  </span>
                </button>
              </div>
              <footer class="plugin-card-foot">
                <button
                  class="btn small"
                  type="button"
                  :disabled="savingSource || index === 0"
                  :aria-label="`${sourceMeta[key].label}：优先级往前挪`"
                  @click="moveSource(index, -1)"
                >
                  <ArrowUp :size="14" aria-hidden="true" />
                  优先
                </button>
                <button
                  class="btn small"
                  type="button"
                  :disabled="savingSource || index === sourceState.order.length - 1"
                  :aria-label="`${sourceMeta[key].label}：优先级往后挪`"
                  @click="moveSource(index, 1)"
                >
                  <ArrowDown :size="14" aria-hidden="true" />
                  靠后
                </button>
              </footer>
            </div>
          </article>
        </div>
        <p v-if="sourceState" class="hint" style="margin: 0">
          {{
            sourceState.active === "off"
              ? "现在机器人不用浏览器：两个都关着，或者开着的那个眼下用不上。"
              : `这一轮机器人用「${sourceMeta[sourceState.active].label}」；排在前面的用不了时会自动换下一个。`
          }}
        </p>
        <p class="muted" style="margin: 0; font-size: 12.5px">
          读公开网页、出图、渲染 PDF 用的是另一个一次性无头浏览器，不带登录态、群成员也能用，一直开着，不用在这里选；
          它缺什么依赖在「扩展」页的「网页渲染」里看。
        </p>
      </div>
    </div>

    <div v-if="sourceState?.box.enabled && !botID" class="card">
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

    <template v-else-if="sourceState?.box.enabled">
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
          <div class="field wide">
            <label class="switch">
              <input v-model="settings.headful" type="checkbox" :disabled="saving" @change="saveSettings" />
              <span class="track" aria-hidden="true"></span>
              <span class="switch-label">开一个真窗口</span>
            </label>
            <span class="hint">新装时按本机条件自动选：有显示器、或能自己拉起虚拟屏时打开。无头也有实时画面。</span>
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

    <BrowserControlPanel v-if="sourceState?.extension.enabled" />

    <Modal
      v-if="dependenciesTarget && sourceState"
      :title="`${sourceMeta[dependenciesTarget].label} · 运行依赖`"
      @close="dependenciesTarget = null"
    >
      <p class="plugin-dependencies-hint">
        {{
          dependenciesTarget === "box"
            ? "内置浏览器要一个 Chrome/Chromium，中文页面截图要中文字体；显示器只影响能不能开真窗口，没有也能无头跑。"
            : "扩展装在你自己的 Chrome 里、反向连到这里。打开开关后在下面的「浏览器控制扩展」里下载扩展源码包。"
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
    <!-- 外接 CDP 是给自己另起了一个带调试端口的 Chrome 的人用的，属于技术细节，默认收起。
         它原来是扩展页的一个标签，后来挪到这里当第四档；改成开关加优先级之后不再算一档。 -->
    <button class="btn ghost small browser-advanced-toggle" type="button" :aria-expanded="advancedOpen" @click="advancedOpen = !advancedOpen">
      <ChevronDown :size="14" :class="{ 'browser-advanced-open': advancedOpen }" aria-hidden="true" />
      高级：外接浏览器（CDP）
    </button>
    <template v-if="advancedOpen">
      <p class="muted" style="margin: 0; font-size: 12.5px">
        给某台机器人指一个你自己另起的、开着调试端口的 Chrome。只在这一轮没用上内置浏览器时生效：它关着、用不上，或者排在前面的 Chrome 正在用。
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
import { ArrowDown, ArrowUp, ChevronDown } from "@lucide/vue";
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
const sourceMeta: Record<SourceKey, { label: string; hint: string }> = {
  box: { label: "Diana 内置浏览器", hint: "Diana 自己的常驻浏览器，你能看画面、随时上手。本机找得到 Chrome 时会自动打开。" },
  extension: { label: "我自己的 Chrome", hint: "装一个扩展，机器人用你日常浏览器的登录态。操作的是你真实的浏览器，要你自己打开。" }
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

function switchTitle(key: SourceKey): string {
  const item = sourceState.value?.[key];
  if (key === "box" && item && !item.detected && !item.enabled) return "没检测到 Chrome，先在运行依赖里装上";
  return item?.enabled ? "点击关闭" : "点击打开";
}

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

function toggleSource(key: SourceKey, checked: boolean): void {
  void patchSource(key === "box" ? { box_enabled: checked } : { extension_enabled: checked });
}

function moveSource(index: number, delta: -1 | 1): void {
  const order = [...(sourceState.value?.order ?? [])];
  const target = index + delta;
  if (target < 0 || target >= order.length) return;
  [order[index], order[target]] = [order[target], order[index]];
  void patchSource({ order });
}

// 每一行右边那枚标签：这一轮是不是在用它、用不上的话卡在哪。
function sourceStatus(key: SourceKey): { text: string; tone: string } {
  const state = sourceState.value;
  if (!state) return { text: "读取中", tone: "" };
  const item = state[key];
  if (state.active === key) return { text: "正在用", tone: "ok" };
  if (!item.enabled) return { text: item.detected ? (key === "box" ? "检测到 Chrome" : "检测到扩展") : "已关闭", tone: "" };
  if (item.usable) return { text: "备用", tone: "" };
  return { text: key === "box" ? "没找到 Chrome" : "等扩展连接", tone: "warn" };
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

/* 只有两张卡，并排放；窄屏时插件页的网格会自己折成一列。 */
.browser-source-tiles {
  grid-template-columns: repeat(auto-fill, minmax(min(100%, 320px), 1fr));
}

.browser-source-rank {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  margin-right: 6px;
  border-radius: 50%;
  vertical-align: 2px;
  font-size: 11.5px;
  font-weight: 600;
  color: var(--muted);
  background: var(--surface-2, rgba(127, 127, 127, 0.12));
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
