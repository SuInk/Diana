<!-- Copyright (c) 2025-now SuInk. Licensed under the Limited Redistribution License. -->
<!--
  浏览器控制扩展这一档的配置面板。它原来长在「设置 → 浏览器控制」里，和另外两档
  浏览器（一次性无头渲染、内置常驻浏览器）各在一处，用户要回答「我该在哪开」得先
  知道自己用的是哪一档。现在收在「浏览器」页底部的「高级」里，这块单独成组件只是为了不把那
  一页堆成两千行。
-->
<template>
  <div class="stack">
    <section class="card">
      <div class="card-header">
        <h2>浏览器控制扩展</h2>
        <span class="badge" :class="browserPolicy.enabled ? (browserReady ? 'ok' : 'warn') : 'warn'">
          {{ browserPolicy.enabled ? (browserReady ? "可用" : "已启用，等扩展连接") : "未启用" }}
        </span>
        <button class="btn small ghost" type="button" :disabled="browserLoading" title="刷新" aria-label="刷新浏览器控制状态" @click="loadBrowserControl">
          <RefreshCw :size="14" aria-hidden="true" />
        </button>
        <span class="card-sub">机器人操作的是你自己浏览器里的页面，带着你的登录态</span>
      </div>
      <div class="card-body stack">
        <p class="muted" style="margin: 0; font-size: 13px">
          装在浏览器里的扩展反向连到这里，只能操作下面列出的站点。默认只读；点击、输入和导航要单独打开。
          任何时候你都可以在扩展或这一页按下接管，机器人立刻停手。还需要在对应机器人的 Agent 设置里单独打开这一档。
        </p>
        <p v-if="browserExtensionDownload" class="muted" style="margin: 0; font-size: 12.5px">
          还没装扩展？
          <a href="/api/browser-control/extension.zip" download>下载扩展源码包</a>
          ，解压后在 <code class="mono">chrome://extensions</code> 开启开发者模式、「加载已解压的扩展程序」选那个目录。
        </p>

        <div class="field">
          <label class="switch-row">
            <input v-model="browserPolicy.enabled" type="checkbox" />
            <span>启用浏览器控制（关闭会当场断开所有已连接的扩展）</span>
          </label>
          <label class="switch-row">
            <input v-model="browserPolicy.write_enabled" type="checkbox" :disabled="!browserPolicy.enabled" />
            <span>允许写操作：点击、输入、导航。关闭时只能读取页面</span>
          </label>
        </div>

        <div class="field">
          <label for="browser-origins">允许的来源（每行一条）</label>
          <textarea
            id="browser-origins"
            v-model="browserOriginsText"
            class="input"
            rows="2"
            placeholder="chrome-extension://abcdefghijklmnopabcdefghijklmnop"
          ></textarea>
          <p class="muted" style="margin: 0; font-size: 12.5px">
            填扩展选项页上显示的扩展 ID，写成 <code class="mono">chrome-extension://&lt;扩展 ID&gt;</code>。留空时谁都连不上。
          </p>
        </div>

        <div class="field">
          <label for="browser-allowed-hosts">可操作站点（每行一条）</label>
          <textarea
            id="browser-allowed-hosts"
            v-model="browserAllowedHostsText"
            class="input"
            rows="3"
            placeholder="example.com&#10;*.wiki.example.com"
          ></textarea>
          <p class="muted" style="margin: 0; font-size: 12.5px">
            <code class="mono">example.com</code> 只匹配这一个主机名；<code class="mono">*.example.com</code> 匹配子域但不含主域本身，
            两个都要就写两行。留空时一个站点都不允许。
          </p>
        </div>

        <div class="field">
          <label for="browser-denied-hosts">排除的站点（每行一条，优先于上面）</label>
          <textarea
            id="browser-denied-hosts"
            v-model="browserDeniedHostsText"
            class="input"
            rows="2"
            placeholder="admin.example.com"
          ></textarea>
        </div>

        <div class="cluster" style="gap: 8px; flex-wrap: wrap">
          <div class="field" style="max-width: 200px">
            <label for="browser-timeout">单条指令超时（毫秒）</label>
            <input id="browser-timeout" v-model.number="browserPolicy.command_timeout_ms" class="input" type="number" min="1000" max="120000" />
          </div>
          <div class="field" style="max-width: 200px">
            <label for="browser-rate">每分钟指令上限</label>
            <input id="browser-rate" v-model.number="browserPolicy.commands_per_minute" class="input" type="number" min="1" max="600" />
          </div>
        </div>

        <div class="cluster" style="gap: 8px">
          <button class="btn primary" type="button" :disabled="browserSaving" @click="saveBrowserPolicy">
            <Save :size="14" aria-hidden="true" />
            {{ browserSaving ? "保存中…" : "保存策略" }}
          </button>
        </div>
      </div>
    </section>

    <section class="card">
      <div class="card-header">
        <h2>控制令牌</h2>
        <span class="card-sub">扩展用它连接，只显示一次</span>
      </div>
      <div class="card-body stack">
        <div v-if="browserCreatedToken" class="openapi-token">
          <p class="openapi-token-hint">令牌只显示这一次，请立即复制并填进扩展选项页：</p>
          <div class="cluster" style="gap: 8px; flex-wrap: wrap">
            <code class="mono openapi-token-value">{{ browserCreatedToken }}</code>
            <button class="btn small" type="button" @click="copyBrowserToken">复制</button>
            <button class="btn small ghost" type="button" @click="browserCreatedToken = ''">我已保存</button>
          </div>
        </div>
        <LoadingSkeleton v-if="browserLoading && browserTokens.length === 0" kind="sessions" :count="2" label="正在加载令牌" />
        <p v-else-if="browserTokens.length === 0" class="muted" style="margin: 0; font-size: 13px">还没有令牌。签发后扩展才能连上来。</p>
        <ul v-else class="session-list">
          <li v-for="token in browserTokens" :key="token.id" class="session-item">
            <div class="session-main">
              <span class="session-name">{{ token.name }}</span>
              <span class="session-meta mono">{{ token.prefix }}…</span>
              <span class="session-meta">
                创建于 {{ formatTime(token.created_at) }}
                · {{ token.last_used_at ? `最近使用 ${formatTime(token.last_used_at)}` : "从未使用" }}
                · {{ token.extension_id ? `已绑定扩展 ${token.extension_id}` : "尚未绑定扩展" }}
              </span>
            </div>
            <button class="btn small danger" type="button" :disabled="browserRevokingID !== ''" @click="revokeBrowserToken(token)">
              {{ browserRevokingID === token.id ? "处理中…" : "吊销" }}
            </button>
          </li>
        </ul>
        <div class="cluster" style="gap: 8px">
          <input
            v-model="browserNewTokenName"
            class="input"
            placeholder="这台浏览器的用途，例如 公司台式机 Chrome"
            style="max-width: 280px"
            @keyup.enter="createBrowserToken"
          />
          <button class="btn primary" type="button" :disabled="browserCreating || browserNewTokenName.trim().length === 0" @click="createBrowserToken">
            <KeyRound :size="15" aria-hidden="true" />
            {{ browserCreating ? "签发中…" : "签发令牌" }}
          </button>
        </div>
      </div>
    </section>

    <section class="card">
      <div class="card-header">
        <h2>已连接的浏览器</h2>
        <span class="card-sub">接管打开时机器人一条指令都不会下发</span>
      </div>
      <div class="card-body stack">
        <p v-if="browserConnections.length === 0" class="muted" style="margin: 0; font-size: 13px">
          还没有扩展连上来。装好扩展、填上地址与令牌之后会自动出现在这里。
        </p>
        <ul v-else class="session-list">
          <li v-for="conn in browserConnections" :key="conn.id" class="session-item">
            <div class="session-main">
              <span class="session-name">
                {{ conn.label || conn.browser || "浏览器" }}
                <span v-if="conn.takeover" class="badge warn">人工接管中</span>
              </span>
              <span class="session-meta mono">{{ conn.extension_id }}</span>
              <span class="session-meta">
                连接于 {{ formatTime(conn.connected_at) }}
                · 可操作标签页 {{ conn.allowed_tabs }} 个
                · 已下发 {{ conn.commands }} 条指令
                <template v-if="conn.takeover_reason"> · {{ conn.takeover_reason }}</template>
              </span>
            </div>
            <div class="cluster" style="gap: 6px">
              <button class="btn small" type="button" @click="toggleBrowserTakeover(conn)">
                {{ conn.takeover ? "交还控制权" : "人工接管" }}
              </button>
              <button class="btn small danger" type="button" @click="disconnectBrowser(conn)">断开</button>
            </div>
          </li>
        </ul>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from "vue";
