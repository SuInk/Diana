<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <p>三步跑通：配置模型 → 接入聊天平台 → 启动验证</p>
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
        :disabled="loading"
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
    <section v-if="loading" class="card">
      <div class="card-header"><SkeletonBlock width="112px" height="23px" /></div>
      <div class="card-body"><LoadingSkeleton kind="form" :count="6" label="正在加载配置向导" /></div>
    </section>
    <section v-else-if="step === 0" class="card">
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
                  <span class="model-chip-id" :title="model.id">{{ model.id }}</span>
                  <button type="button" class="model-chip-remove" :title="`移除模型 ${model.id}`" :aria-label="`移除模型 ${model.id}`" @click="removeModel(model.id)">
                    <X :size="14" :stroke-width="2.25" aria-hidden="true" />
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

    <!-- 第 2 步：接入聊天平台 -->
    <section v-else-if="step === 1" class="card">
      <div class="card-header">
        <h2>接入聊天平台</h2>
        <span v-if="connected" class="badge ok">已连接</span>
      </div>
      <div class="card-body stack">
        <div class="form-grid">
          <div class="field wide">
            <label for="wizard-platform">接入平台</label>
            <AppSelect
              id="wizard-platform"
              :model-value="botForm.platform"
              :options="platformOptions"
              @update:model-value="(value) => (botForm.platform = value)"
            />
            <span class="hint">{{ platformDescription }}</span>
          </div>
          <template v-if="isOneBotPlatform">
            <div class="field wide">
              <label for="wizard-onebot-transport">连接方式</label>
              <select id="wizard-onebot-transport" v-model="botForm.onebot_transport" class="input">
                <option value="reverse_ws">反向 WebSocket</option>
                <option value="forward_ws">正向 WebSocket</option>
                <option value="http">HTTP API + HTTP 事件上报</option>
              </select>
            </div>
            <div v-if="botForm.onebot_transport === 'reverse_ws'" class="field wide">
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
            <div v-else-if="botForm.onebot_transport === 'forward_ws'" class="field wide">
              <label for="wizard-onebot-ws">OneBot WS 服务地址</label>
              <input id="wizard-onebot-ws" v-model="botForm.onebot_ws_endpoint" class="input mono" placeholder="ws://127.0.0.1:6700/" />
              <span class="hint">使用同时提供 API 和事件的通用 WS 地址，Diana 主动连接并自动重连。发送文件/图片时接入端按这里的主机名回源拉取媒体：同机或容器（host.docker.internal）部署无需额外配置，跨机部署稍后可在「设置 → 媒体与文件」页配置媒体回源基址。</span>
            </div>
            <template v-else>
              <div class="field wide">
                <label for="wizard-onebot-http">OneBot HTTP API 地址</label>
                <input id="wizard-onebot-http" v-model="botForm.onebot_http_url" class="input mono" placeholder="http://127.0.0.1:5700" />
                <span class="hint">接入端把事件上报至 http://&lt;Diana 主机&gt;:18080/onebot/v11/http，填写实际可访问的主机和端口。</span>
              </div>
              <div class="field wide">
                <label for="wizard-onebot-secret">HTTP 事件签名密钥</label>
                <input id="wizard-onebot-secret" v-model="botForm.onebot_http_secret" class="input" type="password" autocomplete="off" placeholder="与接入端的上报 secret 一致；留空沿用已保存值" />
              </div>
            </template>
            <p v-if="oneBotMediaOriginWarning" class="hint warn-text">{{ oneBotMediaOriginWarning }}</p>
            <div class="field wide">
              <label for="wizard-token">OneBot Access Token（{{ tokenRequired ? "反向 WebSocket 必填" : "可选" }}，至少 8 位）</label>
              <div class="input-group">
                <input id="wizard-token" v-model="botForm.onebot_access_token" class="input" type="text" autocomplete="off"
                  :placeholder="tokenConfigured ? (savedBot?.onebot_access_token_preview ? `已保存 ${savedBot.onebot_access_token_preview}，留空沿用` : '留空表示沿用已保存 token') : '与 OneBot v11 客户端填写的 token 保持一致'" />
                <button class="btn icon-only" type="button" aria-label="随机生成 Token" title="随机生成" @click="generateToken">
                  <Dices :size="14" aria-hidden="true" />
                </button>
              </div>
              <span v-if="tokenRequiredHint" class="hint">{{ tokenRequiredHint }}</span>
            </div>
          </template>

          <template v-else-if="botForm.platform === 'telegram'">
            <div class="field wide">
              <label for="wizard-tg-token">Bot Token</label>
              <input id="wizard-tg-token" v-model="botForm.telegram_bot_token" class="input" type="password" autocomplete="off"
                :placeholder="secretConfigured('telegram_bot_token_configured') ? '留空表示沿用已保存的 Token' : '从 @BotFather 获取'" />
              <span class="hint">长轮询出站连接，不需要公网地址，也不用配置 webhook。</span>
            </div>
            <div class="field wide">
              <label for="wizard-tg-proxy">代理地址（可选）</label>
              <input id="wizard-tg-proxy" v-model="botForm.telegram_proxy_url" class="input mono" autocomplete="off" placeholder="留空直连，例如 http://127.0.0.1:7890" />
              <span class="hint">直连不通时再填；国内网络访问 api.telegram.org 通常需要代理，支持 http/https/socks5。</span>
            </div>
            <div class="field wide">
              <label for="wizard-tg-base">自建 Bot API 地址（可选）</label>
              <input id="wizard-tg-base" v-model="botForm.telegram_api_base_url" class="input mono" autocomplete="off" placeholder="留空使用官方 https://api.telegram.org" />
              <span class="hint">部署了本地 Bot API server 时填写，可绕过 50MB 上传限制。</span>
            </div>
          </template>

          <template v-else-if="botForm.platform === 'qq-official'">
            <div class="field wide">
              <label for="wizard-qq-appid">AppID</label>
              <input id="wizard-qq-appid" v-model="botForm.qq_app_id" class="input mono" autocomplete="off" placeholder="QQ 开放平台的机器人 AppID" />
              <span class="hint">在 q.qq.com 的机器人管理后台「开发设置」里查看。</span>
            </div>
            <div class="field wide">
              <label for="wizard-qq-secret">AppSecret</label>
              <input id="wizard-qq-secret" v-model="botForm.qq_app_secret" class="input" type="password" autocomplete="off"
                :placeholder="secretConfigured('qq_app_secret_configured') ? '留空表示沿用已保存的 AppSecret' : '开发设置里的机器人密钥'" />
              <span class="hint">出站 WebSocket 网关接入，不需要公网地址；平台只会推送 @ 机器人的群消息。</span>
            </div>
            <div class="field wide">
              <label class="check">
                <input v-model="botForm.qq_sandbox" type="checkbox" />
                <span>使用沙箱环境</span>
              </label>
              <span class="hint">机器人尚未发布上架时勾选，走沙箱接口联调。</span>
            </div>
          </template>

          <template v-else-if="botForm.platform === 'dingtalk'">
            <div class="field wide">
              <label for="wizard-ding-id">Client ID</label>
              <input id="wizard-ding-id" v-model="botForm.dingtalk_client_id" class="input mono" autocomplete="off" placeholder="应用的 AppKey / Client ID" />
              <span class="hint">钉钉开放平台的应用凭证页可以看到。</span>
            </div>
            <div class="field wide">
              <label for="wizard-ding-secret">Client Secret</label>
              <input id="wizard-ding-secret" v-model="botForm.dingtalk_client_secret" class="input" type="password" autocomplete="off"
                :placeholder="secretConfigured('dingtalk_client_secret_configured') ? '留空表示沿用已保存的 Secret' : '应用的 AppSecret / Client Secret'" />
              <span class="hint">用 Stream 模式出站长连接接入，不需要公网地址，也不用在后台配 HTTP 回调。</span>
            </div>
            <div class="field wide">
              <label for="wizard-ding-robot">机器人 RobotCode（可选）</label>
              <input id="wizard-ding-robot" v-model="botForm.dingtalk_robot_code" class="input mono" autocomplete="off" placeholder="留空则与 Client ID 相同" />
              <span class="hint">企业内部机器人单独分配了 robotCode 时才需要填。</span>
            </div>
          </template>

          <template v-else-if="botForm.platform === 'feishu'">
            <div class="field wide">
              <label for="wizard-feishu-appid">App ID</label>
              <input id="wizard-feishu-appid" v-model="botForm.feishu_app_id" class="input mono" autocomplete="off" placeholder="cli_ 开头的自建应用 App ID" />
              <span class="hint">飞书开放平台的「凭证与基础信息」页。</span>
            </div>
            <div class="field wide">
              <label for="wizard-feishu-secret">App Secret</label>
              <input id="wizard-feishu-secret" v-model="botForm.feishu_app_secret" class="input" type="password" autocomplete="off"
                :placeholder="secretConfigured('feishu_app_secret_configured') ? '留空表示沿用已保存的 Secret' : '自建应用的 App Secret'" />
            </div>
            <div class="field wide">
              <label for="wizard-feishu-verify">Verification Token</label>
              <input id="wizard-feishu-verify" v-model="botForm.feishu_verification_token" class="input" type="password" autocomplete="off"
                :placeholder="secretConfigured('feishu_verification_token_configured') ? '留空表示沿用已保存的 Token' : '事件订阅页的 Verification Token'" />
              <span class="hint">用于核验回调来源。强烈建议填写——回调地址本身是公开的，不能当凭据用。</span>
            </div>
            <div class="field wide">
              <label for="wizard-feishu-encrypt">Encrypt Key（可选）</label>
              <input id="wizard-feishu-encrypt" v-model="botForm.feishu_encrypt_key" class="input" type="password" autocomplete="off"
                :placeholder="secretConfigured('feishu_encrypt_key_configured') ? '留空表示沿用已保存的 Key' : '后台开启了加密推送才填'" />
              <span class="hint">填了这里就必须在飞书后台同步开启加密推送，否则明文回调会被拒绝。</span>
            </div>
            <div class="field wide">
              <label for="wizard-feishu-base">开放平台地址（可选）</label>
              <input id="wizard-feishu-base" v-model="botForm.feishu_api_base_url" class="input mono" autocomplete="off" placeholder="留空使用 https://open.feishu.cn" />
              <span class="hint">Lark 国际版填 https://open.larksuite.com。</span>
            </div>
          </template>

          <template v-else-if="botForm.platform === 'wecom'">
            <div class="field wide">
              <label for="wizard-wecom-corp">企业 ID</label>
              <input id="wizard-wecom-corp" v-model="botForm.wecom_corp_id" class="input mono" autocomplete="off" placeholder="ww 开头的 CorpID" />
              <span class="hint">企业微信管理后台「我的企业」页底部。</span>
            </div>
            <div class="field wide">
              <label for="wizard-wecom-agent">AgentId</label>
              <input id="wizard-wecom-agent" v-model="botForm.wecom_agent_id" class="input mono" inputmode="numeric" autocomplete="off" placeholder="自建应用的 AgentId，纯数字" />
              <span class="hint">在「应用管理」里打开自建应用即可看到。</span>
            </div>
            <div class="field wide">
              <label for="wizard-wecom-secret">应用 Secret</label>
              <input id="wizard-wecom-secret" v-model="botForm.wecom_secret" class="input" type="password" autocomplete="off"
                :placeholder="secretConfigured('wecom_secret_configured') ? '留空表示沿用已保存的 Secret' : '自建应用的 Secret'" />
            </div>
            <div class="field wide">
              <label for="wizard-wecom-token">Token</label>
              <input id="wizard-wecom-token" v-model="botForm.wecom_token" class="input" type="password" autocomplete="off"
                :placeholder="secretConfigured('wecom_token_configured') ? '留空表示沿用已保存的 Token' : '「接收消息」配置里的 Token'" />
            </div>
            <div class="field wide">
              <label for="wizard-wecom-aes">EncodingAESKey</label>
              <input id="wizard-wecom-aes" v-model="botForm.wecom_encoding_aes_key" class="input" type="password" autocomplete="off"
                :placeholder="secretConfigured('wecom_encoding_aes_key_configured') ? '留空表示沿用已保存的 Key' : '43 位的 EncodingAESKey'" />
              <span class="hint">Token 和 EncodingAESKey 用于回调验签和解密，缺一个就只能发不能收。</span>
            </div>
          </template>

          <!-- 飞书和企业微信只能靠平台回调收消息，地址要填到对方后台。 -->
          <div v-if="callbackURL" class="field wide">
            <label for="wizard-callback-url">回调地址</label>
            <div class="input-group">
              <input id="wizard-callback-url" class="input mono" :value="callbackURL" readonly />
              <button class="btn icon-only" type="button" aria-label="复制回调地址" @click="copyCallbackURL">
                <Copy :size="14" aria-hidden="true" />
              </button>
            </div>
            <span class="hint">填到该平台后台的事件接收配置里。这里按你当前访问控制台的地址拼出，必须换成平台服务器能访问到的公网 HTTPS 地址才收得到消息。</span>
          </div>
          <div class="field">
            <label for="wizard-owner">{{ ownerLabel }}（可选）</label>
            <input
              id="wizard-owner"
              v-model="botForm.owner_id"
              class="input"
              :inputmode="isOneBotPlatform ? 'numeric' : 'text'"
              :placeholder="ownerPlaceholder"
            />
            <AccountNameHint v-if="isOneBotPlatform" :user-id="botForm.owner_id" />
            <span class="hint">不需要聊天内管理或配对登录时可以留空。</span>
          </div>
        </div>
        <div class="cluster">
          <button class="btn primary" type="button" :disabled="busy" @click="saveBotAndStart">
            <Power :size="15" aria-hidden="true" />
            {{ startButtonLabel }}
          </button>
          <span class="badge" :class="connected ? 'ok' : 'warn'">
            <span class="status-dot" :class="{ pulse: !connected }" aria-hidden="true" />
            {{ connected ? `${connectedPlatformName} 已连接 ${selfID}` : `等待 ${platformName} 通道就绪…` }}
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
            <span class="check-main">{{ connectedPlatformName }} 已连接<div class="check-hint">{{ connected ? `账号 ${selfID}` : "尚未连接" }}</div></span>
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
      <button class="btn wizard-nav-button" type="button" :disabled="loading || step === 0" @click="step = Math.max(0, step - 1)">
        <ChevronLeft :size="15" aria-hidden="true" />
        上一步
      </button>
      <button v-if="step < 2" class="btn wizard-nav-button" type="button" :disabled="loading" @click="step = step + 1">
        跳过此步
        <ChevronRight :size="15" aria-hidden="true" />
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useConfigurationRefresh } from "../configuration-sync";
import { computed, onMounted, ref, watch } from "vue";
import LoadingSkeleton from "../components/LoadingSkeleton.vue";
import SkeletonBlock from "../components/SkeletonBlock.vue";
import { CheckCircle2, ChevronLeft, ChevronRight, Copy, Dices, LayoutGrid, MessageCircle, Plus, Power, RefreshCw, X, Zap } from "@lucide/vue";
import {
  getConfig,
  getBotPlatforms,
  getBotProfileConfig,
  listLLMModels,
  saveConfig,
  saveBotProfileConfig,
  startBot,
  testLLM,
  type BotPlatform,
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
const loading = ref(true);
const busy = ref(false);
const stepLabels = ["配置提供商", "接入聊天平台", "启动验证"];
/** 平台注册表取不到时的默认平台，也是历史配置里 platform 为空时的含义。 */
const PlatformOneBotV11 = "onebot-v11";
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
	// 同步结果只显示服务端真实返回；手填模型可在同步后重新添加。
	modelOptions.value = [...result.models];
	toastSuccess(`成功同步 ${result.models.length} 个模型`);
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

const botForm = ref({
  platform: PlatformOneBotV11,
  onebot_transport: "reverse_ws" as "reverse_ws" | "forward_ws" | "http",
  onebot_ws_endpoint: "",
  onebot_http_url: "",
  onebot_http_secret: "",
  onebot_reverse_ws_endpoint: `ws://${window.location.host}/onebot/v11/ws`,
  owner_id: "",
  onebot_access_token: "",
  telegram_bot_token: "",
  telegram_api_base_url: "",
  telegram_proxy_url: "",
  qq_app_id: "",
  qq_app_secret: "",
  qq_sandbox: false,
  dingtalk_client_id: "",
  dingtalk_client_secret: "",
  dingtalk_robot_code: "",
  feishu_app_id: "",
  feishu_app_secret: "",
  feishu_verification_token: "",
  feishu_encrypt_key: "",
  feishu_api_base_url: "",
  wecom_corp_id: "",
  wecom_agent_id: "",
  wecom_secret: "",
  wecom_token: "",
  wecom_encoding_aes_key: ""
});

// 平台注册表来自后端，这里先塞一条 OneBot 兜底：/platforms 取不到时这一步仍
// 然能用最常见的接入方式走完，而不是给出一个空的平台下拉框。
const platforms = ref<BotPlatform[]>([
  {
    id: PlatformOneBotV11,
    name: "OneBot v11",
    protocol: "onebot-v11",
    category: "onebot_v11",
    category_label: "OneBot v11",
    description: "OneBot v11 正向 WebSocket、反向 WebSocket 和 HTTP 接入",
    inbound: "reverse_ws"
  }
]);

const platformOptions = computed(() =>
  platforms.value.map((platform) => ({ value: platform.id, label: platform.name, hint: platform.description }))
);
const currentPlatform = computed(() => platforms.value.find((platform) => platform.id === botForm.value.platform));
const platformName = computed(() => currentPlatform.value?.name ?? "聊天平台");
const platformDescription = computed(() => currentPlatform.value?.description ?? "选择机器人要接入的聊天平台。");
// 未知平台按 OneBot 处理：兜底列表只有它，落到这里说明后端注册表没取到。
const isOneBotPlatform = computed(() => (currentPlatform.value?.protocol ?? "onebot-v11").startsWith("onebot"));

/** 回调型平台要把这个地址填到对方后台；按浏览器当前 origin 拼。 */
const callbackURL = computed(() => {
  const path = currentPlatform.value?.callback_path ?? "";
  if (!path) return "";
  return window.location.origin ? `${window.location.origin}${path}` : path;
});

const startButtonLabel = computed(() => {
  switch (currentPlatform.value?.inbound) {
    case "callback":
      return "保存并启动接收回调";
    case "outbound":
      return "保存并启动连接";
    default:
      return "保存并启动等待连接";
  }
});

const ownerLabel = computed(() =>
  isOneBotPlatform.value || botForm.value.platform === "telegram" ? "主人账号" : "主人用户 ID"
);
const ownerPlaceholder = computed(() => {
  if (botForm.value.platform === "telegram") return "数字用户 ID 或 @用户名，例如 70001 / @owneruser";
  if (isOneBotPlatform.value) return "例如 123456789，用于管理指令和私聊登录";
  return "平台用户 ID，用于管理指令";
});

/** 后端从不回显明文密钥，只回 *_configured；据此决定占位文案说不说「留空沿用」。 */
function secretConfigured(field: keyof BotProfileConfig): boolean {
  return Boolean(savedBot.value?.[field]);
}

const connected = computed(() => stream.status?.channel.connected ?? false);
// 已连接时按实际在线的通道报平台名：在下拉里翻看别的平台，不该把已经在线的
// 那条通道跟着改名。
const connectedPlatformName = computed(() => {
  const id = stream.status?.channel.platform ?? "";
  return platforms.value.find((platform) => platform.id === id)?.name ?? platformName.value;
});
const selfID = computed(() => stream.status?.channel.self_id ?? "");
// 反向 WS 是接入端连进 Diana：server 侧 token 为空会拒绝一切握手，所以首次
// 配置反向 WS 时必须填 token；正向 WS / HTTP 由 Diana 外连，token 可留空。
const tokenRequired = computed(
  () => isOneBotPlatform.value && botForm.value.onebot_transport === "reverse_ws" && !tokenConfigured.value
);
const tokenRequiredHint = computed(() =>
  tokenRequired.value ? "反向 WebSocket 模式下 NapCat 等客户端必须凭这个 token 才能连进来，请与客户端填写保持一致。" : ""
);
const channelError = computed(() => stream.status?.channel.last_error ?? "");
const wsEndpoint = computed(() => botForm.value.onebot_reverse_ws_endpoint.trim());

function endpointHostname(endpoint: string): string {
  const trimmed = endpoint.trim();
  if (!trimmed) return "";
  try {
    return (new URL(trimmed).hostname || "").replace(/^\[|\]$/g, "").toLowerCase();
  } catch {
    return "";
  }
}

function isLocalOriginHost(host: string): boolean {
  return host === "localhost" || host.endsWith(".localhost") ||
    host === "0.0.0.0" || host === "::" || host === "::1" ||
    host.startsWith("127.") || host === "host.docker.internal";
}

// 正向 ws / HTTP 接入下文件/媒体靠接入端回源 Diana：后端按所填地址推主机名。
// 回环或 docker 内网名多半没事；另一台主机要在服务端 config.yaml 配置
// storage.local_media_base_url，这里直接警告而不是只留一行灰字。纯函数，
// 与 AssistantView 保持一致，便于单测。
function oneBotMediaOriginWarningText(transport: string, wsEndpoint: string, httpEndpoint: string, currentHost: string): string {
  if (transport === "reverse_ws") return "";
  const host = endpointHostname(transport === "forward_ws" ? wsEndpoint : httpEndpoint);
  if (!host) return "";
  if (isLocalOriginHost(host) || host === (currentHost || "").replace(/^\[|\]$/g, "").toLowerCase()) return "";
  return `接入端将按 ${host} 回源拉取文件/媒体（端口为 Diana 的 Web 端口）。若该主机访问不到 Diana，文件发送会失败，请在「设置 → 媒体与文件」页配置媒体回源基址。`;
}

const oneBotMediaOriginWarning = computed(() => {
  if (!isOneBotPlatform.value) return "";
  return oneBotMediaOriginWarningText(
    botForm.value.onebot_transport,
    botForm.value.onebot_ws_endpoint ?? "",
    botForm.value.onebot_http_url ?? "",
    window.location.hostname || ""
  );
});

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

async function copyCallbackURL(): Promise<void> {
  if (!callbackURL.value) return;
  try {
    await navigator.clipboard.writeText(callbackURL.value);
    toastSuccess("回调地址已复制");
  } catch {
    toastError("复制失败，请手动选择复制");
  }
}

async function copyEndpoint(): Promise<void> {
  try {
    await navigator.clipboard.writeText(wsEndpoint.value);
    toastSuccess("已复制连接地址");
  } catch {
    toastError("复制失败，请手动选择复制");
  }
}

// 用 CSPRNG 生成足够长的随机 token，满足后端的最低长度要求；
// 生成后保持明文显示，方便用户复制到 OneBot 客户端。
function generateToken(): void {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  const bytes = new Uint8Array(32);
  crypto.getRandomValues(bytes);
  let token = "";
  for (const byte of bytes) {
    token += alphabet[byte % alphabet.length];
  }
  botForm.value.onebot_access_token = token;
  toastSuccess("已生成随机 Token，请同步填写到 OneBot 客户端");
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

/**
 * 保存前的必填校验。后端同样会校验，但这里能指到具体那一格，而不是把协议层
 * 的报错丢给刚开始配置的人。已存过的密钥留空表示沿用，所以要连
 * `*_configured` 一起看，否则第二次进向导会被自己的校验拦住。
 */
function credentialError(): string {
  const form = botForm.value;
  const filled = (draft: string, field: keyof BotProfileConfig): boolean =>
    draft.trim() !== "" || secretConfigured(field);
  if (isOneBotPlatform.value) {
    if (form.onebot_transport === "http") {
      if (!/^https?:\/\//.test(form.onebot_http_url.trim())) {
        return "请填写有效的 http:// 或 https:// OneBot HTTP API 地址";
      }
      if (!filled(form.onebot_http_secret, "onebot_http_secret_configured")) {
        return "HTTP 事件上报需要配置签名密钥，与接入端保持一致";
      }
      return "";
    }
    if (!validWebSocketURL(form.onebot_transport === "forward_ws" ? form.onebot_ws_endpoint : wsEndpoint.value)) {
      return "请填写有效的 ws:// 或 wss:// 回连地址";
    }
    // 后端会拒绝「启用 + 反向 WS + 空 token」的保存；提前拦住，提示更贴上下文。
    if (tokenRequired.value && form.onebot_access_token.trim() === "") {
      return "反向 WebSocket 模式必须填写 Access Token，需与 OneBot v11 客户端保持一致";
    }
    return "";
  }
  switch (form.platform) {
    case "telegram":
      return filled(form.telegram_bot_token, "telegram_bot_token_configured")
        ? ""
        : "请填写 Telegram Bot Token，找 @BotFather 申请";
    case "qq-official":
      if (form.qq_app_id.trim() === "") return "请填写 QQ 开放平台的 AppID";
      return filled(form.qq_app_secret, "qq_app_secret_configured") ? "" : "请填写 QQ 开放平台的 AppSecret";
    case "dingtalk":
      if (form.dingtalk_client_id.trim() === "") return "请填写钉钉应用的 Client ID";
      return filled(form.dingtalk_client_secret, "dingtalk_client_secret_configured")
        ? ""
        : "请填写钉钉应用的 Client Secret";
    case "feishu":
      if (form.feishu_app_id.trim() === "") return "请填写飞书自建应用的 App ID";
      return filled(form.feishu_app_secret, "feishu_app_secret_configured") ? "" : "请填写飞书自建应用的 App Secret";
    case "wecom":
      if (form.wecom_corp_id.trim() === "") return "请填写企业微信的企业 ID";
      if (!/^\d+$/.test(form.wecom_agent_id.trim())) return "请填写自建应用的 AgentId，为纯数字";
      if (!filled(form.wecom_secret, "wecom_secret_configured")) return "请填写自建应用的 Secret";
      // 回调验签缺任何一项都是「只能发不能收」，这种半可用状态先说清楚。
      if (!filled(form.wecom_token, "wecom_token_configured")
        || !filled(form.wecom_encoding_aes_key, "wecom_encoding_aes_key_configured")) {
        return "请填写企业微信「接收消息」里的 Token 和 EncodingAESKey，缺一个就只能发不能收";
      }
      return "";
    default:
      return "";
  }
}

/**
 * 只提交当前平台的那一组接入字段。别的平台的配置原样跟着已存配置走，免得在
 * 向导里换一次平台就把之前配好的另一套接入信息清空。
 */
function platformPayload(): Partial<BotProfileConfig> {
  const form = botForm.value;
  switch (form.platform) {
    case "telegram":
      return {
        telegram_bot_token: form.telegram_bot_token.trim() || undefined,
        telegram_api_base_url: form.telegram_api_base_url.trim(),
        telegram_proxy_url: form.telegram_proxy_url.trim()
      };
    case "qq-official":
      return {
        qq_app_id: form.qq_app_id.trim(),
        qq_app_secret: form.qq_app_secret.trim() || undefined,
        qq_sandbox: form.qq_sandbox
      };
    case "dingtalk":
      return {
        dingtalk_client_id: form.dingtalk_client_id.trim(),
        dingtalk_client_secret: form.dingtalk_client_secret.trim() || undefined,
        dingtalk_robot_code: form.dingtalk_robot_code.trim()
      };
    case "feishu":
      return {
        feishu_app_id: form.feishu_app_id.trim(),
        feishu_app_secret: form.feishu_app_secret.trim() || undefined,
        feishu_verification_token: form.feishu_verification_token.trim() || undefined,
        feishu_encrypt_key: form.feishu_encrypt_key.trim() || undefined,
        feishu_api_base_url: form.feishu_api_base_url.trim()
      };
    case "wecom":
      return {
        wecom_corp_id: form.wecom_corp_id.trim(),
        wecom_agent_id: form.wecom_agent_id.trim(),
        wecom_secret: form.wecom_secret.trim() || undefined,
        wecom_token: form.wecom_token.trim() || undefined,
        wecom_encoding_aes_key: form.wecom_encoding_aes_key.trim() || undefined
      };
    default:
      return {
        onebot_transport: form.onebot_transport,
        onebot_ws_endpoint: form.onebot_ws_endpoint.trim(),
        onebot_http_url: form.onebot_http_url.trim(),
        onebot_http_secret: form.onebot_http_secret || undefined,
        onebot_reverse_ws_endpoint: wsEndpoint.value,
        onebot_access_token: form.onebot_access_token.trim() || undefined
      };
  }
}

/** 保存成功后清掉明文密钥草稿：再次保存时留空即表示沿用后端已存的那份。 */
function clearSecretDrafts(): void {
  const form = botForm.value;
  form.onebot_access_token = "";
  form.onebot_http_secret = "";
  form.telegram_bot_token = "";
  form.qq_app_secret = "";
  form.dingtalk_client_secret = "";
  form.feishu_app_secret = "";
  form.feishu_verification_token = "";
  form.feishu_encrypt_key = "";
  form.wecom_secret = "";
  form.wecom_token = "";
  form.wecom_encoding_aes_key = "";
}

async function saveBotAndStart(): Promise<void> {
  const invalid = credentialError();
  if (invalid !== "") {
    toastError(invalid);
    return;
  }
  busy.value = true;
  try {
    const base = savedBot.value ?? (await getBotProfileConfig());
    const payload: BotProfileConfig = {
      ...base,
      enabled: true,
      platform: botForm.value.platform,
      bot_account: base.bot_account,
      owner_id: botForm.value.owner_id.trim(),
      profiles: undefined,
      ...platformPayload()
    };
    savedBot.value = await saveBotProfileConfig(payload);
    tokenConfigured.value = Boolean(savedBot.value.onebot_access_token_configured);
    await startBot();
    toastSuccess(`配置已保存，正在启动 ${platformName.value} 通道`);
    clearSecretDrafts();
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存失败");
  } finally {
    busy.value = false;
  }
}

onMounted(async () => {
  // 平台列表失败不该挡住整个向导：兜底的 OneBot 一条仍然可用。
  void getBotPlatforms()
    .then((result) => {
      if (result.platforms.length > 0) platforms.value = result.platforms;
    })
    .catch(() => undefined);
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
    // 老配置可能压根没存 platform，按 OneBot 处理，和后端的归一化一致。
    botForm.value.platform = bot.platform || PlatformOneBotV11;
    botForm.value.onebot_transport = bot.onebot_transport || "reverse_ws";
    botForm.value.onebot_ws_endpoint = bot.onebot_ws_endpoint || "";
    botForm.value.onebot_http_url = bot.onebot_http_url || "";
    botForm.value.onebot_reverse_ws_endpoint =
      bot.onebot_reverse_ws_endpoint || `ws://${window.location.host}/onebot/v11/ws`;
    // 密钥一律不回填——后端只回 *_configured，留空即沿用。
    botForm.value.telegram_api_base_url = bot.telegram_api_base_url || "";
    botForm.value.telegram_proxy_url = bot.telegram_proxy_url || "";
    botForm.value.qq_app_id = bot.qq_app_id || "";
    botForm.value.qq_sandbox = Boolean(bot.qq_sandbox);
    botForm.value.dingtalk_client_id = bot.dingtalk_client_id || "";
    botForm.value.dingtalk_robot_code = bot.dingtalk_robot_code || "";
    botForm.value.feishu_app_id = bot.feishu_app_id || "";
    botForm.value.feishu_api_base_url = bot.feishu_api_base_url || "";
    botForm.value.wecom_corp_id = bot.wecom_corp_id || "";
    botForm.value.wecom_agent_id = bot.wecom_agent_id || "";
    // 10001 was used by early demo data and should not appear as a real default.
    botForm.value.owner_id = bot.owner_id === "10001" ? "" : (bot.owner_id ?? "");
    if (llmConfigured.value && !connected.value) {
      step.value = 1;
    } else if (llmConfigured.value && connected.value) {
      step.value = 2;
    }
  } catch {
    /* 初次加载失败保持第一步 */
  } finally {
    loading.value = false;
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
useConfigurationRefresh(["bot"], async () => {
  savedBot.value = await getBotProfileConfig();
  tokenConfigured.value = Boolean(savedBot.value.onebot_access_token_configured);
});
useConfigurationRefresh(["llm"], async () => {
  savedLLM.value = await getConfig();
  llmConfigured.value = Boolean(savedLLM.value.api_key_configured);
});

</script>
