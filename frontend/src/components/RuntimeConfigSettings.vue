<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="stack runtime-config">
    <!-- 网络放最上面：最常来这页的原因是「从别的设备该用哪个地址连」。 -->
    <section class="card">
      <div class="card-header">
        <div><h2>访问地址</h2><span class="card-sub">本机网卡上的地址和服务监听的端口。</span></div>
        <button class="btn small ghost" type="button" :disabled="networkLoading" aria-label="刷新访问地址" @click="loadNetwork(false)"><RefreshCw :size="14" /></button>
      </div>
      <div class="card-body stack">
        <LoadingSkeleton v-if="networkLoading && !network" kind="form" :count="3" label="正在探测本机地址" />
        <div v-else-if="networkError" class="operation-error" role="alert">{{ networkError }} <button class="btn small" @click="loadNetwork(false)">重试</button></div>
        <template v-else-if="network">
          <div class="info-row"><span class="muted info-label">监听</span><span class="mono info-value">{{ network.listen_host || "所有地址" }} : {{ network.port }}</span></div>
          <div class="info-row"><span class="muted info-label">主机名</span><span class="mono info-value">{{ network.hostname || "—" }}</span></div>
          <p v-if="network.container" class="hint">运行在 Docker 容器里：下面是容器内部的地址，从局域网访问要用宿主机地址和映射出来的端口。</p>
          <div v-for="group in addressGroups" :key="group.scope" class="address-group">
            <span class="address-group-label">{{ group.label }}</span>
            <div v-for="addr in group.items" :key="addr.interface + addr.ip" class="address-row">
              <span class="mono address-ip">{{ addr.ip }}</span>
              <span class="muted address-iface">{{ addr.interface }} · {{ addr.family }}</span>
              <span class="address-url">
                <a v-if="addr.url" :href="addr.url" target="_blank" rel="noopener" class="mono">{{ addr.url }}</a>
                <span v-else class="muted">{{ addr.scope === "link_local" ? "链路本地，不可直接访问" : "未监听此地址" }}</span>
              </span>
              <button v-if="addr.url" class="btn small ghost" type="button" :aria-label="`复制 ${addr.url}`" @click="copy(addr.url)"><Copy :size="13" /></button>
            </div>
          </div>
          <div class="address-group">
            <span class="address-group-label">公网出口</span>
            <div class="address-row">
              <template v-if="network.public?.ip">
                <span class="mono address-ip">{{ network.public.ip }}</span>
                <span class="muted address-iface">{{ formatTime(network.public.checked_at) }}</span>
                <span class="address-url"><span class="mono">{{ network.public.url }}</span></span>
              </template>
              <span v-else-if="network.public?.error" class="operation-error address-url">{{ network.public.error }}</span>
              <span v-else class="muted address-url">探测会访问 ipify 等外部服务，默认不查。</span>
              <button class="btn small" type="button" :disabled="networkLoading" @click="loadNetwork(true)">{{ network.public ? "重新探测" : "探测公网地址" }}</button>
            </div>
            <p class="hint">公网出口地址只说明从外面看你是谁；能不能从外网连进来，还取决于路由器端口映射和防火墙。</p>
          </div>
        </template>
      </div>
    </section>

    <section class="card">
      <div class="card-header">
        <div><h2>配置文件</h2><span class="card-sub mono">{{ snapshot?.config_path || "—" }}{{ snapshot && !snapshot.config_exists ? "（尚未创建，保存时生成）" : "" }}</span></div>
        <button class="btn small ghost" type="button" :disabled="loading" aria-label="刷新配置" @click="load"><RefreshCw :size="14" /></button>
      </div>
      <div class="card-body stack">
        <LoadingSkeleton v-if="loading && !snapshot" kind="form" :count="4" label="正在加载启动配置" />
        <div v-else-if="error" class="operation-error" role="alert">{{ error }} <button class="btn small" @click="load">重试</button></div>
        <template v-else-if="snapshot">
          <p v-if="snapshot.restart_needed" class="restart-banner" role="status">配置文件已修改，标着「待重启」的项要在「运行状态」里重启服务后才生效。</p>
          <p class="hint">这些项在启动时读取，保存只写入配置文件，重启服务后生效。标「谨慎」的项改错会让控制台连不上或换掉数据位置，保存前会再确认。机器人设置和模型以数据库为准，不在这里。</p>
          <div v-for="section in sections" :key="section.name" class="config-section">
            <h3>{{ sectionLabel(section.name) }}</h3>
            <div v-for="item in section.items" :key="item.key" class="config-row">
              <div class="config-meta">
                <label :for="inputId(item)" class="config-label">{{ item.label }}</label>
                <span class="mono muted config-key">{{ item.key }}</span>
                <span class="badge" :class="item.source === 'config_file' ? 'ok' : ''">{{ sourceLabel(item.source) }}</span>
                <span v-if="item.pending" class="badge warn">待重启</span>
                <span v-if="item.risky" class="badge warn" title="改错会让控制台连不上或换掉数据位置">谨慎</span>
                <span v-if="item.bot_editable" class="badge" title="主人可以在私聊里让机器人修改">机器人可改</span>
              </div>
              <div class="config-control">
                <template v-if="item.editable">
                  <AppSwitch v-if="item.kind === 'bool'" :id="inputId(item)" :model-value="boolDraft(item)" @update:model-value="draft[item.key] = String($event)">{{ boolDraft(item) ? "开启" : "关闭" }}</AppSwitch>
                  <textarea v-else-if="item.kind === 'list'" :id="inputId(item)" v-model="draft[item.key]" class="input mono" rows="3" :placeholder="item.default || '每行一个'" />
                  <input v-else :id="inputId(item)" v-model="draft[item.key]" class="input mono" :inputmode="item.kind === 'int' || item.kind === 'port' ? 'numeric' : undefined" :placeholder="item.default ? `默认 ${item.default}` : '留空用默认值'" />
                </template>
                <span v-else :id="inputId(item)" class="mono config-readonly">{{ displayValue(item) }}</span>
                <span v-if="item.pending" class="hint">运行中：<span class="mono">{{ item.value || "（空）" }}</span></span>
                <span v-if="item.hint" class="hint">{{ item.hint }}</span>
              </div>
            </div>
          </div>
          <div class="cluster config-actions">
            <button class="btn" type="button" :disabled="!dirtyKeys.length || saving" @click="resetDraft">撤销修改</button>
            <button class="btn primary" type="button" :disabled="!dirtyKeys.length || saving" @click="save"><Save :size="14" />{{ saving ? "保存中…" : dirtyKeys.length ? `保存 ${dirtyKeys.length} 项` : "保存" }}</button>
          </div>
        </template>
      </div>
    </section>

    <section v-if="snapshot" class="card">
      <div class="card-header"><div><h2>环境变量与启动参数</h2><span class="card-sub">属于进程本身，控制台和机器人都改不了，要改得改启动脚本、服务文件或 Docker 配置。</span></div></div>
      <div class="card-body stack">
        <div class="info-row"><span class="muted info-label">启动参数</span><code class="info-value">{{ snapshot.args.length ? snapshot.args.join(" ") : "（无）" }}</code></div>
        <div class="env-list">
          <div v-for="env in sortedEnv" :key="env.name" class="env-row" :class="{ 'env-unset': !env.set }">
            <span class="mono">{{ env.name }}</span>
            <span class="muted">{{ env.label }}</span>
            <span class="mono env-value">{{ !env.set ? "未设置" : env.secret ? "已设置" : env.value }}</span>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { Copy, RefreshCw, Save } from "@lucide/vue";