import { KeyRound, RefreshCw, Save } from "@lucide/vue";
import LoadingSkeleton from "./LoadingSkeleton.vue";
import {
  getBrowserControlStatus,
  saveBrowserControlPolicy,
  createBrowserControlToken,
  revokeBrowserControlToken,
  setBrowserControlTakeover,
  disconnectBrowserControl,
  type BrowserControlConnection,
  type BrowserControlPolicy,
  type BrowserControlToken
} from "../api";
import { askConfirm } from "../confirm";
import { formatTime } from "../format";
import { toastError, toastSuccess } from "../toast";

const browserPolicy = ref<BrowserControlPolicy>({ enabled: false, write_enabled: false, command_timeout_ms: 20000, commands_per_minute: 60 });
const browserTokens = ref<BrowserControlToken[]>([]);
const browserConnections = ref<BrowserControlConnection[]>([]);
const browserReady = ref(false);
const browserExtensionDownload = ref(false);
const browserLoading = ref(true);
const browserSaving = ref(false);
const browserCreating = ref(false);
const browserRevokingID = ref("");
const browserNewTokenName = ref("");
const browserCreatedToken = ref("");
// 三个站点列表在界面上是多行文本，保存时才拆成数组：让用户一行一条地贴，
// 比逗号分隔好改，也不会因为多打一个逗号多出一条空白规则。
const browserOriginsText = ref("");
const browserAllowedHostsText = ref("");
const browserDeniedHostsText = ref("");

