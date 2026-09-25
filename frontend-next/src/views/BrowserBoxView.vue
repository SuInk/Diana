<!-- Copyright (c) 2025-now SuInk. Licensed under the Limited Redistribution License. -->
<!--
  机器人用到的浏览器全在这一页，按用途分两组：

  - 读网页、出图：网页渲染。每次开一个全新的无头 Chrome，用完就扔，不带登录态，谁的
    消息都能用。它本身是插件页里的「网页渲染」插件，这里放同一个开关，免得用户以为
    浏览器只有下面那几个。
  - 登录、点按钮：Diana 内置和用户自己的 Chrome（扩展）做的是同一件事——带登录态、只有
    主人能驱动——区别只在用谁的。一行一个勾选框，可以都勾上并排优先级，排在上面的先用，
    它用不了时自动换另一个（见 model/browsersource）。外接 CDP 不参与排序：它是 browser_*
    那组工具在内置浏览器没在用时改接的地址，所以只显示状态，设置在「更多设置」里。

  四种都复用这台机器上的 Chrome/Chromium，区别在用哪份登录态、谁能驱动。
-->
<template>
  <section class="stack">
    <div class="card">
      <div class="card-header">
        <h2>浏览器</h2>
        <span class="card-sub">机器人用到的浏览器都在这里，全都复用这台机器上的 Chrome，区别在用哪份登录态</span>
      </div>
      <div class="card-body stack">
        <div class="browser-group-title">
          <h3>读网页、出图</h3>
          <span>不带登录态，谁的消息都能用</span>
        </div>
        <div class="browser-toggle-list">
          <div class="browser-toggle-row">
            <input
              id="browser-source-render"
              type="checkbox"
              :checked="renderPlugin?.enabled"
              :disabled="savingRender || !renderPlugin"
              @change="toggleRender(($event.target as HTMLInputElement).checked)"
            />
            <div class="browser-toggle-copy">
              <div class="browser-toggle-title">
                <label for="browser-source-render">网页渲染</label>
                <template v-if="renderPlugin?.enabled">
                  <span v-if="dependencyProblem('render')" class="badge warn">没找到 Chrome</span>
                  <span v-else class="badge ok">一直在用</span>
                </template>
              </div>
              <p class="browser-toggle-desc">
                每次开一个全新的 Chrome，用完就扔。群里发的链接自动读出来、模型查网页（browser_render）都靠它，默认有头、不容易被网站拦；HTML 转图片这类本地渲染用无头。
              </p>
              <div v-if="renderPlugin" class="browser-toggle-meta">
                <button type="button" :class="{ warn: dependencyProblem('render') }" @click="dependenciesTarget = 'render'">
                  运行依赖 {{ renderDependencies.filter((dep) => dep.available).length }}/{{ renderDependencies.length }}
                </button>
                <span class="browser-inline-select">
                  <label for="browser-render-window">窗口</label>
                  <AppSelect
                    id="browser-render-window"
                    :model-value="renderWindowMode"
                    :options="renderWindowOptions"
                    :disabled="savingRender"
                    @update:model-value="setRenderWindowMode"
                  />
                </span>
              </div>
            </div>
          </div>
        </div>

        <div class="browser-group-title">
          <h3>登录、点按钮</h3>
          <span>带登录态，只有主人能让机器人用；都勾上时排在上面的先用</span>
        </div>
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
              <!-- 勾上就是开着，没有启动、停止、「我来操作」这些按钮：进程在机器人要用或你打开这一页时
                   自动拉起，取消勾选才停。接管和「交还给机器人」在画面底部的状态栏里。 -->
              <p v-if="key === 'box' && sourceState?.box.enabled && botID && status.last_error" class="browser-toggle-error">
                最近一次错误：{{ status.last_error }}
              </p>
            </div>
          </div>
          <!-- 外接 CDP 没有勾选框：配了地址就算有，内置浏览器没在用的时候 browser_* 改接它。 -->
          <div class="browser-toggle-row">
            <span class="browser-toggle-spacer" aria-hidden="true"></span>
            <div class="browser-toggle-copy">
              <div class="browser-toggle-title">
                <span class="browser-toggle-label">外接浏览器（CDP）</span>
                <template v-if="botID">
                  <span v-if="!externalCDPConfigured" class="badge">没配置</span>
                  <span v-else-if="sourceState?.active === 'box'" class="badge">内置在用，暂不用它</span>
                  <span v-else class="badge ok">正在用</span>
                </template>
              </div>
              <p class="browser-toggle-desc">
                接一个你自己带 --remote-debugging-port 起的浏览器，用它的登录态。只在这台机器人的内置浏览器没在用时顶上。
              </p>
              <div class="browser-toggle-meta">
                <span v-if="externalCDPConfigured" class="mono">{{ agentBrowser?.cdp_url }}</span>
                <button type="button" @click="openExternalCDPSettings">{{ externalCDPConfigured ? "修改地址" : "配置地址" }}</button>
              </div>
            </div>
          </div>
        </div>

        <p v-if="sourceState?.box.enabled && !botID" class="muted" style="margin: 0; font-size: 13px">
          每台机器人各用一个内置浏览器，登录态互不相通。在顶部选一台机器人，就能看到它的画面、在里面登录。
        </p>
      </div>
    </div>
    <!-- 画面做成一个浏览器窗口：标签、工具栏、画面、状态栏拼成一整块。谁在控制这个浏览器
         是这里最要紧的信息，放在状态栏里一直看得见，交还也在那里点。 -->
    <div v-if="sourceState?.box.enabled && botID && status.running" class="card browser-live-card">
      <div ref="liveWindow" class="browser-window" :class="{ 'is-takeover': status.takeover, 'is-fullscreen': fullscreen }">
        <div class="browser-tabbar">
          <span class="browser-tab" :title="currentTitle || addressInput">
            <Globe :size="13" aria-hidden="true" />
            <span class="browser-tab-title">{{ currentTitle || pageHost || "新标签页" }}</span>
          </span>
        </div>
        <div class="browser-toolbar">
          <button class="browser-tool" type="button" title="后退" aria-label="后退" :disabled="!status.takeover" @click="send({ type: 'back' })">
            <ArrowLeft :size="16" aria-hidden="true" />
          </button>
          <button class="browser-tool" type="button" title="刷新" aria-label="刷新" :disabled="!status.takeover" @click="send({ type: 'reload' })">
            <RotateCw :size="15" aria-hidden="true" />
          </button>
          <label class="browser-address" :class="{ readonly: !status.takeover }">
            <Lock v-if="addressSecure" :size="13" class="browser-address-icon" aria-hidden="true" />
            <Search v-else :size="13" class="browser-address-icon" aria-hidden="true" />
            <input
              ref="addressField"
              v-model="addressInput"
              aria-label="网址"
              :placeholder="status.takeover ? '输入网址，回车打开' : ''"
              :readonly="!status.takeover"
              :title="status.takeover ? '' : '接管之后才能换网址'"
              spellcheck="false"
              autocomplete="off"
              @focus="($event.target as HTMLInputElement).select()"
              @keydown.enter="onAddressEnter"
            />
          </label>
          <button
            class="browser-tool"
            type="button"
            :title="fullscreen ? '退出全屏' : '全屏'"
            :aria-label="fullscreen ? '退出全屏' : '全屏'"
            @click="toggleFullscreen"
          >
            <Minimize2 v-if="fullscreen" :size="15" aria-hidden="true" />
            <Maximize2 v-else :size="15" aria-hidden="true" />
          </button>
        </div>
        <div class="browser-progress" :class="{ active: pageLoading }" aria-hidden="true"></div>

        <div class="browser-stage" :class="{ empty: !hasFrame }" @contextmenu.prevent>
          <!-- canvas 一直在：帧是异步解码后画上去的，没有画面时只是藏起来。 -->
          <canvas
            v-show="hasFrame"
            ref="screen"
            class="browser-screen"
            aria-label="内置浏览器画面"
            tabindex="0"
            @mousedown="onMouse($event, 'mousePressed')"
            @mouseup="onMouse($event, 'mouseReleased')"
            @mousemove="onMouseMove"
            @wheel="onWheel"
            @keydown="onKey($event, 'keyDown')"
            @keyup="onKey($event, 'keyUp')"
          />
          <div v-if="!hasFrame && liveNotice" class="browser-live-notice">
            <p style="margin: 0; font-size: 13px">{{ liveNotice }}</p>
            <button class="btn small ghost" type="button" @click="reconnectLive">重新连接</button>
          </div>
          <p v-else-if="!hasFrame" class="muted" style="margin: 0; font-size: 13px">正在连接画面……</p>
        </div>

        <div class="browser-statusbar" role="status">
          <span class="browser-status-dot" aria-hidden="true"></span>
          <template v-if="status.takeover">
            <strong>你在操作</strong>
            <span class="browser-status-hint">
              机器人先停下，也看不到你敲了什么；离开这个画面或 {{ takeoverIdleMinutes }} 分钟不操作就交还给它
            </span>
            <button class="btn small" type="button" :disabled="busy" @click="handBack">交还给机器人</button>
          </template>
          <template v-else>
            <strong>机器人在用</strong>
            <span class="browser-status-hint">你只能看；要自己登录、点按钮，先接管</span>
            <button class="btn small primary" :class="{ 'browser-nudge': nudging }" type="button" :disabled="busy" @click="takeOver">接管</button>
          </template>
          <span class="browser-status-live" :class="{ warn: !liveHealthy }">{{ liveLabel }}</span>
        </div>
      </div>
    </div>

    <Modal
      v-if="dependenciesTarget && sourceState"
      :title="`${dependenciesTarget === 'render' ? '网页渲染' : sourceMeta[dependenciesTarget].label} · 运行依赖`"
      @close="dependenciesTarget = null"
    >
      <p class="plugin-dependencies-hint">
        {{
          dependenciesTarget === "render"
            ? "网页渲染复用这台机器上的 Chrome/Chromium，缺了可以装；中文字体可一键下载，出图时缺的多语言字体会自动补齐。"
            : dependenciesTarget === "box"
              ? "内置浏览器要一个 Chrome/Chromium，中文页面截图要中文字体；显示器只影响能不能开真窗口，没有也能无头跑。"
              : "扩展装在你自己的 Chrome 里、反向连到这里。勾上「我自己的 Chrome」后，点「下载扩展」拿到扩展源码包。"
        }}
      </p>
      <PluginDependencyList
        :dependencies="dependenciesTarget === 'render' ? renderDependencies : sourceState[dependenciesTarget].dependencies"
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
      <div ref="externalCDPPanel">
        <AgentBrowserPanel @saved="loadExternalCDP" />
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, nextTick, onActivated, onBeforeUnmount, onDeactivated, onMounted, reactive, ref } from "vue";
import { botScope } from "../bot-scope";
import { formatTime } from "../format";
import { pluginForBot } from "../plugin-settings";
import { navigate as navigateToView } from "../router";
import { ArrowLeft, ArrowUp, ChevronDown, Globe, Lock, Maximize2, Minimize2, RotateCw, Search } from "@lucide/vue";
import AgentBrowserPanel from "../components/AgentBrowserPanel.vue";
import AppSelect, { type AppSelectOption } from "../components/AppSelect.vue";
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
  type BrowserBoxSettings,
  type BrowserBoxStatus,
  listBrowserActivity,
  type AppLogEntry,
  getAgentBrowser,
  type AgentBrowserSettings,
  listPlugins,
  setPluginEnabled,
  updatePluginSettings,
  type PluginState
} from "../api";
import { toastError, toastSuccess } from "../toast";

