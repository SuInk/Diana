<script setup lang="ts">
import { computed, ref } from "vue";
import { codingAgentSetup } from "../api";
import AppSelect from "./AppSelect.vue";

interface AgentProfile {
  id: string;
  base_url?: string;
  managed_workspace?: boolean;
  backend: string;
  command: string;
  command_template: string;
  model: string;
  api_key_env: string;
  approval_mode: string;
  default: boolean;
}
const props = defineProps<{ modelValue?: AgentProfile[]; id: string; keyDrafts?: string }>();
const emit = defineEmits<{ (event: "update:modelValue", value: AgentProfile[]): void; (event: "update:key-drafts", value: string): void }>();
const busy = ref(false);
const logins = ref<Record<string,{url?:string;code?:string;state?:string}>>({});
const messages = ref<Record<string,string>>({});
function keys(): Record<string,string> { try { return JSON.parse(props.keyDrafts || "{}"); } catch { return {}; } }
function setKey(name: string, value: string): void { emit("update:key-drafts", JSON.stringify({...keys(), [name.toLowerCase()]:value})); }
async function setup(name: string, operation: "status" | "install" | "test" | "login-start" | "login-status" | "login-cancel"): Promise<void> {
  busy.value = true;
  messages.value[name] = operation === "install" ? "正在安装，可能需要几分钟…" : "正在检测…";
  try { const result = await codingAgentSetup(name,operation); if (result.login_state) logins.value[name]={url:result.login_url,code:result.device_code,state:result.login_state}; messages.value[name] = result.message + (result.key_configured ? "（已配置密钥）" : ""); }
  catch(error) { messages.value[name] = error instanceof Error ? error.message : "操作失败"; }
  finally { busy.value = false; }
}
const profiles = computed(() => Array.isArray(props.modelValue) ? props.modelValue : []);
const backends = [
  { value: "claude", label: "Claude Code" },
  { value: "codex", label: "Codex" },
  { value: "custom", label: "自定义命令" }
];
const approvals = [
  { value: "dangerous", label: "危险操作要确认（仅 Claude Code）" },
  { value: "all_writes", label: "所有写操作要确认（仅 Claude Code）" },
  { value: "off", label: "关闭聊天审批" }
];
function update(index: number, key: keyof AgentProfile, value: string | boolean): void {
  emit("update:modelValue", profiles.value.map((profile, i) => i === index ? { ...profile, [key]: value, ...(key === "backend" && value === "custom" ? { api_key_env: "" } : {}) } : { ...profile }));
}
function text(event: Event): string { return (event.target as HTMLInputElement).value; }
function setDefault(index: number): void {
  emit("update:modelValue", profiles.value.map((profile, i) => ({ ...profile, default: i === index })));
}
function add(): void {
  let n = 1;
  while (profiles.value.some(p => p.id.toLowerCase() === `agent-${n}`)) n++;
  emit("update:modelValue", [...profiles.value, { id: `agent-${n}`, managed_workspace: true, backend: "claude", command: "", command_template: "", model: "", api_key_env: "", approval_mode: "dangerous", default: false }]);
}
function remove(index: number): void {
  const profile = profiles.value[index];
  if (profile) setKey(profile.id, "");
  emit("update:modelValue", profiles.value.filter((_, i) => i !== index));
}
</script>