import AppSwitch from "./AppSwitch.vue";
import LoadingSkeleton from "./LoadingSkeleton.vue";
import { getNetworkInfo, getRuntimeConfig, saveRuntimeConfig, type NetworkSnapshot, type RuntimeConfigItem, type RuntimeConfigSnapshot } from "../api";
import { askConfirm } from "../lib/confirm";
import { formatTime } from "../lib/format";
import { toastError, toastSuccess } from "../lib/toast";

const snapshot = ref<RuntimeConfigSnapshot | null>(null);
const loading = ref(false);
const saving = ref(false);
const error = ref("");
const draft = reactive<Record<string, string>>({});
const network = ref<NetworkSnapshot | null>(null);
const networkLoading = ref(false);
const networkError = ref("");

const sectionNames: Record<string, string> = { server: "服务", storage: "存储与日志", admin: "初始管理员", update: "更新", llm_runtime: "模型调用" };
const sectionLabel = (name: string) => sectionNames[name] ?? name;
const sourceLabel = (source: RuntimeConfigItem["source"]) => ({ default: "默认值", config_file: "配置文件", env: "环境变量", arg: "启动参数" })[source];
const inputId = (item: RuntimeConfigItem) => `rc-${item.key.replace(/\./g, "-")}`;
const displayValue = (item: RuntimeConfigItem) => item.value || (item.default ? `默认 ${item.default}` : "（空）");
const boolDraft = (item: RuntimeConfigItem) => (draft[item.key] || item.default) === "true";

const sections = computed(() => {
  const groups: { name: string; items: RuntimeConfigItem[] }[] = [];
  for (const item of snapshot.value?.items ?? []) {
    let group = groups.find((g) => g.name === item.section);
    if (!group) groups.push((group = { name: item.section, items: [] }));
    group.items.push(item);
  }
  return groups;
});
const dirtyKeys = computed(() => (snapshot.value?.items ?? []).filter((item) => item.editable && (draft[item.key] ?? "") !== fileDraft(item)).map((item) => item.key));
const sortedEnv = computed(() => [...(snapshot.value?.env ?? [])].sort((a, b) => Number(b.set) - Number(a.set)));

const scopeLabels = { loopback: "本机", lan: "局域网", public: "网卡上的公网地址", link_local: "链路本地" } as const;
const addressGroups = computed(() =>
  (["loopback", "lan", "public", "link_local"] as const)
    .map((scope) => ({ scope, label: scopeLabels[scope], items: (network.value?.addresses ?? []).filter((addr) => addr.scope === scope) }))
    .filter((group) => group.items.length)
);

