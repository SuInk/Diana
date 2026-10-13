<template>
  <div class="reply-tone-editor">
    <aside class="soul-library" aria-label="回复口吻库">
      <div class="soul-library-head">
        <span class="soul-library-title">口吻库</span>
        <div class="cluster">
          <button class="btn small ghost" type="button" :disabled="busy" @click="fileInput?.click()">
            <Upload :size="14" aria-hidden="true" />导入
          </button>
          <button class="btn small ghost" type="button" :disabled="busy || !modelValue.trim()" @click="toggleSaver">
            <component :is="saverOpen ? X : Plus" :size="14" aria-hidden="true" />{{ saverOpen ? "取消" : "存为" }}
          </button>
        </div>
      </div>
      <input ref="fileInput" type="file" accept=".txt,.md,text/plain,text/markdown" hidden @change="importFile" />
      <form v-if="saverOpen" class="soul-saver" @submit.prevent="storeCurrent">
        <input ref="saverInput" v-model.trim="saverName" class="input" maxlength="40" placeholder="名字，例如 群聊短句" @keydown.esc="saverOpen = false" />
        <button class="btn primary small" type="submit" :disabled="busy || !saverName">{{ saverTarget ? "覆盖" : "保存" }}</button>
      </form>
      <template v-for="group in libraryGroups" :key="group.label">
        <span v-if="group.items.length" class="soul-library-group">{{ group.label }}</span>
        <ul v-if="group.items.length" class="soul-library-list">
          <li v-for="preset in group.items" :key="preset.id" class="soul-library-item" :class="{ 'is-active': activePreset?.id === preset.id }">
            <button type="button" class="soul-library-apply" :disabled="busy" @click="apply(preset)">
              <span class="soul-library-name">{{ preset.name }}</span>
              <small class="muted">{{ presetMeta(preset) }}</small>
            </button>
            <span class="soul-library-actions">
              <button type="button" class="icon-btn" :aria-label="`导出 ${preset.name}`" title="导出" @click="download(preset.name, preset.text)"><Download :size="13" /></button>
              <button v-if="!preset.builtin" type="button" class="icon-btn danger" :disabled="busy" :aria-label="`删除 ${preset.name}`" title="从口吻库删除" @click="remove(preset)"><Trash2 :size="13" /></button>
            </span>
          </li>
        </ul>
      </template>
      <p v-if="!loaded" class="hint">口吻库加载中…</p>
      <p v-else-if="!userPresets.length" class="hint">还没存过自己的口吻，改好后点「存为」</p>
    </aside>

    <div class="soul-document reply-tone-document">
      <div class="soul-document-bar">
        <span class="soul-document-state">
          <template v-if="activePreset"><Check :size="13" />与 {{ activePreset.name }} 一致{{ activePreset.builtin ? "（内置）" : "" }}</template>
          <template v-else>自己修改的口吻，还没存入口吻库</template>
        </span>
        <button v-if="previous !== null" class="btn small ghost" type="button" @click="undo"><Undo2 :size="13" />撤销</button>
      </div>
      <textarea id="bot-reply-style" class="textarea" rows="8" spellcheck="false" :value="modelValue" @input="emit('update:modelValue', ($event.target as HTMLTextAreaElement).value)"></textarea>
      <span class="hint">选择模板只会替换编辑框，保存机器人配置后才会生效</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from "vue";
import { Check, Download, Plus, Trash2, Undo2, Upload, X } from "@lucide/vue";
import { deleteReplyTone, listReplyTones, saveReplyTone, type ReplyTonePreset } from "../api";
import { askConfirm } from "../lib/confirm";
import { normalizeReplyTone } from "../lib/reply-tone";
import { toastError, toastSuccess } from "../lib/toast";

const props = defineProps<{ modelValue: string }>();
const emit = defineEmits<{ "update:modelValue": [value: string] }>();
const presets = ref<ReplyTonePreset[]>([]);
const loaded = ref(false);
const busy = ref(false);
const previous = ref<string | null>(null);
const fileInput = ref<HTMLInputElement | null>(null);
const saverInput = ref<HTMLInputElement | null>(null);
const saverOpen = ref(false);
const saverName = ref("");
const builtinPresets = computed(() => presets.value.filter(item => item.builtin));
const userPresets = computed(() => presets.value.filter(item => !item.builtin));
const libraryGroups = computed(() => [{ label: "内置", items: builtinPresets.value }, { label: "我的", items: userPresets.value }]);
const activePreset = computed(() => presets.value.find(item => normalizeReplyTone(item.text) === normalizeReplyTone(props.modelValue)));
const saverTarget = computed(() => userPresets.value.find(item => item.name === saverName.value.trim()));