<template>
  <div :id="id" class="coding-profiles stack">
    <p class="hint">添加代理并填写 API 密钥，保存设置后点击「检测环境 → 安装 CLI → 测试连接」。安装文件保存在数据目录，Docker 重建后仍可用。测试连接会发起一次小额模型请求。</p>
    <label class="check-item">
      <input type="radio" :name="`${id}-default`" :checked="!profiles.some(p => p.default)" @change="setDefault(-1)" />
      使用下方原有配置作为默认代理（default）
    </label>
    <fieldset v-for="(profile, index) in profiles" :key="index" class="coding-profile stack">
      <legend>代理 {{ index + 1 }}</legend>
      <div class="cluster" style="justify-content: space-between">
        <label class="check-item">
          <input type="radio" :name="`${id}-default`" :checked="profile.default" @change="setDefault(index)" />
          设为默认代理
        </label>
        <button type="button" class="btn ghost small" :aria-label="`移除代理 ${profile.id}`" @click="remove(index)">移除</button>
      </div>
      <div class="coding-profile-grid">
        <label class="field">名称
          <input class="input" :value="profile.id" placeholder="例如 codex-main" @input="update(index, 'id', text($event))" />
        </label>
        <div class="field">
          <label :for="`${id}-${index}-backend`">后端 CLI</label>
          <AppSelect :id="`${id}-${index}-backend`" :model-value="profile.backend" :options="backends" @update:model-value="update(index, 'backend', $event)" />
        </div>
        <label class="field">模型
          <input class="input" :value="profile.model" placeholder="留空使用 CLI 默认模型" @input="update(index, 'model', text($event))" />
        </label>
        <label class="field">可执行文件
          <input class="input" :value="profile.command" placeholder="留空按后端查找，可填绝对路径" @input="update(index, 'command', text($event))" />
        </label>
      </div>
      <div class="field">
        <label :for="`${id}-${index}-approval`">聊天审批</label>
        <AppSelect :id="`${id}-${index}-approval`" :model-value="profile.approval_mode" :options="approvals" @update:model-value="update(index, 'approval_mode', $event)" />
        <span v-if="profile.backend !== 'claude'" class="hint">此后端不支持聊天审批，需明确选择关闭。Codex 预置命令会绕过 CLI 审批和沙箱。</span>
      </div>
      <template v-if="profile.backend !== 'custom'">
        <label class="field">API 服务地址
          <input class="input" :value="profile.base_url || ''" placeholder="留空使用官方服务" @input="update(index, 'base_url', text($event))" />
        </label>
        <label class="check-item"><input type="checkbox" :checked="profile.managed_workspace" @change="update(index, 'managed_workspace', ($event.target as HTMLInputElement).checked)" />自动创建持久工作目录（无需挂载代码仓库）</label>
        <label class="field">API 密钥
          <input class="input" type="password" autocomplete="off" :value="keys()[profile.id.toLowerCase()] || ''" placeholder="填写新密钥；未输入时保留已保存密钥" @input="setKey(profile.id, text($event))" />
        </label>
        <button type="button" class="btn ghost small" @click="setKey(profile.id, '')">清除已保存密钥（保存后生效）</button>
        <span v-if="keys()[profile.id.toLowerCase()] === ''" class="hint">保存时将清除这份配置的密钥。</span>
        <div class="cluster">
          <button type="button" class="btn small" :disabled="busy" @click="setup(profile.id, 'status')">检测环境</button>
          <button type="button" class="btn small" :disabled="busy" @click="setup(profile.id, 'install')">安装 CLI</button>
          <button type="button" class="btn small" :disabled="busy" @click="setup(profile.id, 'test')">测试连接</button>
        </div>
        <div v-if="profile.backend === 'codex'" class="cluster">
          <button type="button" class="btn small" :disabled="busy" @click="setup(profile.id, 'login-start')">使用 ChatGPT 登录</button>
          <button v-if="logins[profile.id]?.state === 'pending'" type="button" class="btn small" :disabled="busy" @click="setup(profile.id, 'login-status')">刷新登录状态</button>
          <button v-if="logins[profile.id]?.state === 'pending'" type="button" class="btn ghost small" :disabled="busy" @click="setup(profile.id, 'login-cancel')">取消登录</button>
        </div>
        <p v-if="logins[profile.id]?.url" class="hint"><a :href="logins[profile.id]?.url" target="_blank" rel="noreferrer">打开 ChatGPT 登录页面</a> · 一次性代码：<strong>{{ logins[profile.id]?.code || '申请中，请刷新状态' }}</strong></p>
        <p v-if="messages[profile.id]" class="hint" role="status">{{ messages[profile.id] }}</p>
        <p class="hint">这些按钮使用已保存配置；修改名称或凭据后请先保存。密钥留空可使用服务端已有登录。</p>
      </template>
      <details>
        <summary>高级配置</summary>
        <div class="stack" style="margin-top: 12px">
          <label class="field">命令模板
            <textarea class="input" rows="3" :value="profile.command_template" placeholder="留空使用预置命令，自定义后端必填" @input="update(index, 'command_template', text($event))"></textarea>
            <span class="hint">模板需包含 <code v-text="'{{instruction}}'"></code>，其他占位符与原有配置一致。</span>
          </label>
          <label v-if="profile.backend !== 'custom'" class="field">密钥环境变量名
            <input class="input" :value="profile.api_key_env" placeholder="例如 DIANA_CODEX_KEY（不要填写密钥本身）" @input="update(index, 'api_key_env', text($event))" />
            <span class="hint">留空使用 CLI 已有登录或环境配置；额外代理不继承下方 API 密钥。</span>
          </label>
        </div>
      </details>
    </fieldset>
    <button class="btn small" type="button" :disabled="profiles.length >= 16" @click="add">添加编码代理</button>
  </div>
</template>

<style scoped>
.coding-profile { border: 1px solid var(--border); border-radius: 10px; padding: 14px; min-width: 0; }
.coding-profile legend { padding: 0 6px; font-size: 13px; }
.coding-profile-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.coding-profile summary { cursor: pointer; font-size: 13px; }
@media (max-width: 600px) { .coding-profile-grid { grid-template-columns: 1fr; } }
</style>
