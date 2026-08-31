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

    <!-- 第 1 步：提供商 -->
    <section v-if="step === 0" class="card">
      <div class="card-header">
        <h2>配置提供商</h2>
        <span v-if="llmConfigured" class="badge ok">已配置</span>
      </div>
      <div class="card-body stack">
        <div class="form-grid">
          <div class="field">
            <label for="wizard-provider-kind">接入类型</label>
            <AppSelect
              id="wizard-provider-kind"
              :model-value="llmForm.provider"
              :options="providerKindOptions"
              @update:model-value="applyProviderKind"
            />
            <span class="hint">{{ currentProviderKind.hint }}。</span>
          </div>
          <div class="field">
            <label for="wizard-service">服务平台</label>
            <AppSelect
              id="wizard-service"
              :model-value="selectedService"
              :options="serviceOptions"
              @update:model-value="applyServicePreset"
            />
          </div>
          <!-- 接口模式只对 OpenAI 兼容接口有意义；原生协议带上它会被后端拒绝。 -->
          <div v-if="supportsAPIStyle" class="field">
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
            <input
              id="wizard-baseurl"
              v-model="llmForm.base_url"
              class="input mono"
              :class="{ invalid: invalidField === 'base_url' }"
              :aria-invalid="invalidField === 'base_url'"
              placeholder="https://api.example.com/v1"
              @input="clearInvalid('base_url')"
            />
            <span class="hint">{{ selectedPreset?.hint }}；请填写完整 API 根地址，包括服务要求的 `/v1` 等路径。</span>
          </div>
          <div class="field wide">
            <label for="wizard-apikey">API Key</label>
            <input
              id="wizard-apikey"
              v-model="llmForm.api_key"
              class="input"
              :class="{ invalid: invalidField === 'api_key' }"
              :aria-invalid="invalidField === 'api_key'"
              type="password"
              :placeholder="llmConfigured ? '留空表示沿用已保存的 Key' : '粘贴你的 API Key'"
              autocomplete="off"
              @input="clearInvalid('api_key')"
            />
            <span class="hint">Key 只保存在本机 SQLite，不会上传到其他服务。</span>
          </div>
          <div class="field wide model-config-field">
            <div class="model-sync-row">
              <div class="model-sync-copy">
                <span class="model-sync-title">模型列表</span>
                <span v-if="modelOptions.length > 0" class="hint">当前有 {{ modelOptions.length }} 个模型，可同步刷新或手动补充。</span>
                <span v-else class="hint">填写 API Key 后从服务同步，也可以直接手填模型 ID。</span>
              </div>
              <button class="btn" type="button" :disabled="modelsLoading" @click="loadModels(false)">
                <RefreshCw :size="14" aria-hidden="true" />
                {{ modelsLoading ? "同步中…" : "同步模型列表" }}
              </button>
            </div>
            <!-- 中转和自建 endpoint 常常不实现 /models，同步会直接失败。首次配置
                 卡在这里就一步都走不下去，所以手填这条路必须有。 -->
            <div class="model-manual">
              <div class="input-group">
                <input
                  id="wizard-model"
                  v-model="manualModelDraft"
                  class="input"
                  placeholder="手动添加模型 ID，多个用逗号或换行分隔"
                  autocomplete="off"
                  @keydown.enter.prevent="addManualModels"
                />
                <button class="btn" type="button" :disabled="manualModelDraft.trim() === ''" @click="addManualModels">
                  <Plus :size="14" aria-hidden="true" />
                  添加
                </button>
              </div>
              <div v-if="modelOptions.length > 0" class="model-chips">
                <span v-for="model in modelOptions" :key="model.id" class="model-chip">
                  <span class="model-chip-id">{{ model.id }}</span>
                  <button type="button" class="model-chip-remove" :title="`移除 ${model.id}`" @click="removeModel(model.id)">
                    <X :size="12" aria-hidden="true" />
                  </button>
                </span>
              </div>
              <span class="hint">列表第一项就是这一步用来测试连通的模型；之后在提供商页还能继续增删。</span>
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
            <label for="wizard-owner">主人账号（可选）</label>
            <input
              id="wizard-owner"
              v-model="botForm.owner_id"
              class="input"
              inputmode="numeric"
              placeholder="例如 123456789，用于管理指令和私聊登录"
            />
            <AccountNameHint :user-id="botForm.owner_id" />
            <span class="hint">不需要聊天内管理或配对登录时可以留空。</span>
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
            <span class="check-main">提供商已配置<div class="check-hint">{{ llmSummary }}</div></span>
          </div>
          <div class="checklist-item" :class="connected ? 'done' : 'todo'">
            <span class="check-icon"><CheckCircle2 :size="15" aria-hidden="true" /></span>
            <span class="check-main">OneBot v11 已连接<div class="check-hint">{{ connected ? `账号 ${selfID}` : "尚未连接" }}</div></span>
          </div>
        </div>
        <p class="muted">
          现在给机器人发一条私聊消息，或在群里 @ 它试试。群聊触发词默认为
          <code>Diana</code>、<code>diana</code>。
        </p>
        <div class="cluster">
          <button class="btn primary" type="button" @click="finishSetup">
            <LayoutGrid :size="15" aria-hidden="true" />
            进入总览
          </button>
          <button class="btn" type="button" @click="navigate('provider')">
            <MessageCircle :size="15" aria-hidden="true" />
            去「提供商」再测一次
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
import { CheckCircle2, ChevronLeft, ChevronRight, Copy, LayoutGrid, MessageCircle, Plus, Power, RefreshCw, X, Zap } from "@lucide/vue";
import {
  getConfig,
  getBotProfileConfig,
  listLLMModels,
  saveConfig,
  saveBotProfileConfig,
  startBot,
  testLLM,
  type LLMConfig,
  type LLMModelInfo,
  type Provider,
  type BotProfileConfig
} from "../api";
import { stream } from "../stream";
import { navigate } from "../router";
import { toastError, toastSuccess } from "../toast";
import AccountNameHint from "../components/AccountNameHint.vue";
import AppSelect from "../components/AppSelect.vue";
import {
  defaultPresetForProvider,
  detectLLMService,
  llmErrorField,
  llmProviderKinds,
  llmServicePresets,
  presetsForProvider,
  type LLMErrorField
} from "../llm-presets";