/** 一帧画面的元数据。width/height 是页面的 CSS 尺寸，点击坐标按它换算。 */
interface LiveFrameMeta {
  width: number;
  height: number;
  timestamp?: number;
}

type SourceKey = Exclude<BrowserSource, "off">;
const sourceKeys: SourceKey[] = ["box", "extension"];
const sourceMeta: Record<SourceKey, { label: string; short: string; title: string; hint: string }> = {
  box: {
    label: "Diana 内置",
    short: "内置浏览器",
    title: "Diana 内置浏览器",
    hint: "用这台机器上的 Chrome 单独开一个，不碰你日常浏览器的登录态；每台机器人一份，你能看画面、随时接管。"
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
const dependenciesTarget = ref<SourceKey | "render" | null>(null);
const detecting = ref(false);
const busyDependency = ref("");

// 显示器是可选的：没有它照样能无头跑，不算「缺依赖」。
function dependencyProblem(key: SourceKey | "render"): boolean {
  const dependencies = key === "render" ? renderDependencies.value : (sourceState.value?.[key].dependencies ?? []);
  return dependencies.some((dep) => !dep.available && dep.name !== "display");
}

// 网页渲染就是插件页的「网页渲染」插件，这里读写的是同一份开关和设置。
const renderPluginID = "official.sandboxed-browser-renderer";
const renderWindowModeKey = "window_mode";
const renderPlugin = ref<PluginState | null>(null);
const renderDependencies = ref<ResolverDependency[]>([]);
const savingRender = ref(false);
const renderWindowSpec = computed(() => renderPlugin.value?.manifest.settings?.find((spec) => spec.key === renderWindowModeKey));
// 选项来自插件清单，补一句各自什么时候用：「显示窗口」只在排查渲染问题时有用。
const renderWindowHints: Record<string, string> = {
  auto: "有头但看不见，不容易被网站拦；没条件时退回无头",
  headless: "最省资源，但容易被网站认出来拦掉",
  visible: "在跑 Diana 的机器上弹出窗口，排查用"
};
const renderWindowOptions = computed<AppSelectOption[]>(() =>
  (renderWindowSpec.value?.options ?? []).map((option) => ({ ...option, hint: renderWindowHints[option.value] }))
);
const renderWindowMode = computed(() => String(renderPlugin.value?.settings?.[renderWindowModeKey] ?? renderWindowSpec.value?.default ?? "auto"));

async function loadRender(refreshDependencies = false): Promise<void> {
  try {
    const [plugins, dependencies] = await Promise.all([listPlugins(), listPluginDependencies(refreshDependencies)]);
    const state = plugins.find((plugin) => plugin.manifest.id === renderPluginID);
    renderPlugin.value = state ? pluginForBot(state, botID) : null;
    renderDependencies.value = dependencies.plugins[renderPluginID] ?? [];
  } catch {
    // 读不到只是这一行不显示状态，不打断这一页。
  }
}

async function toggleRender(enabled: boolean): Promise<void> {
  savingRender.value = true;
  try {
    renderPlugin.value = pluginForBot(await setPluginEnabled(renderPluginID, enabled, botID), botID);
  } catch (err) {
    toastError(err instanceof Error ? err.message : "切换网页渲染失败");
    await loadRender();
  } finally {
    savingRender.value = false;
  }
}

async function setRenderWindowMode(mode: string): Promise<void> {
  if (!renderPlugin.value) return;
  savingRender.value = true;
  try {
    const settings = { ...(renderPlugin.value.settings ?? {}), [renderWindowModeKey]: mode };
    renderPlugin.value = pluginForBot(await updatePluginSettings(renderPluginID, settings), botID);
  } catch (err) {
    toastError(err instanceof Error ? err.message : "保存网页渲染设置失败");
    await loadRender();
  } finally {
    savingRender.value = false;
  }
}

// 外接 CDP 地址按机器人存；默认值 127.0.0.1:9222 等于没配（和后端 defaultAgentBrowserCDPURL 一致）。
const defaultExternalCDPURL = "http://127.0.0.1:9222";
const agentBrowser = ref<AgentBrowserSettings | null>(null);
const externalCDPPanel = ref<HTMLElement | null>(null);
const externalCDPConfigured = computed(() => {
  const url = agentBrowser.value?.cdp_url?.trim() ?? "";
  return url !== "" && url !== defaultExternalCDPURL;
});

async function loadExternalCDP(): Promise<void> {
  if (!botID) {
    agentBrowser.value = null;
    return;
  }
  try {
    agentBrowser.value = await getAgentBrowser(botID);
  } catch {
    agentBrowser.value = null;
  }
}

async function openExternalCDPSettings(): Promise<void> {
  advancedOpen.value = true;
  await nextTick();
  externalCDPPanel.value?.scrollIntoView({ behavior: "smooth", block: "start" });
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
    // 先刷新探测缓存（网页渲染那一行顺带拿到新结果），来源状态里的依赖读的是同一份缓存。
    await loadRender(true);
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
const hasFrame = ref(false);
// 断线重连期间留着最后一帧，只在角上提示，不再整块换成「正在连接」。
const reconnecting = ref(false);
let frameMeta: LiveFrameMeta | null = null;
const screen = ref<HTMLCanvasElement | null>(null);
const addressInput = ref("");
const addressField = ref<HTMLInputElement | null>(null);
const currentTitle = ref("");
const pageLoading = ref(false);
const liveWindow = ref<HTMLElement | null>(null);
const fullscreen = ref(false);
const addressSecure = computed(() => /^https:\/\//i.test(addressInput.value.trim()));
const pageHost = computed(() => {
  try {
    return new URL(addressInput.value.trim()).host;
  } catch {
    return "";
  }
});
const liveHealthy = computed(() => hasFrame.value && !reconnecting.value);
const liveLabel = computed(() => (reconnecting.value ? "重新连接中…" : hasFrame.value ? "实时" : "连接中…"));

interface LivePage {
  url?: string;
  title?: string;
  loading?: boolean;
}

// 地址栏正在输入时不拿页面地址盖掉，打到一半被冲掉最让人恼火。
function applyPage(page: LivePage | undefined): void {
  if (!page) return;
  if (document.activeElement !== addressField.value) addressInput.value = page.url ?? "";
  currentTitle.value = page.title ?? "";
  pageLoading.value = Boolean(page.loading);
}

async function toggleFullscreen(): Promise<void> {
  try {
    if (document.fullscreenElement) await document.exitFullscreen();
    else await liveWindow.value?.requestFullscreen();
  } catch {
    // 浏览器不让全屏（比如嵌在不允许全屏的 iframe 里）就算了。
  }
}

function onFullscreenChange(): void {
  fullscreen.value = document.fullscreenElement !== null && document.fullscreenElement === liveWindow.value;
}
const saving = ref(false);
const busy = ref(false);
// 和后端 browserbox.TakeoverIdleTimeout 一致，只用来在页面上说清楚。
const takeoverIdleMinutes = 5;

let socket: WebSocket | null = null;
let statusTimer: number | undefined;
// 这一页在不在前台。页面被 KeepAlive 缓存着，切到别的页也不卸载；以前画面照样一直推，
// 浏览器照样每秒编几十帧 JPEG，白白拖慢机器人自己在用的那个浏览器。
let pageActive = false;
// 想不想连着画面：断线后要不要自动重连。
let wantLive = false;
let reconnectTimer: number | undefined;
// 断线重连从半秒起步，连续失败翻倍，最多 5 秒一次。
const liveReconnectMinMS = 500;
let reconnectDelayMS = liveReconnectMinMS;
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
    // 刚勾上就该马上起，不等上一次失败的冷却。
    if (patch.box_enabled) lastAutoStart = 0;
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
    // 连着画面时接管状态以画面连接的推送为准：轮询请求要是在交还之前发出、交还之后才
    // 回来，会把刚推过来的「已交还」又改回「你在操作」。
    const pushedTakeover = socket?.readyState === WebSocket.OPEN ? status.takeover : null;
    Object.assign(status, next);
    if (pushedTakeover !== null) status.takeover = pushedTakeover;
    Object.assign(settings, next.settings);
    if (next.running && !socket && botID && pageActive && !document.hidden) connectLive();
    if (!next.running && (socket || hasFrame.value)) disconnectLive();
    if (!next.running) void autoStart();
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
    if (status.running && botID && pageActive) connectLive();
    else disconnectLive();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "保存失败");
    await refresh();
  } finally {
    saving.value = false;
  }
}

// 勾上就该开着：选中机器人打开这一页时，没在跑就拉起来，好让你看到画面、在里面登录。
// 起不来时隔一分钟再试，别每次轮询都重启一遍、刷一堆失败记录。
let lastAutoStart = 0;
const autoStartRetryMS = 60_000;

async function autoStart(): Promise<void> {
  if (busy.value || !botID || !sourceState.value?.box.enabled || status.running) return;
  if (Date.now() - lastAutoStart < autoStartRetryMS) return;
  lastAutoStart = Date.now();
  busy.value = true;
  try {
    const result = await startBrowserBox(botID);
    Object.assign(status, result.status);
    if (pageActive) connectLive();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "内置浏览器没能启动");
  } finally {
    busy.value = false;
  }
}

