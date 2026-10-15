<template>
  <span v-if="resolved.length" class="github-user-hint">
    <span v-for="user in resolved" :key="user.login" :title="`@${user.login}`"><UserRound :size="12" aria-hidden="true" />{{ user.name || user.login }} <span v-if="user.name">(@{{ user.login }})</span></span>
  </span>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { UserRound } from "@lucide/vue";
import { lookupGitHubUser, type GitHubUserIdentity } from "../api";
import { githubUserLogins, isGitHubLogin } from "../lib/github-user-input";

const props = defineProps<{ value: string }>();
const names = ref<Record<string, GitHubUserIdentity>>({});
const logins = computed(() => githubUserLogins(props.value).filter(isGitHubLogin).slice(0, 10));
const resolved = computed(() => logins.value.flatMap(login => names.value[login] ? [names.value[login]] : []));
let timer: ReturnType<typeof setTimeout> | undefined;
let revision = 0;
watch(logins, () => {
  clearTimeout(timer);
  const current = ++revision;
  timer = setTimeout(async () => {
    for (const login of logins.value) {
      if (current !== revision) return;
      if (names.value[login]) continue;
      try {
        const result = await lookupGitHubUser(login);
        if (current === revision && result.user) names.value[login] = result.user;
      } catch { /* Name hints are optional; lookup failure must not block saving. */ }
    }
  }, 400);
}, { immediate: true });
onBeforeUnmount(() => { clearTimeout(timer); revision++; });
</script>

<style scoped>
.github-user-hint { display: flex; flex-wrap: wrap; gap: 4px 12px; color: var(--muted); font-size: 11.5px; }
.github-user-hint > span { display: inline-flex; align-items: center; gap: 4px; overflow-wrap: anywhere; }
</style>