const step = ref(0);
const busy = ref(false);
const stepLabels = ["配置提供商", "接入 OneBot v11", "启动验证"];
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
const savedBot = ref<BotProfileConfig | null>(null);
const modelOptions = ref<LLMModelInfo[]>([]);
const modelsLoading = ref(false);
const invalidField = ref<LLMErrorField>("");

function clearInvalid(field: LLMErrorField): void {
  if (invalidField.value === field) {
    invalidField.value = "";
  }
}
const manualModelDraft = ref("");

const llmForm = ref<{ provider: Provider; api_style: "responses" | "chat_completions" | ""; base_url: string; api_key: string }>({
  provider: "openai_compatible",
  api_style: "responses",
  base_url: "https://api.openai.com/v1",
  api_key: ""
});

const providerKindOptions = llmProviderKinds.map((kind) => ({
  value: kind.id,
  label: kind.label,
  hint: kind.hint
}));

const currentProviderKind = computed(() =>
  llmProviderKinds.find((kind) => kind.id === llmForm.value.provider) ?? llmProviderKinds[0]
);

/** 接口模式只对 OpenAI 兼容接口有意义，原生协议带上它会被后端拒绝。 */
const supportsAPIStyle = computed(() => currentProviderKind.value.supportsAPIStyle);

const serviceOptions = computed(() =>
  presetsForProvider(llmForm.value.provider).map((preset) => ({
    value: preset.id,
    label: preset.label,
    hint: preset.hint
  }))
);

/** 切换接入类型：落到该类型的第一个服务商，把协议专属字段一并归位。 */
function applyProviderKind(provider: string): void {
  const preset = defaultPresetForProvider(provider as Provider);
  if (preset) applyServicePreset(preset.id);
}
const selectedPreset = computed(() => llmServicePresets.find((preset) => preset.id === selectedService.value));

function applyServicePreset(id: string): void {
  const preset = llmServicePresets.find((item) => item.id === id);
  if (!preset) return;
  selectedService.value = id;
  llmForm.value.provider = preset.provider;
  llmForm.value.api_style = preset.apiStyle;
  llmForm.value.base_url = preset.baseURL;
  // 预设自带的模型直接当列表第一项：换服务平台时它就是最合理的起点，
  // 用户不满意可以删掉再手填。
  modelOptions.value = preset.model ? [{ id: preset.model }] : [];
  manualModelDraft.value = "";
  invalidField.value = "";
}