// 画面默认只能看，接管要点按钮（和 OpenAI Operator、Cloudflare Browser Run 的 handoff 一样）：
// 以前点一下画面就算接管，点画面想让窗口获得焦点也会把浏览器从机器人手里抢走。
async function takeOver(): Promise<void> {
  busy.value = true;
  try {
    const result = await setBrowserBoxTakeover(botID, true);
    status.takeover = result.active;
    screen.value?.focus();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "接管失败");
  } finally {
    busy.value = false;
  }
}

// 没接管时点画面：让「接管」按钮闪一下，告诉人该点哪里。
const nudging = ref(false);
let nudgeTimer: number | undefined;
function nudgeTakeover(): void {
  nudging.value = false;
  if (nudgeTimer !== undefined) window.clearTimeout(nudgeTimer);
  requestAnimationFrame(() => {
    nudging.value = true;
    nudgeTimer = window.setTimeout(() => (nudging.value = false), 900);
  });
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
  wantLive = true;
  clearReconnectTimer();
  closeLiveSocket();
  const ws = new WebSocket(browserBoxLiveURL(botID));
  ws.binaryType = "arraybuffer";
  socket = ws;
  ws.onopen = () => {
    reconnectDelayMS = liveReconnectMinMS;
  };
  ws.onmessage = (event) => {
    if (event.data instanceof ArrayBuffer) {
      onFrame(ws, event.data);
      return;
    }
    const message = JSON.parse(event.data) as {
      type: string;
      tab?: { url?: string; title?: string };
      page?: LivePage;
      takeover?: boolean;
      active?: boolean;
      message?: string;
    };
    if (message.type === "ready") {
      applyPage(message.page ?? message.tab);
      status.takeover = Boolean(message.takeover);
      clearFirstFrameTimer();
      firstFrameTimer = window.setTimeout(() => {
        if (socket === ws && !hasFrame.value) {
          liveNotice.value = "画面 10 秒还没出来：这个页面可能卡住了（脚本卡死或渲染进程崩溃）。可以点「刷新」、在地址栏换个网址，或者重新连接。";
        }
      }, firstFrameTimeoutMS);
    } else if (message.type === "page") {
      applyPage(message.page);
    } else if (message.type === "takeover") {
      // 闲置自动交还、别的窗口点了交还，都从这里当场知道，不用等下一次轮询。
      status.takeover = Boolean(message.active);
    } else if (message.type === "error") {
      // 标签页卡死或崩溃时后端会说明原因再断开；换掉之前的画面，别让人对着最后一帧干等。
      clearFirstFrameTimer();
      hasFrame.value = false;
      liveNotice.value = message.message || "画面连接出错了";
    }
  };
  ws.onclose = () => {
    if (socket !== ws) return;
    socket = null;
    clearFirstFrameTimer();
    if (!wantLive) return;
    // 断了就自己重连，不等 5 秒一次的状态轮询；中间的代理掐掉长连接时，画面停在
    // 最后一帧，角上提示一下就好。
    reconnecting.value = hasFrame.value;
    if (!hasFrame.value && !liveNotice.value) liveNotice.value = "画面连接断开了，正在重连……";
    reconnectTimer = window.setTimeout(() => {
      reconnectTimer = undefined;
      if (wantLive && pageActive && status.running) connectLive();
    }, reconnectDelayMS);
    reconnectDelayMS = Math.min(reconnectDelayMS * 2, 5000);
  };
}

