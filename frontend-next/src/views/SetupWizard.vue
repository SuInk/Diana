<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h1>配置向导</h1>
        <p>三步跑通：配置模型 → 接入 OneBot v11 → 启动验证</p>
      </div>
    </header>

    <div class="wizard-steps">
      <button
        v-for="(label, index) in stepLabels"
        :key="label"
        class="wizard-step"
        :class="stepClass(index)"
        type="button"
        :aria-current="step === index ? 'step' : undefined"
        @click="step = index"
      >
        <span class="step-index">
          <CheckCircle2 v-if="stepDone(index)" :size="13" aria-hidden="true" />
          <template v-else>{{ index + 1 }}</template>
        </span>
        <span>{{ label }}</span>
      </button>
    </div>

    <!-- 第 1 步：LLM -->
    <section v-if="step === 0" class="card">
      <div class="card-header">
        <h2>配置 LLM Provider</h2>
        <span v-if="llmConfigured" class="badge ok">已配置</span>
      </div>
      <div class="card-body stack">
        <div class="form-grid">
          <div class="field">
            <label for="wizard-service">服务平台</label>
            <AppSelect
              id="wizard-service"
              :model-value="selectedService"
              :options="serviceOptions"
              @update:model-value="applyServicePreset"
            />
          </div>
          <div class="field">
            <label for="wizard-api-style">接口模式</label>
            <AppSelect
              id="wizard-api-style"
              v-model="llmForm.api_style"
              :options="[
                { value: 'chat_completions', label: 'Chat Completions' },
                { value: 'responses', label: 'Responses API' }
              ]"
            />
            <span class="hint">平台预设会选择推荐模式，也可按服务实际支持情况切换。</span>
          </div>
          <div class="field wide">
            <label for="wizard-baseurl">API 地址</label>
            <input id="wizard-baseurl" v-model="llmForm.base_url" class="input mono" placeholder="https://api.example.com/v1" />
            <span class="hint">{{ selectedPreset?.hint }}；请填写完整 API 根地址，包括服务要求的 `/v1` 等路径。</span>
          </div>
          <div class="field wide">
            <label for="wizard-apikey">API Key</label>
            <input
              id="wizard-apikey"
              v-model="llmForm.api_key"
              class="input"
              type="password"
              :placeholder="llmConfigured ? '留空表示沿用已保存的 Key' : '粘贴你的 API Key'"
              autocomplete="off"
            />
            <span class="hint">Key 只保存在本机 SQLite，不会上传到其他服务。</span>
          </div>
          <div class="field wide model-config-field">
            <div class="model-sync-row">
              <div class="model-sync-copy">
                <span class="model-sync-title">模型列表</span>
                <span v-if="modelOptions.length > 0" class="hint">已同步 {{ modelOptions.length }} 个可用模型。</span>
                <span v-else class="hint">填写 API Key 后，从服务同步可用模型。</span>
              </div>
              <button class="btn" type="button" :disabled="modelsLoading" @click="loadModels(false)">
                <RefreshCw :size="14" aria-hidden="true" />
                {{ modelsLoading ? "同步中…" : "同步模型列表" }}
              </button>
            </div>
            <details v-if="modelsError" class="request-error" open>
              <summary>模型列表获取失败，查看完整错误</summary>
              <pre>{{ modelsError }}</pre>
            </details>
            <div class="model-default-field">
              <label for="wizard-model">默认模型</label>
              <div class="model-picker-anchor">
                <input
                  id="wizard-model"
                  v-model="llmForm.model"
                  class="input"
                  placeholder="填写默认模型 ID，或从已同步列表中选择"
                  autocomplete="off"
                  @focus="openModelPicker"
                  @input="openModelPicker"
                  @keydown.esc.stop="modelPickerOpen = false"
                />
                <div v-if="modelPickerOpen && modelOptions.length > 0" class="model-picker">
                  <div class="model-picker-meta">
                    <span>
                      共 {{ modelOptions.length }} 个模型<template v-if="llmForm.model.trim() && filteredModels.length < modelOptions.length"
                        >，匹配 {{ filteredModels.length }} 个</template
                      >
                    </span>
                    <button class="btn ghost small" type="button" @click="modelPickerOpen = false">收起</button>
                  </div>
                  <p v-if="llmForm.model.trim() && filteredModels.length === 0" class="model-picker-empty">
                    没有包含「{{ llmForm.model.trim() }}」的模型，已显示全部
                  </p>
                  <ul class="model-picker-list">
                    <li v-for="model in displayModels" :key="model.id">
                      <button
                        type="button"
                        class="model-picker-item"
                        :class="{ active: model.id === llmForm.model }"
                        @mousedown.prevent="pickModel(model.id)"
                      >
                        {{ model.id }}
                      </button>
                    </li>
                  </ul>
                </div>
              </div>
              <span v-if="modelOptions.length > 0" class="hint">输入可筛选同步结果；留空保存时采用列表第一项。</span>
              <span v-else class="hint">也可以直接填写服务支持的模型 ID。</span>
            </div>
          </div>
          <div class="field wide">
            <label for="wizard-test-message">测试内容</label>
            <input
              id="wizard-test-message"
              v-model="llmTestMessage"
              class="input"
              placeholder="hi"
              autocomplete="off"
            />
            <span class="hint">保存后会立即发送这条消息；测试成功后才能完成此步骤。</span>
          </div>
        </div>
        <div class="cluster">
          <button class="btn primary" type="button" :disabled="busy || !llmTestMessage.trim()" @click="saveAndTestLLM">
            <Zap :size="15" aria-hidden="true" />
            保存并测试连通
          </button>
          <span v-if="llmTestResult" class="badge ok">{{ llmTestResult }}</span>
        </div>
      </div>
    </section>

    <!-- 第 2 步：OneBot v11 -->
    <section v-else-if="step === 1" class="card">
      <div class="card-header">
        <h2>接入 OneBot v11</h2>
        <span v-if="connected" class="badge ok">已连接</span>
      </div>
      <div class="card-body stack">
        <div class="form-grid">
          <div class="field wide">
            <label for="wizard-onebot-endpoint">OneBot v11 回连地址</label>
            <div class="input-group">
              <input
                id="wizard-onebot-endpoint"
                v-model="botForm.onebot_reverse_ws_endpoint"
                class="input mono"
                placeholder="ws://127.0.0.1:18080/onebot/v11/ws"
                autocomplete="off"
              />
              <button class="btn icon-only" type="button" aria-label="复制地址" @click="copyEndpoint">
                <Copy :size="14" aria-hidden="true" />
              </button>
            </div>
            <span class="hint">填写 OneBot v11 客户端实际能访问的地址；Docker 或局域网部署时请修改主机名。自定义路径需要反向代理转发到 /onebot/v11/ws。</span>
          </div>
          <div class="field">
            <label for="wizard-owner">主人 QQ 号（可选）</label>
            <input
              id="wizard-owner"
              v-model="botForm.owner_id"
              class="input"
              inputmode="numeric"
              placeholder="例如 123456789，用于管理指令和私聊登录"
            />
            <span class="hint">不需要聊天内管理或 QQ 配对登录时可以留空。</span>
          </div>
          <div class="field wide">
            <label for="wizard-token">OneBot Access Token（可选，至少 16 位）</label>
            <input id="wizard-token" v-model="botForm.onebot_access_token" class="input" type="password" autocomplete="off"
              :placeholder="tokenConfigured ? '留空表示沿用已保存 token' : '与 OneBot v11 客户端填写的 token 保持一致'" />
          </div>
        </div>
        <div class="cluster">
          <button class="btn primary" type="button" :disabled="busy" @click="saveBotAndStart">
            <Power :size="15" aria-hidden="true" />
            保存并启动等待连接
          </button>
          <span class="badge" :class="connected ? 'ok' : 'warn'">
            <span class="status-dot" :class="{ pulse: !connected }" aria-hidden="true" />
            {{ connected ? `OneBot v11 已连接 ${selfID}` : "等待 OneBot v11 客户端连入…" }}
          </span>
        </div>
        <p v-if="channelError" class="text-err" style="font-size: 12.5px">{{ channelError }}</p>
      </div>
    </section>

    <!-- 第 3 步：完成 -->
    <section v-else class="card">
      <div class="card-header">
        <h2>完成验证</h2>
      </div>
      <div class="card-body stack">
        <div class="checklist">
          <div class="checklist-item" :class="llmConfigured ? 'done' : 'todo'">
            <span class="check-icon"><CheckCircle2 :size="15" aria-hidden="true" /></span>
            <span class="check-main">LLM 已配置<div class="check-hint">{{ llmSummary }}</div></span>
          </div>
          <div class="checklist-item" :class="connected ? 'done' : 'todo'">
            <span class="check-icon"><CheckCircle2 :size="15" aria-hidden="true" /></span>
            <span class="check-main">OneBot v11 已连接<div class="check-hint">{{ connected ? `账号 ${selfID}` : "尚未连接" }}</div></span>
          </div>
        </div>
        <p class="muted">
          现在给机器人发一条私聊消息，或在群里 @ 它试试。群聊触发词默认为
          <code>嘉然</code>、<code>然然</code>、<code>Diana</code>。
        </p>
        <div class="cluster">
          <button class="btn primary" type="button" @click="finishSetup">
            <LayoutGrid :size="15" aria-hidden="true" />
            进入总览
          </button>
          <button class="btn" type="button" @click="navigate('llm')">
            <MessageCircle :size="15" aria-hidden="true" />
            去 LLM 配置再测一次
          </button>
        </div>
      </div>
    </section>

    <div class="wizard-nav">
      <button class="btn wizard-nav-button" type="button" :disabled="step === 0" @click="step = Math.max(0, step - 1)">
        <ChevronLeft :size="15" aria-hidden="true" />
        上一步
      </button>
      <button v-if="step < 2" class="btn wizard-nav-button" type="button" @click="step = step + 1">
        跳过此步
        <ChevronRight :size="15" aria-hidden="true" />
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { CheckCircle2, ChevronLeft, ChevronRight, Copy, LayoutGrid, MessageCircle, Power, RefreshCw, Zap } from "@lucide/vue";
import {
  getConfig,
  getQQBotConfig,
  listLLMModels,
  saveConfig,
  saveQQBotConfig,
  startQQBot,
  testLLM,
  type LLMConfig,
  type LLMModelInfo,
  type Provider,
  type QQBotConfig
} from "../api";
import { stream } from "../stream";
import { navigate } from "../router";
import { toastError, toastSuccess } from "../toast";
import AppSelect from "../components/AppSelect.vue";
import { detectLLMService, llmServicePresets } from "../llm-presets";

