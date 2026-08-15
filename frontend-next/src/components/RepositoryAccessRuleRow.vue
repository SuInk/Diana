<template>
  <div class="repository-access-rule">
    <div class="repository-access-rule-main">
      <input
        class="input mono repository-access-id"
        type="text"
        inputmode="numeric"
        :value="rule.id"
        :placeholder="kind === 'user' ? '用户 ID' : '群 ID'"
        :aria-label="kind === 'user' ? '用户 ID' : '群 ID'"
        @input="updateID"
      />
      <div class="repository-access-choices">
        <label
          v-for="repository in repositories"
          :key="repository"
          class="repository-access-choice"
          :class="{ active: rule.repositories.includes(repository) }"
        >
          <input
            type="checkbox"
            :checked="rule.repositories.includes(repository)"
            @change="toggle(repository, $event)"
          />
          <span class="mono">{{ repository }}</span>
        </label>
      </div>
    </div>
    <button class="btn small ghost danger icon-only" type="button" title="删除授权" aria-label="删除授权" @click="$emit('remove')">
      <Trash2 :size="14" aria-hidden="true" />
    </button>
  </div>
</template>

<script setup lang="ts">
import { Trash2 } from "@lucide/vue";

export type RepositoryAccessRule = { id: string; repositories: string[] };

const props = defineProps<{
  kind: "user" | "group";
  rule: RepositoryAccessRule;
  repositories: string[];
}>();
const emit = defineEmits<{
  update: [RepositoryAccessRule];
  remove: [];
}>();

function updateID(event: Event): void {
  emit("update", { ...props.rule, id: (event.target as HTMLInputElement).value });
}

function toggle(repository: string, event: Event): void {
  const selected = new Set(props.rule.repositories);
  if ((event.target as HTMLInputElement).checked) selected.add(repository);
  else selected.delete(repository);
  emit("update", { ...props.rule, repositories: [...selected] });
}
</script>

<style scoped>
.repository-access-rule {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  gap: 10px;
  padding: 12px 0;
  border-bottom: 1px solid var(--border);
}

.repository-access-rule-main {
  display: grid;
  grid-template-columns: minmax(140px, 0.35fr) minmax(0, 1fr);
  align-items: center;
  gap: 10px;
}

.repository-access-id {
  min-width: 0;
}

.repository-access-choices {
  display: flex;
  flex-wrap: wrap;
  gap: 7px;
}

.repository-access-choice {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-height: 30px;
  padding: 5px 8px;
  border: 1px solid var(--border);
  background: var(--surface-2);
  font-size: 12px;
  cursor: pointer;
}

.repository-access-choice.active {
  border-color: color-mix(in srgb, var(--accent) 55%, var(--border));
  background: var(--accent-soft);
  color: var(--text);
}

.repository-access-choice input {
  width: 14px;
  height: 14px;
  margin: 0;
  accent-color: var(--accent);
}

@media (max-width: 720px) {
  .repository-access-rule-main {
    grid-template-columns: 1fr;
  }

  .repository-access-rule {
    align-items: start;
  }
}
</style>