// 帧是异步解码的，两帧可能倒着解完；序号保证旧帧不会盖掉新帧。
let frameSeq = 0;
let paintedSeq = 0;
const frameTextDecoder = new TextDecoder();

/**
 * 解一帧二进制画面：4 字节大端的元数据长度、元数据 JSON、JPEG。画完才回 ack，
 * 后端收到 ack 才发下一帧——网慢的时候画面帧率跟着降，但永远是最新的一帧，
 * 不会在缓冲里排几秒。
 */
function onFrame(ws: WebSocket, buffer: ArrayBuffer): void {
  const metaLength = new DataView(buffer).getUint32(0);
  const meta = JSON.parse(frameTextDecoder.decode(new Uint8Array(buffer, 4, metaLength))) as LiveFrameMeta;
  const seq = ++frameSeq;
  createImageBitmap(new Blob([new Uint8Array(buffer, 4 + metaLength)], { type: "image/jpeg" }))
    .then((bitmap) => {
      if (socket === ws && seq > paintedSeq) {
        paintedSeq = seq;
        paintFrame(bitmap, meta);
      }
      bitmap.close();
    })
    .catch(() => undefined)
    .finally(() => {
      if (ws.readyState === WebSocket.OPEN) ws.send('{"type":"ack"}');
    });
}