const step = ref(0);
const busy = ref(false);
const stepLabels = ["配置 LLM", "接入 OneBot v11", "启动验证"];
const SETUP_COMPLETE_KEY = "dqb-next:setup-completed";

function finishSetup(): void {
  window.localStorage.setItem(SETUP_COMPLETE_KEY, "1");
  navigate("dashboard");
}

const llmConfigured = ref(false);
const tokenConfigured = ref(false);
const llmTestResult = ref("");
const llmTestMessage = ref("hi");
const selectedService = ref("openai");
const savedLLM = ref<LLMConfig | null>(null);
const savedBot = ref<QQBotConfig | null>(null);
const modelOptions = ref<LLMModelInfo[]>([]);
const modelsLoading = ref(false);
const modelsError = ref("");
const modelPickerOpen = ref(false);

const llmForm = ref<{ provider: Provider; api_style: "responses" | "chat_completions"; model: string; base_url: string; api_key: string }>({
  provider: "openai_compatible",
  api_style: "responses",
  model: "",
  base_url: "https://api.openai.com/v1",
  api_key: ""
});

const serviceOptions = llmServicePresets.map((preset) => ({
  value: preset.id,
  label: preset.label,
  hint: preset.hint
}));
const selectedPreset = computed(() => llmServicePresets.find((preset) => preset.id === selectedService.value));
const filteredModels = computed(() => {
  const keyword = llmForm.value.model.trim().toLowerCase();
  if (!keyword || modelOptions.value.some((model) => model.id.toLowerCase() === keyword)) {
    return modelOptions.value;
  }
  return modelOptions.value.filter((model) => model.id.toLowerCase().includes(keyword));
});
const displayModels = computed(() => filteredModels.value.length > 0 ? filteredModels.value : modelOptions.value);

