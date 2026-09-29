<template>
  <div class="subscription-targets">
    <div v-for="(target, index) in modelValue" :key="index" class="subscription-target-row">
      <AppSelect :model-value="target.profile_id || ''" :options="profileOptions" aria-label="推送机器人" @update:model-value="update(index, { profile_id: String($event), group_id: '', user_id: '' })" />
      <AppSelect :model-value="target.destination" :options="destinationOptions" aria-label="通知对象类型" @update:model-value="update(index, { destination: $event as 'group' | 'private', group_id: '', user_id: '' })" />
      <div class="subscription-target-account">
        <AppSelect v-if="target.destination === 'group' && groupOptions(target.profile_id).length" :model-value="target.group_id || ''" :options="groupOptions(target.profile_id)" aria-label="通知群聊" @update:model-value="update(index, { group_id: String($event) })" />
        <input v-else class="input" :value="target.destination === 'group' ? target.group_id : target.user_id" :aria-label="target.destination === 'group' ? '通知群聊 ID' : '通知私聊 ID'" :placeholder="target.destination === 'group' ? '群号或 Chat ID' : '私聊对象 ID'" @input="update(index, { [target.destination === 'group' ? 'group_id' : 'user_id']: ($event.target as HTMLInputElement).value.trim() })" />
        <AccountNameHint v-if="target.destination === 'private'" :user-id="target.user_id" :profile="target.profile_id" />
        <p v-else-if="groupNotice(target)" class="hint warn-text subscription-target-notice">{{ groupNotice(target) }}</p>
      </div>
      <button class="btn small ghost danger icon-only" type="button" title="移除通知目标" aria-label="移除通知目标" @click="emit('update:modelValue', modelValue.filter((_, i) => i !== index))"><Trash2 :size="14" /></button>
    </div>
    <button class="btn small ghost" type="button" :disabled="modelValue.length >= 50" @click="emit('update:modelValue', [...modelValue, { profile_id: defaultProfile || profiles[0]?.id, destination: 'private', user_id: '' }])"><Plus :size="14" />添加通知目标</button>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, watch } from "vue";
import { Plus, Trash2 } from "@lucide/vue";
import { listBotGroups, type BotProfileConfig, type BotGroupSummary, type RepositoryWatchTarget } from "../api";
import AppSelect from "./AppSelect.vue";
import AccountNameHint from "./AccountNameHint.vue";

// pluginIds 是发这条推送的插件：用来判断目标群是不是单独关了它。
const props = defineProps<{ modelValue: RepositoryWatchTarget[]; profiles: BotProfileConfig[]; defaultProfile?: string; pluginIds?: string[] }>();
const emit = defineEmits<{ 'update:modelValue': [RepositoryWatchTarget[]] }>();
const profileOptions = computed(() => props.profiles.map(p => ({ value: p.id || '', label: p.name || p.id || '', hint: p.platform })));
const destinationOptions = [{ value: 'private', label: '私聊' }, { value: 'group', label: '群聊' }];
// 按机器人各取各的群。「全部机器人」那份列表按群号去重，两台机器人同在一个群时
// 只记在其中一台名下，另一台的下拉框就少了这个群。
const groupsByProfile = reactive<Record<string, BotGroupSummary[]>>({});
watch(() => props.modelValue.filter(t => t.destination === 'group' && t.profile_id).map(t => t.profile_id as string), (ids) => {
  for (const id of new Set(ids)) {
    if (id in groupsByProfile) continue;
    groupsByProfile[id] = [];
    listBotGroups(false, id).then(result => { groupsByProfile[id] = result.groups ?? []; }).catch(() => { delete groupsByProfile[id]; });
  }
}, { immediate: true });
function groupOptions(profile?: string) {
  if (!profile) return [];
  return (groupsByProfile[profile] ?? []).filter(g => g.joined).map(g => ({ value: g.group_id, label: g.group_name ? `${g.group_name}（${g.group_id}）` : g.group_id }));
}
// groupNotice 提醒目标群的状态和推送不一致的地方。推送只在整台机器人停用时拦截，
// 群关着、或者群里单独关了这个插件，推送照样发到群里：不说一句，看起来像是
// 「群关了就不会收到」。
function groupNotice(target: RepositoryWatchTarget): string {
  if (target.destination !== 'group' || !target.profile_id || !target.group_id) return '';
  const group = (groupsByProfile[target.profile_id] ?? []).find(g => g.group_id === target.group_id);
  if (!group) return '';
  if (!group.enabled) return '这个群已关闭，机器人不在群里回复，但订阅推送仍会发到这里。';
  if ((props.pluginIds ?? []).some(id => group.plugin_overrides?.[id] === false)) return '这个群单独关了本插件，群里对话用不了它，但订阅推送仍会发到这里。';
  return '';
}
function update(index: number, patch: Partial<RepositoryWatchTarget>) {
  emit('update:modelValue', props.modelValue.map((target, i) => i === index ? { ...target, ...patch } : target));
}
</script>

<style scoped>
.subscription-targets { display: grid; gap: 12px; }
/* 私聊昵称拆到第二行，第一行的下拉、输入框和删除按钮才能按同一条中线对齐。 */
.subscription-target-row { display: grid; grid-template-columns: minmax(0, 1fr) 88px minmax(0, 1fr) 32px; align-items: center; gap: 4px 8px; }
.subscription-target-account { display: contents; }
.subscription-target-account > .account-name-hint, .subscription-target-account > .subscription-target-notice { grid-row: 2; grid-column: 3; }
.subscription-target-notice { margin: 0; }
.subscription-target-account .input { width: 100%; }
@media (max-width: 640px) { .subscription-target-row { grid-template-columns: minmax(0, 1fr) 88px 32px; } .subscription-target-account > :not(.account-name-hint):not(.subscription-target-notice) { grid-row: 2; grid-column: 1 / 3; } .subscription-target-account > .account-name-hint, .subscription-target-account > .subscription-target-notice { grid-row: 3; grid-column: 1 / 3; } .subscription-target-row > button { grid-row: 1; grid-column: 3; } }
</style>