function paintFrame(bitmap: ImageBitmap, meta: LiveFrameMeta): void {
  const canvas = screen.value;
  const context = canvas?.getContext("2d");
  if (!canvas || !context) return;
  if (canvas.width !== bitmap.width || canvas.height !== bitmap.height) {
    canvas.width = bitmap.width;
    canvas.height = bitmap.height;
  }
  context.drawImage(bitmap, 0, 0);
  frameMeta = meta;
  if (!hasFrame.value) hasFrame.value = true;
  if (reconnecting.value) reconnecting.value = false;
  if (liveNotice.value) liveNotice.value = "";
  clearFirstFrameTimer();
}

function clearReconnectTimer(): void {
  if (reconnectTimer !== undefined) window.clearTimeout(reconnectTimer);
  reconnectTimer = undefined;
}

function closeLiveSocket(): void {
  clearFirstFrameTimer();
  if (!socket) return;
  socket.onclose = null;
  socket.close();
  socket = null;
}

/** 断开画面但留着最后一帧：切回来时先看到旧画面，新帧到了再换。 */
function pauseLive(): void {
  wantLive = false;
  clearReconnectTimer();
  closeLiveSocket();
  reconnecting.value = false;
}

function disconnectLive(): void {
  pauseLive();
  hasFrame.value = false;
  frameMeta = null;
  liveNotice.value = "";
}