function applyServicePreset(id: string): void {
  const preset = llmServicePresets.find((item) => item.id === id);
  if (!preset) return;
  selectedService.value = id;
  llmForm.value.provider = preset.provider;
  llmForm.value.api_style = preset.apiStyle;
  llmForm.value.base_url = preset.baseURL;
  llmForm.value.model = preset.model;
  modelOptions.value = [];
  modelsError.value = "";
  modelPickerOpen.value = false;
}

function openModelPicker(): void {
  if (modelOptions.value.length > 0) {
    modelPickerOpen.value = true;
  } else if (!llmForm.value.model.trim() && (llmForm.value.api_key.trim() || llmConfigured.value)) {
    void loadModels(false);
  }
}

function pickModel(id: string): void {
  llmForm.value.model = id;
  modelPickerOpen.value = false;
}

async function loadModels(selectFirst: boolean): Promise<boolean> {
  if (modelsLoading.value) return false;
  modelsError.value = "";
  modelsLoading.value = true;
  try {
    const result = await listLLMModels({
      id: savedLLM.value?.id,
      provider: llmForm.value.provider,
      api_style: llmForm.value.api_style,
      base_url: llmForm.value.base_url.trim() || undefined,
      api_key: llmForm.value.api_key.trim() || undefined,
      model: llmForm.value.model.trim()
    });
    modelOptions.value = result.models;
    if (result.models.length === 0) {
      toastError("服务平台没有返回可用模型");
      return false;
    }
    if (selectFirst && !llmForm.value.model.trim()) {
      llmForm.value.model = result.models[0].id;
    }
    modelPickerOpen.value = !selectFirst;
    return true;
  } catch (error) {
    modelsError.value = error instanceof Error ? error.message : "拉取模型列表失败";
    toastError("模型列表获取失败，完整信息已显示在模型字段下方");
    return false;
  } finally {
    modelsLoading.value = false;
  }
}

