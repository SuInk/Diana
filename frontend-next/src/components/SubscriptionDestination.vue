<template>
  <span class="subscription-destination">
    <strong v-if="profileName">{{ profileName }}</strong>
    <span>{{ subscriptionPlatformLabel(platform) }} · {{ groupId ? "群聊" : "私聊" }}</span>
    <strong v-if="name">{{ name }}</strong>
    <span class="mono">（{{ groupId || userId || "—" }}）</span>
  </span>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import { fetchAssistantUserNames, listBotGroups } from "../api";
import { subscriptionPlatformLabel } from "../rss-display";

const props = defineProps<{ platform?: string; profileId?: string; profileName?: string; groupId?: string; userId?: string }>();
const name = ref("");
let generation = 0;
watch(() => [props.profileId, props.platform, props.groupId, props.userId], async () => {
  const current = ++generation;
  name.value = "";
  const profile = props.profileId;
  const group = props.groupId;
  const user = props.userId;
  // An old task without a profile must never borrow another bot's nickname.
  if (!profile || (!group && !user)) return;
  try {
    let resolved = "";
    if (group) {
      const result = await listBotGroups(false, profile);
      resolved = result.groups.find((item) => item.group_id === group && (!item.bot_profile_id || item.bot_profile_id === profile))?.group_name ?? "";
    } else if (user) {
      resolved = (await fetchAssistantUserNames([user], profile)).names?.[user] ?? "";
    }
    if (current === generation) name.value = resolved;
  } catch { /* Keep the platform and ID when a name is unavailable. */ }
}, { immediate: true });
onBeforeUnmount(() => { generation += 1; });
</script>

<style scoped>
.subscription-destination { display: inline-flex; flex-wrap: wrap; gap: 4px; min-width: 0; max-width: 100%; overflow-wrap: anywhere; }
</style>