/** 手填模型：同步不可用时（中转、自建网关常见）这是唯一的入口。 */
function addManualModels(): void {
  const existing = new Set(modelOptions.value.map((model) => model.id));
  for (const raw of manualModelDraft.value.split(/[,，\n]/)) {
    const id = raw.trim();
    if (id === "" || existing.has(id)) continue;
    existing.add(id);
    modelOptions.value.push({ id });
  }
  manualModelDraft.value = "";
}

function removeModel(id: string): void {
  modelOptions.value = modelOptions.value.filter((model) => model.id !== id);
}

async function loadModels(selectFirst: boolean): Promise<boolean> {
  if (modelsLoading.value) return false;
  invalidField.value = "";
  modelsLoading.value = true;
  try {
    const result = await listLLMModels({
      id: savedLLM.value?.id,
      provider: llmForm.value.provider,
      api_style: llmForm.value.provider === "openai_compatible" ? (llmForm.value.api_style || undefined) : undefined,
      base_url: llmForm.value.base_url.trim() || undefined,
      api_key: llmForm.value.api_key.trim() || undefined,
      model: modelOptions.value[0]?.id ?? ""
    });
    if (result.models.length === 0) {
      toastError("服务平台没有返回可用模型");
      return false;
    }
    // 合并而不是覆盖：手填进来的模型（中转站上同步不到的那些）不能被一次同步冲掉。
    const fetched = new Set(result.models.map((model) => model.id));
    modelOptions.value = [...result.models, ...modelOptions.value.filter((model) => !fetched.has(model.id))];
    return true;
  } catch (error) {
    // 和 LLM 配置页一致：报错原文进 toast，该回去改的那一格标红。
    const message = error instanceof Error ? error.message : "拉取模型列表失败";
    invalidField.value = llmErrorField(message);
    toastError(message);
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
  // 一个模型都没有就没得测。同步不通的话上面还能手填，这里只兜同步这一条。
  if (modelOptions.value.length === 0) {
    const resolved = await loadModels(true);
    if (!resolved) return;
  }
  busy.value = true;
  llmTestResult.value = "";
  try {
    const payload: LLMConfig = {
      id: savedLLM.value?.id,
      provider: llmForm.value.provider,
      api_style: llmForm.value.provider === "openai_compatible" ? (llmForm.value.api_style || undefined) : undefined,
      // 兜底模型跟着列表走，和提供商页一致：这一格不再单独让人填，
      // 填出来的值不在列表里时，兜底会指向一套配置里根本没有的模型。
      model: modelOptions.value[0]?.id ?? "",
      models: modelOptions.value,
      base_url: llmForm.value.base_url.trim() || undefined,
      api_key: llmForm.value.api_key.trim() || undefined
    };
    const saved = await saveConfig(payload);
    savedLLM.value = saved;
    const result = await testLLM(llmTestMessage.value.trim());
    llmConfigured.value = true;
    llmTestResult.value = `连通成功：${result.text.slice(0, 40)}`;
    toastSuccess("提供商配置已保存并连通");
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
    const base = savedBot.value ?? (await getBotProfileConfig());
    const payload: BotProfileConfig = {
      ...base,
      enabled: true,
      onebot_reverse_ws_endpoint: wsEndpoint.value,
      bot_account: base.bot_account,
      owner_id: botForm.value.owner_id.trim(),
      onebot_access_token: botForm.value.onebot_access_token.trim() || undefined,
      profiles: undefined
    };
    savedBot.value = await saveBotProfileConfig(payload);
    await startBot();
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
    const [llm, bot] = await Promise.all([getConfig(), getBotProfileConfig()]);
    savedLLM.value = llm;
    savedBot.value = bot;
    llmConfigured.value = Boolean(llm.api_key_configured);
    tokenConfigured.value = Boolean(bot.onebot_access_token_configured);
    llmForm.value.provider = llm.provider;
    // 原生协议没有接口模式，补默认值会在保存时被后端拒绝。
    llmForm.value.api_style = llm.provider === "openai_compatible" ? (llm.api_style ?? "responses") : "";
    llmForm.value.base_url = llm.base_url ?? "";
    // 已经配过的实例重进向导时，模型列表要回填出来——它现在是这一步唯一的
    // 模型来源，空着的话会看起来像配置丢了。老配置可能只存了单个 model。
    modelOptions.value = llm.models?.length ? [...llm.models] : llm.model ? [{ id: llm.model }] : [];
    selectedService.value = detectLLMService(llm.base_url, llm.provider);
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
    toastSuccess(`已识别机器人账号：${id}`);
    step.value = 2;
  }
});
</script>