function reconnectLive(): void {
  liveNotice.value = "";
  reconnectDelayMS = liveReconnectMinMS;
  connectLive();
}

function send(payload: Record<string, unknown>): void {
  if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify(payload));
}

/** 把画面上的坐标换算成页面坐标：画面被 CSS 缩放过，点的位置得按比例还原。 */
function pagePoint(event: MouseEvent): { x: number; y: number } {
  const element = screen.value;
  if (!element || !frameMeta) return { x: 0, y: 0 };
  const rect = element.getBoundingClientRect();
  const scaleX = frameMeta.width / rect.width;
  const scaleY = frameMeta.height / rect.height;
  return { x: (event.clientX - rect.left) * scaleX, y: (event.clientY - rect.top) * scaleY };
}

function modifiers(event: MouseEvent | KeyboardEvent): number {
  return (event.altKey ? 1 : 0) | (event.ctrlKey ? 2 : 0) | (event.metaKey ? 4 : 0) | (event.shiftKey ? 8 : 0);
}

const mouseButtons = ["left", "middle", "right"];

function onMouse(event: MouseEvent, type: "mousePressed" | "mouseReleased"): void {
  event.preventDefault();
  if (!status.takeover) {
    if (type === "mousePressed") nudgeTakeover();
    return;
  }
  if (type === "mousePressed") screen.value?.focus();
  const point = pagePoint(event);
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
}

// 移动和滚轮按屏幕刷新合并，一帧最多发一条：不合并的话一次拖动能发出几百条，
// 后端逐条等浏览器处理完，画面反而更卡。
let pendingMove: MouseEvent | null = null;
let moveFrame = 0;
function onMouseMove(event: MouseEvent): void {
  // 没接管时鼠标只是路过，不发：后端同样会丢掉，这里省掉一路的消息。
  if (!status.takeover || !frameMeta) return;
  pendingMove = event;
  if (moveFrame) return;
  moveFrame = window.requestAnimationFrame(() => {
    moveFrame = 0;
    const latest = pendingMove;
    pendingMove = null;
    if (!latest) return;
    const point = pagePoint(latest);
    send({ type: "mouse", mouse: { type: "mouseMoved", x: point.x, y: point.y, buttons: latest.buttons, modifiers: modifiers(latest) } });
  });
}

