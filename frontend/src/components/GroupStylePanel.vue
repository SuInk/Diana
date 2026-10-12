<script setup lang="ts">
import { computed, ref, useId, watch } from "vue";
import { RefreshCw, RotateCcw, Save } from "@lucide/vue";
import { getGroupStyle, relearnGroupStyle, saveGroupStyle, setGroupStyleEnabled, type GroupStyleResponse } from "../api";
import { askConfirm } from "../confirm";
import { toastError, toastSuccess } from "../toast";

// 本群说话风格：模型读群聊写的一段「这个群怎么说话」，回复时带上。这段不是群配置，
// 是运行时学到的东西，所以自己加载、自己保存，不跟着群配置的「保存」走。
const props = defineProps<{ profileId: string; groupId: string }>();
const id = useId();

const data = ref<GroupStyleResponse | null>(null);
const draft = ref("");
const loading = ref(false);
const saving = ref(false);
const learning = ref(false);
const toggling = ref(false);
const loadError = ref("");

async function load(): Promise<void> {
  if (!props.groupId) return;
  loading.value = true;
  loadError.value = "";
  try {
    apply(await getGroupStyle(props.groupId, props.profileId));
  } catch (error) {
    loadError.value = error instanceof Error ? error.message : "读取失败";
  } finally {
    loading.value = false;
  }
}

function apply(response: GroupStyleResponse): void {
  data.value = response;
  draft.value = response.style?.text ?? "";
}

watch(() => [props.profileId, props.groupId], load, { immediate: true });

const style = computed(() => data.value?.style);
const hasNote = computed(() => Boolean(style.value?.text.trim()));
const enabled = computed(() => !style.value?.disabled);
const busy = computed(() => saving.value || learning.value || toggling.value);
const maxRunes = computed(() => data.value?.max_runes ?? 400);
const length = computed(() => Array.from(draft.value).length);
const dirty = computed(() => draft.value.trim() !== (style.value?.text ?? "").trim());

const status = computed(() => {
  const current = style.value;
  if (current?.disabled) return "本群已关闭";
  if (!current || !hasNote.value) return "还没学到";
  const day = new Date(current.updated_at);
  const when = Number.isNaN(day.getTime()) ? "" : ` · ${day.getMonth() + 1} 月 ${day.getDate()} 日`;
  if (current.manual) return `手动写的${when}`;
  return `自动学到${when}${current.sample_count ? ` · 读了 ${current.sample_count} 条` : ""}`;
});

async function save(text: string, message: string): Promise<void> {
  saving.value = true;
  try {
    apply(await saveGroupStyle(props.groupId, props.profileId, text));
    toastSuccess(message);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存失败");
  } finally {
    saving.value = false;
  }
}

async function toggle(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  toggling.value = true;
  try {
    // 开关不动正文：还没保存的修改留在输入框里。
    const unsaved = draft.value;
    apply(await setGroupStyleEnabled(props.groupId, props.profileId, input.checked));
    draft.value = unsaved;
    toastSuccess(input.checked ? "本群的风格已打开" : "本群的风格已关闭，笔记留着");
  } catch (error) {
    input.checked = enabled.value;
    toastError(error instanceof Error ? error.message : "切换失败");
  } finally {
    toggling.value = false;
  }
}

// 重新学习会覆盖现在这段（手动写的也一样），还要花一次后台模型调用，先问一句。
function relearnWarning(): string {
  const lost: string[] = [];
  if (style.value?.manual) lost.push("你手动写的这段会被覆盖");
  else if (hasNote.value) lost.push("现在这段会被覆盖");
  if (dirty.value) lost.push("还没保存的修改也会丢掉");
  const head = "会用后台模型重新读最近的群聊，写一段新的风格笔记，要等十几秒。";
  return lost.length ? `${head}${lost.join("，")}。` : head;
}

async function relearn(): Promise<void> {
  const confirmed = await askConfirm({
    title: "重新学习本群风格？",
    message: relearnWarning(),
    confirmLabel: "重新学习",
    danger: Boolean(style.value?.manual) || dirty.value
  });
  if (!confirmed) return;
  learning.value = true;
  try {
    apply(await relearnGroupStyle(props.groupId, props.profileId));
    toastSuccess("重新学好了");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "学习失败");
  } finally {
    learning.value = false;
  }
}
</script>

<template>
  <div class="group-style">
    <div class="group-style-head">
      <label :for="id">本群说话风格</label>
      <span class="badge" :class="{ accent: enabled && hasNote && !style?.manual, warn: enabled && style?.manual }">{{ loading ? "读取中…" : status }}</span>
      <label class="switch group-style-switch" :title="enabled ? '本群带上风格笔记' : '本群不用风格笔记'">
        <input type="checkbox" :checked="enabled" :disabled="loading || !!loadError || busy" @change="toggle" />
        <span class="track" aria-hidden="true"></span>
        <span class="switch-label">本群启用</span>
      </label>
    </div>
    <p v-if="loadError" class="hint">读取失败：{{ loadError }}</p>
    <template v-else>
      <textarea
        :id="id"
        v-model="draft"
        class="textarea group-style-text"
        :class="{ muted: !enabled }"
        rows="5"
        :maxlength="maxRunes"
        placeholder="还没学到。群里聊够几十条之后，大约一天学一次；也可以点「重新学习」，或者自己写一段。"
      ></textarea>
      <div class="group-style-actions">
        <span class="hint">{{ length }} / {{ maxRunes }}</span>
        <button class="btn small" type="button" :disabled="!dirty || busy" @click="save(draft, draft.trim() ? '已保存，自动学习不再覆盖它' : '已交回自动学习')">
          <Save :size="14" aria-hidden="true" />
          保存
        </button>
        <button v-if="style?.manual" class="btn small ghost" type="button" :disabled="busy" @click="save('', '已交回自动学习')">
          <RotateCcw :size="14" aria-hidden="true" />
          交回自动
        </button>
        <button class="btn small" type="button" :disabled="busy" @click="relearn">
          <RefreshCw :size="14" aria-hidden="true" :class="{ spinning: learning }" />
          {{ learning ? "学习中…" : "重新学习" }}
        </button>
      </div>
      <span class="hint">
        风格学习打开时，每个群大约一天学一次：读最近的群友消息，写一段这个群怎么说话，回复时带上。手动改过的不会被自动覆盖，点「重新学习」会覆盖。
        <template v-if="!enabled"><strong>本群已关闭风格：不自动学，也不带进回复，这段留着，打开后接着用。</strong></template>
        <template v-else-if="data && !data.learning_enabled"><strong>这台机器人没开「风格学习」，这段现在不会带进回复。</strong></template>
      </span>
    </template>
  </div>
</template>

<style scoped>
.group-style { display: grid; gap: 8px; }
.group-style-head { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.group-style-head > label:first-child { margin-right: auto; }
.group-style-text.muted { opacity: 0.6; }
.group-style-text { min-height: 120px; }
.group-style-actions { display: flex; align-items: center; justify-content: flex-end; gap: 8px; flex-wrap: wrap; }
.group-style-actions .hint { margin-right: auto; }
.spinning { animation: group-style-spin 1s linear infinite; }
@keyframes group-style-spin { to { transform: rotate(360deg); } }
</style>