const botForm = ref<{ onebot_reverse_ws_endpoint: string; owner_id: string; onebot_access_token: string }>({
  onebot_reverse_ws_endpoint: `ws://${window.location.host}/onebot/v11/ws`,
  owner_id: "",
  onebot_access_token: ""
});

const connected = computed(() => stream.status?.channel.connected ?? false);
const selfID = computed(() => stream.status?.channel.self_id ?? "");
const channelError = computed(() => stream.status?.channel.last_error ?? "");
const wsEndpoint = computed(() => botForm.value.onebot_reverse_ws_endpoint.trim());

const llmSummary = computed(() => {
  const config = savedLLM.value;
  if (!config) {
    return "—";
  }
  return `${config.provider} · ${config.model}`;
});

function stepDone(index: number): boolean {
  if (index === 0) {
    return llmConfigured.value;
  }
  if (index === 1) {
    return connected.value;
  }
  return false;
}

function stepClass(index: number): string {
  if (step.value === index) {
    return "active";
  }
  return stepDone(index) ? "done" : "";
}

async function copyEndpoint(): Promise<void> {
  try {
    await navigator.clipboard.writeText(wsEndpoint.value);
    toastSuccess("已复制连接地址");
  } catch {
    toastError("复制失败，请手动选择复制");
  }
}

