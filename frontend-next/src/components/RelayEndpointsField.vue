<template>
  <section class="relay-endpoints">
    <div class="relay-head">
      <div><label>{{ spec.label }}</label><p v-if="spec.description" class="hint">{{ spec.description }}</p></div>
      <button class="btn small" type="button" @click="addEndpoint"><Plus :size="14" />添加端点</button>
    </div>
    <div v-if="endpoints.length" class="relay-list">
      <article v-for="(endpoint, index) in endpoints" :key="index" class="relay-row">
        <div class="field">
          <label>机器人 / 平台</label>
          <AppSelect :model-value="endpoint.profile_id" :options="profileOptions" @update:model-value="selectProfile(index, String($event))" />
        </div>
        <div class="field">
          <label>会话类型</label>
          <AppSelect :model-value="endpoint.kind" :options="kindOptions" @update:model-value="update(index, { kind: String($event) as RelayKind, target_id: '' })" />
        </div>
        <div class="field">
          <label>{{ endpoint.kind === 'group' ? '群聊' : '用户 ID' }}</label>
          <AppSelect v-if="endpoint.kind === 'group' && groupOptions(endpoint.profile_id).length" :model-value="endpoint.target_id" :options="groupOptions(endpoint.profile_id)" @update:model-value="update(index, { target_id: String($event) })" />
          <input v-else :value="endpoint.target_id" class="input" :placeholder="endpoint.kind === 'group' ? '群号 / Chat ID' : '用户 ID / Chat ID'" @input="update(index, { target_id: ($event.target as HTMLInputElement).value })" />
        </div>
        <button class="btn ghost icon-only danger relay-delete" type="button" aria-label="删除端点" @click="remove(index)"><Trash2 :size="15" /></button>
      </article>
    </div>
    <p v-else class="relay-empty">尚未配置。添加至少两个端点后再启用插件。</p>
    <p v-if="endpoints.length === 1" class="hint warn-text">还需要一个端点才能互通。</p>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { Plus, Trash2 } from "@lucide/vue";
import AppSelect from "./AppSelect.vue";
import { getBotProfileConfig, listBotGroups, type BotGroupSummary, type BotProfileConfig, type PluginSettingSpec } from "../api";

type RelayKind = "group" | "private";
interface RelayEndpoint { profile_id: string; platform: string; kind: RelayKind; target_id: string }
const props = defineProps<{ spec: PluginSettingSpec; modelValue?: unknown }>();
const emit = defineEmits<{ "update:modelValue": [RelayEndpoint[]] }>();
const profiles = ref<BotProfileConfig[]>([]);
const groups = ref<Record<string, BotGroupSummary[]>>({});
const endpoints = computed(() => Array.isArray(props.modelValue) ? props.modelValue as RelayEndpoint[] : []);
const profileOptions = computed(() => profiles.value.filter(item => item.id && item.platform).map(item => ({ value: String(item.id), label: `${item.name || item.id} · ${platformLabel(item.platform || "")}` })));
const kindOptions = [{ value: "group", label: "群聊" }, { value: "private", label: "私聊" }];

onMounted(async () => {
  try {
    const config = await getBotProfileConfig();
    profiles.value = config.profiles?.length ? config.profiles : [config];
    await Promise.all(profiles.value.filter(item => item.id).map(async item => {
      try { groups.value[String(item.id)] = (await listBotGroups(false, String(item.id))).groups ?? []; } catch { groups.value[String(item.id)] = []; }
    }));
  } catch { profiles.value = []; }
});

function addEndpoint(): void {
  const profile = profiles.value[0];
  emit("update:modelValue", [...endpoints.value, { profile_id: String(profile?.id ?? ""), platform: String(profile?.platform ?? ""), kind: "group", target_id: "" }]);
}
function remove(index: number): void { emit("update:modelValue", endpoints.value.filter((_, i) => i !== index)); }
function update(index: number, patch: Partial<RelayEndpoint>): void { emit("update:modelValue", endpoints.value.map((item, i) => i === index ? { ...item, ...patch } : item)); }
function selectProfile(index: number, profileID: string): void {
  const profile = profiles.value.find(item => item.id === profileID);
  update(index, { profile_id: profileID, platform: String(profile?.platform ?? ""), target_id: "" });
}
function groupOptions(profileID: string) { return (groups.value[profileID] ?? []).filter(item => item.joined).map(item => ({ value: item.group_id, label: item.group_name ? `${item.group_name} · ${item.group_id}` : item.group_id })); }
function platformLabel(platform: string): string { return ({ "onebot-v11": "QQ", telegram: "Telegram", "qq-official": "QQ 官方", dingtalk: "钉钉", feishu: "飞书", wecom: "企业微信" } as Record<string, string>)[platform] ?? platform; }
</script>

<style scoped>
.relay-endpoints { display: grid; gap: 12px; }
.relay-head { display: flex; justify-content: space-between; align-items: flex-start; gap: 16px; }
.relay-head p, .relay-empty { margin: 4px 0 0; }
.relay-list { display: grid; gap: 10px; }
.relay-row { display: grid; grid-template-columns: minmax(180px, 1.2fr) minmax(110px, .6fr) minmax(180px, 1fr) 34px; gap: 10px; align-items: end; padding: 12px; border: 1px solid var(--border); border-radius: var(--radius-md); }
.relay-delete { width: 34px; height: 34px; }
.relay-empty { color: var(--text-muted); font-size: 13px; }
.warn-text { color: var(--warning); }
@media (max-width: 760px) { .relay-head { flex-direction: column; } .relay-row { grid-template-columns: 1fr 1fr; } .relay-delete { justify-self: end; } }
</style>