let wheelDeltaX = 0;
let wheelDeltaY = 0;
let wheelEvent: WheelEvent | null = null;
let wheelFrame = 0;
// 没接管时滚轮归 WebUI：鼠标停在画面上照样能上下滚这一页，不会把浏览器抢过来。
function onWheel(event: WheelEvent): void {
  if (!status.takeover) return;
  event.preventDefault();
  wheelDeltaX += event.deltaX;
  wheelDeltaY += event.deltaY;
  wheelEvent = event;
  if (wheelFrame) return;
  wheelFrame = window.requestAnimationFrame(() => {
    wheelFrame = 0;
    const latest = wheelEvent;
    const deltaX = wheelDeltaX;
    const deltaY = wheelDeltaY;
    wheelDeltaX = 0;
    wheelDeltaY = 0;
    wheelEvent = null;
    if (!latest) return;
    const point = pagePoint(latest);
    send({
      type: "mouse",
      mouse: { type: "mouseWheel", x: point.x, y: point.y, delta_x: -deltaX, delta_y: -deltaY, modifiers: modifiers(latest) }
    });
  });
}

function onKey(event: KeyboardEvent, type: "keyDown" | "keyUp"): void {
  // 没接管时按键不碰也不拦：焦点留在画面上时 Cmd+Tab、Cmd+C、Tab 照常归 WebUI，
  // 也不会因此把浏览器抢过来。
  if (!status.takeover) return;
  event.preventDefault();
  // 可打印字符走 insertText：中文输入法上屏的是整段文字，不是一串按键。
  if (type === "keyDown" && event.key.length === 1 && !event.ctrlKey && !event.metaKey) {
    send({ type: "text", text: event.key });
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
}

// 输入法选字按的回车不算提交：以前带中文的网址会因此连开两次。
function onAddressEnter(event: KeyboardEvent): void {
  if (event.isComposing || event.keyCode === 229 || !status.takeover) return;
  event.preventDefault();
  navigate();
  // 打开之后把焦点还给页面，地址栏才会跟着跳转后的真实地址变。
  addressField.value?.blur();
}

function navigate(): void {
  const target = addressInput.value.trim();
  if (!target) return;
  send({ type: "navigate", url: /^https?:\/\//i.test(target) ? target : `https://${target}` });
}

function onVisibilityChange(): void {
  if (!pageActive) return;
  if (document.hidden) pauseLive();
  else void refresh();
}

function startPage(): void {
  pageActive = true;
  // 先读来源再读状态：要知道勾没勾上，才能决定要不要自动拉起。
  void loadSource().then(refresh);
  void loadActivity();
  void loadRender();
  void loadExternalCDP();
  // 扩展连上、断开或被接管都会改变「这一轮用哪个」，跟着状态一起刷。
  statusTimer = window.setInterval(() => {
    void refresh();
    void loadSource();
    void loadActivity();
  }, 5000);
}

function stopPage(): void {
  pageActive = false;
  if (statusTimer) window.clearInterval(statusTimer);
  statusTimer = undefined;
  pauseLive();
}

onMounted(() => {
  document.addEventListener("visibilitychange", onVisibilityChange);
  document.addEventListener("fullscreenchange", onFullscreenChange);
  startPage();
});

// 页面被 KeepAlive 缓存着：切走时停掉画面和轮询，切回来再接上。
onActivated(() => {
  if (!pageActive) startPage();
});

onDeactivated(stopPage);

onBeforeUnmount(() => {
  document.removeEventListener("visibilitychange", onVisibilityChange);
  document.removeEventListener("fullscreenchange", onFullscreenChange);
  stopPage();
  if (moveFrame) window.cancelAnimationFrame(moveFrame);
  if (wheelFrame) window.cancelAnimationFrame(wheelFrame);
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

/* 两组之间靠小标题分开：读网页、出图 / 登录、点按钮。 */
.browser-group-title {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 4px 10px;
  padding-bottom: 6px;
  border-bottom: 1px solid var(--border);
}

.browser-group-title:not(:first-child) {
  margin-top: 10px;
}

.browser-group-title h3 {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
}

.browser-group-title span {
  font-size: 12.5px;
  color: var(--muted);
}

/* 没有勾选框的行（外接 CDP）用它占住勾选框那一列，标题照样对齐。 */
.browser-toggle-spacer {
  width: 16px;
}

.browser-toggle-label {
  font-weight: 600;
}

.browser-inline-select {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--muted);
  white-space: nowrap;
}

/* 这一行都是小字链接，下拉跟着缩小，样式仍是全站统一的 AppSelect。 */
.browser-inline-select :deep(.app-select-trigger) {
  padding: 3px 10px;
  font-size: 12.5px;
  color: var(--text);
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

.browser-toggle-error {
  margin: 2px 0 0;
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

/* 画面卡片本身不留内边距，窗口贴着卡片边：标签、工具栏、画面、状态栏是一整块。 */
.browser-live-card {
  overflow: hidden;
}

.browser-window {
  position: relative;
  display: flex;
  flex-direction: column;
  background: var(--surface);
  border-radius: inherit;
}

/* 你在操作时整块描一圈主题色：一眼能看出这会儿浏览器在谁手里。描边叠在最上面一层，
   不然会被标签栏和画面的底色盖住。 */
.browser-window::after {
  content: "";
  position: absolute;
  inset: 0;
  z-index: 2;
  border: 2px solid transparent;
  border-radius: inherit;
  pointer-events: none;
  transition: border-color 0.15s ease;
}

.browser-window.is-takeover::after {
  border-color: var(--accent);
}

.browser-tabbar {
  display: flex;
  align-items: flex-end;
  padding: 8px 12px 0;
  background: var(--surface-2);
}

.browser-tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: min(320px, 100%);
  padding: 7px 14px;
  border-radius: 10px 10px 0 0;
  background: var(--surface);
  color: var(--text);
  font-size: 12.5px;
}

.browser-tab > svg {
  flex: 0 0 auto;
  color: var(--muted);
}

.browser-tab-title {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}

.browser-toolbar {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 8px 10px;
}

.browser-tool {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 auto;
  width: 32px;
  height: 32px;
  padding: 0;
  border: 0;
  border-radius: 999px;
  background: transparent;
  color: var(--text-secondary);
  cursor: pointer;
}

.browser-tool:hover:not(:disabled) {
  background: var(--surface-2);
  color: var(--text);
}

.browser-tool:disabled {
  opacity: 0.4;
  cursor: default;
}

.browser-address {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 0;
  height: 34px;
  margin: 0 4px;
  padding: 0 14px;
  border: 1px solid transparent;
  border-radius: 999px;
  background: var(--surface-2);
  cursor: text;
}

.browser-address.readonly {
  cursor: default;
}

.browser-address.readonly input {
  color: var(--text-secondary);
}

.browser-address:not(.readonly):focus-within {
  border-color: var(--accent);
  background: var(--surface);
}

.browser-address-icon {
  flex: 0 0 auto;
  color: var(--muted);
}

.browser-address input {
  flex: 1;
  min-width: 0;
  padding: 0;
  border: 0;
  outline: none;
  background: transparent;
  color: var(--text);
  font: inherit;
  font-size: 13px;
}

/* 加载进度：页面在加载时一条细线来回走，停了就消失。 */
.browser-progress {
  position: relative;
  height: 2px;
  overflow: hidden;
}

.browser-progress.active::after {
  content: "";
  position: absolute;
  inset: 0 auto 0 0;
  width: 35%;
  background: var(--accent);
  animation: browser-progress 1.1s ease-in-out infinite;
}

@keyframes browser-progress {
  from {
    transform: translateX(-100%);
  }
  to {
    transform: translateX(300%);
  }
}

.browser-stage {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  border-top: 1px solid var(--border);
  border-bottom: 1px solid var(--border);
  background: var(--surface-2);
  overflow: hidden;
}

/* 只有还没画面时撑出一块地方放提示；有画面就按画面本身的比例，手机上不留上下空带。 */
.browser-stage.empty {
  min-height: 240px;
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

.browser-statusbar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px 10px;
  min-height: 44px;
  padding: 6px 14px;
  font-size: 12.5px;
}

.browser-status-dot {
  width: 8px;
  height: 8px;
  border-radius: 999px;
  background: var(--ok);
}

.browser-window.is-takeover .browser-status-dot {
  background: var(--accent);
}

.browser-statusbar strong {
  font-weight: 600;
}

.browser-status-hint {
  color: var(--muted);
}

.browser-nudge {
  animation: browser-nudge 0.9s ease;
}

@keyframes browser-nudge {
  0%,
  100% {
    transform: none;
    box-shadow: none;
  }
  20%,
  60% {
    transform: scale(1.08);
    box-shadow: 0 0 0 4px var(--accent-soft);
  }
}

.browser-status-live {
  margin-left: auto;
  color: var(--muted);
  font-size: 12px;
}

.browser-status-live.warn {
  color: var(--warn);
}

/* 全屏时画面按比例塞满剩下的高度。 */
.browser-window.is-fullscreen {
  height: 100vh;
  border-radius: 0;
}

.browser-window.is-fullscreen .browser-stage {
  flex: 1;
  min-height: 0;
  border-bottom: 0;
}

.browser-window.is-fullscreen .browser-screen {
  width: auto;
  height: auto;
  max-width: 100%;
  max-height: 100%;
}

</style>