async function saveAndTestLLM(): Promise<void> {
  if (!llmForm.value.model.trim()) {
    const resolved = await loadModels(true);
    if (!resolved) return;
  }
  busy.value = true;
  llmTestResult.value = "";
  try {
    const payload: LLMConfig = {
      id: savedLLM.value?.id,
      provider: llmForm.value.provider,
      api_style: llmForm.value.api_style,
      model: llmForm.value.model.trim(),
      models: modelOptions.value,
      base_url: llmForm.value.base_url.trim() || undefined,
      api_key: llmForm.value.api_key.trim() || undefined
    };
    const saved = await saveConfig(payload);
    savedLLM.value = saved;
    const result = await testLLM(llmTestMessage.value.trim());
    llmConfigured.value = true;
    llmTestResult.value = `连通成功：${result.text.slice(0, 40)}`;
    toastSuccess("LLM 配置已保存并连通");
    llmForm.value.api_key = "";
    step.value = 1;
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存或测试失败");
  } finally {
    busy.value = false;
  }
}

async function saveBotAndStart(): Promise<void> {
  if (!validWebSocketURL(wsEndpoint.value)) {
    toastError("请填写有效的 ws:// 或 wss:// 回连地址");
    return;
  }
  busy.value = true;
  try {
    const base = savedBot.value ?? (await getQQBotConfig());
    const payload: QQBotConfig = {
      ...base,
      enabled: true,
      onebot_reverse_ws_endpoint: wsEndpoint.value,
      bot_qq: base.bot_qq,
      owner_id: botForm.value.owner_id.trim(),
      onebot_access_token: botForm.value.onebot_access_token.trim() || undefined,
      profiles: undefined
    };
    savedBot.value = await saveQQBotConfig(payload);
    await startQQBot();
    toastSuccess("配置已保存，等待 OneBot v11 客户端连接");
    botForm.value.onebot_access_token = "";
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存失败");
  } finally {
    busy.value = false;
  }
}

onMounted(async () => {
  try {
    const [llm, bot] = await Promise.all([getConfig(), getQQBotConfig()]);
    savedLLM.value = llm;
    savedBot.value = bot;
    llmConfigured.value = Boolean(llm.api_key_configured);
    tokenConfigured.value = Boolean(bot.onebot_access_token_configured);
    llmForm.value.provider = llm.provider;
    llmForm.value.api_style = llm.api_style ?? "responses";
    llmForm.value.model = llm.model;
    llmForm.value.base_url = llm.base_url ?? "";
    selectedService.value = detectLLMService(llm.base_url);
    botForm.value.onebot_reverse_ws_endpoint =
      bot.onebot_reverse_ws_endpoint || `ws://${window.location.host}/onebot/v11/ws`;
    // 10001 was used by early demo data and should not appear as a real default.
    botForm.value.owner_id = bot.owner_id === "10001" ? "" : (bot.owner_id ?? "");
    if (llmConfigured.value && !connected.value) {
      step.value = 1;
    } else if (llmConfigured.value && connected.value) {
      step.value = 2;
    }
  } catch {
    /* 初次加载失败保持第一步 */
  }
});

function validWebSocketURL(value: string): boolean {
  try {
    const parsed = new URL(value);
    return (parsed.protocol === "ws:" || parsed.protocol === "wss:") && Boolean(parsed.host);
  } catch {
    return false;
  }
}

watch([connected, selfID], ([isConnected, id]) => {
  if (isConnected && id && step.value === 1) {
    toastSuccess(`已识别机器人 QQ：${id}`);
    step.value = 2;
  }
});
</script>