onMounted(async () => {
  try { presets.value = (await listReplyTones()).presets ?? []; }
  catch { presets.value = []; }
  finally { loaded.value = true; }
});

function replace(text: string): void { previous.value = props.modelValue; emit("update:modelValue", text); }
function undo(): void { if (previous.value !== null) { emit("update:modelValue", previous.value); previous.value = null; } }

async function apply(preset: ReplyTonePreset): Promise<void> {
  if (normalizeReplyTone(preset.text) === normalizeReplyTone(props.modelValue)) return;
  if (props.modelValue.trim() && !activePreset.value) {
    const ok = await askConfirm({ title: `换成「${preset.name}」？`, message: "当前修改还没存入口吻库，替换后可以撤销一次", confirmLabel: "换" });
    if (!ok) return;
  }
  replace(preset.text);
}

function toggleSaver(): void {
  saverOpen.value = !saverOpen.value;
  if (!saverOpen.value) return;
  saverName.value = activePreset.value && !activePreset.value.builtin ? activePreset.value.name : "";
  void nextTick(() => saverInput.value?.focus());
}

function unusedName(name: string): string {
  if (!presets.value.some(item => item.name === name)) return name;
  for (let i = 2; i < 52; i++) if (!presets.value.some(item => item.name === `${name} (${i})`)) return `${name} (${i})`;
  return `${name} 副本`;
}

async function storeCurrent(): Promise<void> {
  const name = saverName.value.trim();
  if (!name || !props.modelValue.trim()) return;
  const target = saverTarget.value;
  const savedName = target ? name : unusedName(name);
  busy.value = true;
  try {
    const response = await saveReplyTone({ ...(target ? { id: target.id } : {}), name: savedName, text: props.modelValue });
    presets.value = response.presets ?? presets.value;
    saverOpen.value = false;
    toastSuccess(target ? `已覆盖「${savedName}」` : `已存为「${savedName}」`);
  } catch (error) { toastError(error instanceof Error ? error.message : "保存回复口吻失败"); }
  finally { busy.value = false; }
}

async function remove(preset: ReplyTonePreset): Promise<void> {
  if (!(await askConfirm({ title: `删除「${preset.name}」？`, message: "只删除口吻库里的模板，机器人已经保存的配置不受影响", danger: true, confirmLabel: "删除" }))) return;
  busy.value = true;
  try { presets.value = (await deleteReplyTone(preset.id)).presets ?? presets.value; toastSuccess(`已删除「${preset.name}」`); }
  catch (error) { toastError(error instanceof Error ? error.message : "删除失败"); }
  finally { busy.value = false; }
}

function safeName(name: string): string { return name.replace(/[\\/:*?"<>|]+/g, "-").trim() || "reply-tone"; }
function download(name: string, text: string): void {
  const url = URL.createObjectURL(new Blob([text.endsWith("\n") ? text : `${text}\n`], { type: "text/plain;charset=utf-8" }));
  const link = document.createElement("a"); link.href = url; link.download = `${safeName(name)}.txt`; link.click(); URL.revokeObjectURL(url);
}
async function importFile(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement; const file = input.files?.[0]; input.value = ""; if (!file) return;
  try { const text = normalizeReplyTone(await file.text()); if (!text) throw new Error("导入文件里没有回复口吻"); replace(text); toastSuccess(`已导入「${file.name}」，可以修改后存为模板`); }
  catch (error) { toastError(error instanceof Error ? error.message : "导入失败"); }
}
function presetMeta(preset: ReplyTonePreset): string {
  if (preset.builtin) return `内置 · ${[...preset.text].length} 字`;
  const date = preset.updated_at ? new Date(preset.updated_at) : null;
  return date && !Number.isNaN(date.getTime()) ? `${date.getMonth() + 1}/${date.getDate()} · ${[...preset.text].length} 字` : `${[...preset.text].length} 字`;
}
</script>
