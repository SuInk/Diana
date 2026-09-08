<template>
  <div class="subscription-targets">
    <div v-for="(target, index) in modelValue" :key="index" class="subscription-target-row">
      <AppSelect :model-value="target.profile_id || ''" :options="profileOptions" aria-label="推送机器人" @update:model-value="update(index, { profile_id: String($event), group_id: '', user_id: '' })" />
      <AppSelect :model-value="target.destination" :options="destinationOptions" aria-label="通知对象类型" @update:model-value="update(index, { destination: $event as 'group' | 'private', group_id: '', user_id: '' })" />
      <div class="subscription-target-account">
        <AppSelect v-if="target.destination === 'group' && groupOptions(target.profile_id).length" :model-value="target.group_id || ''" :options="groupOptions(target.profile_id)" aria-label="通知群聊" @update:model-value="update(index, { group_id: String($event) })" />
        <input v-else class="input" :value="target.destination === 'group' ? target.group_id : target.user_id" :aria-label="target.destination === 'group' ? '通知群聊 ID' : '通知私聊 ID'" :placeholder="target.destination === 'group' ? '群号或 Chat ID' : '私聊对象 ID'" @input="update(index, { [target.destination === 'group' ? 'group_id' : 'user_id']: ($event.target as HTMLInputElement).value.trim() })" />
        <AccountNameHint v-if="target.destination === 'private'" :user-id="target.user_id" :profile="target.profile_id" />
      </div>
      <button class="btn small ghost danger icon-only" type="button" title="移除通知目标" aria-label="移除通知目标" @click="emit('update:modelValue', modelValue.filter((_, i) => i !== index))"><Trash2 :size="14" /></button>
    </div>
    <button class="btn small ghost" type="button" :disabled="modelValue.length >= 50" @click="emit('update:modelValue', [...modelValue, { profile_id: defaultProfile || profiles[0]?.id, destination: 'private', user_id: '' }])"><Plus :size="14" />添加通知目标</button>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { Plus, Trash2 } from "@lucide/vue";
import type { BotProfileConfig, BotGroupSummary, RepositoryWatchTarget } from "../api";
import AppSelect from "./AppSelect.vue";
import AccountNameHint from "./AccountNameHint.vue";

const props = defineProps<{ modelValue: RepositoryWatchTarget[]; profiles: BotProfileConfig[]; groups: BotGroupSummary[]; defaultProfile?: string }>();
const emit = defineEmits<{ 'update:modelValue': [RepositoryWatchTarget[]] }>();
const profileOptions = computed(() => props.profiles.map(p => ({ value: p.id || '', label: p.name || p.id || '', hint: p.platform })));
const destinationOptions = [{ value: 'private', label: '私聊' }, { value: 'group', label: '群聊' }];
function groupOptions(profile?: string) {
	if (!profile) return [];
  return props.groups.filter(g => g.joined && g.bot_profile_id === profile).map(g => ({ value: g.group_id, label: g.group_name ? `${g.group_name}（${g.group_id}）` : g.group_id }));
}
function update(index: number, patch: Partial<RepositoryWatchTarget>) {
  emit('update:modelValue', props.modelValue.map((target, i) => i === index ? { ...target, ...patch } : target));
}
</script>

<style scoped>
.subscription-targets { display: grid; gap: 12px; }
.subscription-target-row { display: grid; grid-template-columns: minmax(0, 1fr) 88px minmax(0, 1fr) 32px; align-items: start; gap: 8px; }
.subscription-target-account { min-width: 0; }
.subscription-target-account .input { width: 100%; }
@media (max-width: 640px) { .subscription-target-row { grid-template-columns: minmax(0, 1fr) 88px 32px; } .subscription-target-account { grid-row: 2; grid-column: 1 / 3; } .subscription-target-row > button { grid-row: 1; grid-column: 3; } }
</style>