// 草稿用文件里的值而不是运行值：改过没重启的项再打开，看到的应该是已经写进去的那份。
function fileDraft(item: RuntimeConfigItem) {
  return item.file_set ? item.file_value : "";
}
function resetDraft() {
  for (const key of Object.keys(draft)) delete draft[key];
  for (const item of snapshot.value?.items ?? []) if (item.editable) draft[item.key] = fileDraft(item);
}

async function load() {
  loading.value = true;
  error.value = "";
  try {
    snapshot.value = await getRuntimeConfig();
    resetDraft();
  } catch (e) {
    error.value = e instanceof Error ? e.message : "加载失败";
  } finally {
    loading.value = false;
  }
}

async function loadNetwork(withPublic: boolean) {
  networkLoading.value = true;
  networkError.value = "";
  try {
    // 已经探测过公网的，刷新时继续带上，不让结果凭空消失。
    const wantPublic = withPublic || !!network.value?.public;
    network.value = await getNetworkInfo({ public: wantPublic, force: withPublic });
  } catch (e) {
    networkError.value = e instanceof Error ? e.message : "探测失败";
  } finally {
    networkLoading.value = false;
  }
}

async function save() {
  const values: Record<string, string> = {};
  for (const key of dirtyKeys.value) values[key] = draft[key] ?? "";
  const items = snapshot.value?.items ?? [];
  const risky = items.filter((item) => item.risky && item.key in values);
  if (risky.length) {
    const names = risky.map((item) => item.label).join("、");
    const keys = risky.map((item) => item.key);
    const notes: string[] = [];
    if (keys.some((key) => key.startsWith("server."))) notes.push("重启后要用新的地址或端口访问控制台，Docker 部署要同步改端口映射。");
    if (keys.some((key) => key.startsWith("storage.") || key.startsWith("update."))) notes.push("不会自动搬旧数据，新位置没有数据时重启后就是空的，请先自己复制过去。");
    const message = `即将修改：${names}。${notes.join("")}重启后连不上控制台时，只能直接编辑 ${snapshot.value?.config_path || "config.yaml"} 改回来。`;
    if (!(await askConfirm({ title: "确认修改关键配置", message, confirmLabel: "写入配置文件", danger: true }))) return;
  } else if (Object.keys(values).some((key) => key === "server.trusted_proxies" || key.startsWith("update."))) {
    if (!(await askConfirm({ title: "保存启动配置", message: "信任代理和更新开关影响登录来源判定与能否升级，确认写入配置文件？重启后生效。", confirmLabel: "保存" }))) return;
  }
  saving.value = true;
  try {
    snapshot.value = await saveRuntimeConfig(values);
    resetDraft();
    toastSuccess("已写入配置文件，重启服务后生效");
  } catch (e) {
    toastError(e instanceof Error ? e.message : "保存失败");
  } finally {
    saving.value = false;
  }
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text);
    toastSuccess("已复制");
  } catch {
    toastError("复制失败");
  }
}

onMounted(() => {
  void load();
  void loadNetwork(false);
});
</script>

<style scoped>
.address-group { display: flex; flex-direction: column; gap: 6px; }
.address-group-label { font-size: 12px; font-weight: 600; color: var(--text-muted, var(--muted)); }
.address-row { display: grid; grid-template-columns: minmax(110px, 0.9fr) minmax(90px, 0.7fr) minmax(0, 2fr) auto; align-items: center; gap: 10px; font-size: 13px; }
.address-ip { font-weight: 600; word-break: break-all; }
.address-iface { font-size: 12px; }
.address-url { min-width: 0; word-break: break-all; }
.restart-banner { margin: 0; padding: 8px 12px; border-radius: 8px; background: var(--warn-bg, rgba(234, 179, 8, 0.12)); font-size: 13px; }
.config-section { display: flex; flex-direction: column; gap: 10px; }
.config-section h3 { margin: 6px 0 0; font-size: 14px; }
.config-row { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.2fr); gap: 12px; align-items: start; padding: 8px 0; border-top: 1px solid var(--border); }
.config-meta { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.config-label { font-weight: 500; font-size: 13px; }
.config-key { font-size: 11.5px; width: 100%; }
.config-control { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.config-readonly { font-size: 13px; padding: 6px 0; color: var(--text-muted, var(--muted)); word-break: break-all; }
.config-actions { justify-content: flex-end; }
.env-list { display: flex; flex-direction: column; }
.env-row { display: grid; grid-template-columns: minmax(0, 1.2fr) minmax(0, 1fr) minmax(0, 1.2fr); gap: 10px; padding: 6px 0; border-top: 1px solid var(--border); font-size: 12.5px; }
.env-row.env-unset { opacity: 0.55; }
.env-value { word-break: break-all; }
@media (max-width: 720px) {
  .config-row, .env-row { grid-template-columns: minmax(0, 1fr); gap: 4px; }
  .address-row { grid-template-columns: minmax(0, 1fr) auto; }
  .address-iface, .address-url { grid-column: 1 / -1; }
}
</style>