// 三个站点列表在界面上是多行文本，保存时才拆成数组。
function linesToList(value: string): string[] {
  return value
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line.length > 0);
}

async function loadBrowserControl(): Promise<void> {
  browserLoading.value = true;
  try {
    const status = await getBrowserControlStatus();
    browserPolicy.value = status.policy;
    browserTokens.value = status.tokens ?? [];
    browserConnections.value = status.connections ?? [];
    browserReady.value = status.ready;
    browserExtensionDownload.value = status.extension_download ?? false;
    browserOriginsText.value = (status.policy.allowed_origins ?? []).join("\n");
    browserAllowedHostsText.value = (status.policy.allowed_hosts ?? []).join("\n");
    browserDeniedHostsText.value = (status.policy.denied_hosts ?? []).join("\n");
  } catch (err) {
    toastError(err instanceof Error ? err.message : "读取浏览器控制状态失败");
  } finally {
    browserLoading.value = false;
  }
}

async function saveBrowserPolicy(): Promise<void> {
  if (browserSaving.value) return;
  const policy: BrowserControlPolicy = {
    ...browserPolicy.value,
    allowed_origins: linesToList(browserOriginsText.value),
    allowed_hosts: linesToList(browserAllowedHostsText.value),
    denied_hosts: linesToList(browserDeniedHostsText.value)
  };
  if (policy.enabled && (policy.allowed_hosts?.length ?? 0) === 0) {
    // 这不是错误配置，但它的效果是「启用了却一个站点都碰不到」，先说清楚再保存。
    if (!(await askConfirm({
      title: "没有授权任何站点",
      message: "站点白名单是空的，扩展连上来也读不了任何页面。确定就这样保存吗？",
      confirmLabel: "保存"
    }))) {
      return;
    }
  }
  browserSaving.value = true;
  try {
    const saved = await saveBrowserControlPolicy(policy);
    toastSuccess("浏览器控制策略已保存");
    browserPolicy.value = saved.policy;
    await loadBrowserControl();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "保存浏览器控制策略失败");
  } finally {
    browserSaving.value = false;
  }
}

async function createBrowserToken(): Promise<void> {
  const name = browserNewTokenName.value.trim();
  if (name.length === 0 || browserCreating.value) return;
  browserCreating.value = true;
  try {
    const result = await createBrowserControlToken(name);
    // 明文只在这次响应里出现，摆在页面上等用户复制，刷新即消失。
    browserCreatedToken.value = result.plaintext;
    browserNewTokenName.value = "";
    await loadBrowserControl();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "签发令牌失败");
  } finally {
    browserCreating.value = false;
  }
}

async function copyBrowserToken(): Promise<void> {
  try {
    await navigator.clipboard.writeText(browserCreatedToken.value);
    toastSuccess("令牌已复制");
  } catch {
    toastError("复制失败，请手动选中复制");
  }
}

async function revokeBrowserToken(token: BrowserControlToken): Promise<void> {
  if (!(await askConfirm({
    title: "吊销控制令牌",
    message: `吊销「${token.name}」后，用它连着的浏览器会立即断开，需要重新签发才能再连。`,
    confirmLabel: "吊销",
    danger: true
  }))) {
    return;
  }
  browserRevokingID.value = token.id;
  try {
    await revokeBrowserControlToken(token.id);
    toastSuccess("令牌已吊销");
    await loadBrowserControl();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "吊销令牌失败");
  } finally {
    browserRevokingID.value = "";
  }
}

async function toggleBrowserTakeover(conn: BrowserControlConnection): Promise<void> {
  try {
    await setBrowserControlTakeover(conn.id, !conn.takeover, conn.takeover ? "" : "从 WebUI 接管");
    await loadBrowserControl();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "切换接管状态失败");
  }
}

async function disconnectBrowser(conn: BrowserControlConnection): Promise<void> {
  if (!(await askConfirm({
    title: "断开浏览器连接",
    message: "断开后扩展会自动重连；要彻底停掉请关闭总开关或吊销令牌。",
    confirmLabel: "断开",
    danger: true
  }))) {
    return;
  }
  try {
    await disconnectBrowserControl(conn.id);
    await loadBrowserControl();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "断开连接失败");
  }
}

onMounted(() => {
  void loadBrowserControl();
});
</script>
