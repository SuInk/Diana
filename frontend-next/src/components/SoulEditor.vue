<template>
  <section class="card soul-editor">
    <div class="card-header">
      <div>
        <h2>SOUL.md</h2>
        <span class="card-sub">{{ subtitle }}</span>
      </div>
      <div class="cluster">
        <button v-if="previous !== null" class="btn small" type="button" title="退回替换前的那一版" @click="undo">
          <Undo2 :size="14" aria-hidden="true" />
          撤销
        </button>
        <button class="btn small" type="button" :disabled="!generatable" @click="emit('generate')">
          <Sparkles :size="14" aria-hidden="true" />
          {{ modelValue.trim() ? "AI 改写" : "AI 写一份" }}
        </button>
      </div>
    </div>

    <div class="card-body soul-editor-body">
      <aside class="soul-library" aria-label="人设库">
        <div class="soul-library-head">
          <span class="soul-library-title">人设库</span>
          <div class="cluster">
            <button class="btn small ghost" type="button" :disabled="busy" title="导入 .md 文件、旧版人设文件或酒馆角色卡" @click="fileInput?.click()">
              <Upload :size="14" aria-hidden="true" />
              导入
            </button>
            <button class="btn small ghost" type="button" :disabled="busy || !modelValue.trim()" title="把当前正文存进人设库" @click="toggleSaver">
              <component :is="saverOpen ? X : Plus" :size="14" aria-hidden="true" />
              {{ saverOpen ? "取消" : "存入" }}
            </button>
          </div>
        </div>
        <input ref="fileInput" type="file" accept=".md,.markdown,.txt,.json,.yaml,.yml,.png,text/markdown,application/json,image/png" hidden @change="importFile" />

        <form v-if="saverOpen" class="soul-saver" @submit.prevent="storeCurrent">
          <input ref="saverInput" v-model.trim="saverName" class="input" maxlength="40" placeholder="名字，例如 值班助理" @keydown.esc="saverOpen = false" />
          <button class="btn primary small" type="submit" :disabled="busy || !saverName">
            {{ saverTarget ? "覆盖" : "保存" }}
          </button>
        </form>

        <template v-for="group in libraryGroups" :key="group.label">
          <span v-if="group.items.length" class="soul-library-group">{{ group.label }}</span>
          <ul v-if="group.items.length" class="soul-library-list">
            <li v-for="persona in group.items" :key="persona.id" class="soul-library-item" :class="{ 'is-active': activePersona?.id === persona.id }">
              <button type="button" class="soul-library-apply" :disabled="busy" :aria-current="activePersona?.id === persona.id ? 'true' : undefined" @click="apply(persona)">
                <span class="soul-library-name">{{ persona.name }}</span>
                <small class="muted">{{ personaMeta(persona) }}</small>
              </button>
              <span class="soul-library-actions">
                <button type="button" class="icon-btn" :aria-label="`导出 ${persona.name}`" :title="`导出 ${soulFileName(persona.name)}`" @click="download(persona.name, persona.system_prompt ?? '')">
                  <Download :size="13" aria-hidden="true" />
                </button>
                <button v-if="!persona.builtin" type="button" class="icon-btn danger" :disabled="busy" :aria-label="`删除 ${persona.name}`" title="从人设库删除" @click="remove(persona)">
                  <Trash2 :size="13" aria-hidden="true" />
                </button>
              </span>
            </li>
          </ul>
        </template>
        <p v-if="!loaded" class="hint">人设库加载中…</p>
        <p v-else-if="!userPersonas.length" class="hint">还没存过自己的人设。改好正文后点「存入」。</p>
      </aside>

      <div class="soul-document">
        <div class="soul-document-bar">
          <span class="soul-document-state">
            <template v-if="activePersona">
              <Check :size="13" aria-hidden="true" />
              与「{{ activePersona.name }}」一致{{ activePersona.builtin ? "（内置）" : "" }}
            </template>
            <template v-else-if="modelValue.trim()">自己写的，还没存进人设库</template>
            <template v-else>空着：保存后按内置的默认人设跑</template>
          </span>
          <span class="soul-document-count" :class="{ 'warn-text': length > SOUL_WARN_CHARS && length <= SOUL_MAX_CHARS, 'err-text': length > SOUL_MAX_CHARS }">
            {{ length }} / {{ SOUL_MAX_CHARS }}
          </span>
        </div>
        <textarea
          :id="inputId"
          class="textarea soul-textarea"
          :value="modelValue"
          spellcheck="false"
          :placeholder="placeholder"
          aria-label="SOUL.md 正文"
          @input="emit('update:modelValue', ($event.target as HTMLTextAreaElement).value)"
        ></textarea>
        <p v-if="length > SOUL_MAX_CHARS" class="hint err-text">超过 {{ SOUL_MAX_CHARS }} 字存不进人设库，也会挤占工具规则和聊天记录的上下文。</p>
        <p v-else-if="length > SOUL_WARN_CHARS" class="hint warn-text">写得越长，模型越抓不住重点。能用一句理由讲清的，就别列三条规则。</p>

        <details class="soul-guide">
          <summary><BookOpen :size="14" aria-hidden="true" />怎么写</summary>
          <div class="soul-guide-body">
            <p>Diana 的写法：<strong>讲理由，不列规则</strong>。规则总会漏掉没料到的情况，讲清楚为什么，没写到的场合她也能自己推出来。只有做错了代价很大的几件事才写成硬线。</p>
            <ul>
              <li>用「我们」（你，主人）的口吻写她，写一个人，不写一张清单。</li>
              <li>开头先写基本情况：名字、性别、社会情况（年龄、职业或身份、和主人的关系）。</li>
              <li>推荐几章：基本情况、概述、核心价值、真的有用、正派、守规矩、她的本性、结语。标题只是给人看的结构，不解析。</li>
              <li>长相可以不写：不写时她的形象默认就是机器人自己的头像，被问长什么样、让画自己时会先看头像。写了就以这里为准。</li>
              <li>写明身份在压力下不变：别人起外号、逼她演另一个人，她可以接玩笑，但不会变成那样。</li>
              <li>不用写输出格式、分条、工具、时间、在哪个群：这些运行时会另外补上，写了反而打架。</li>
              <li>示例对话可以不写：模型会把它照抄成模板。</li>
            </ul>
          </div>
        </details>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from "vue";
import { BookOpen, Check, Download, Plus, Sparkles, Trash2, Undo2, Upload, X } from "@lucide/vue";
import { deletePersona, importCharacterCard, importPersonaSource, listPersonas, savePersona, type Persona, type WorldBookNode } from "../api";
import { askConfirm } from "../confirm";
import { toastError, toastSuccess } from "../toast";
import { matchingPersona, soulFileName, soulLength, soulTitle, SOUL_MAX_CHARS, SOUL_WARN_CHARS, unusedPersonaName } from "../soul-library";

// SOUL.md 编辑器：左边人设库，右边正文。
//
// 人设库是「套用来源」，不是活绑定：点一套就把它的正文填进右边，改不改随你，保存
// 配置才生效；库里那份之后再改，不会偷偷影响已经保存的机器人。所以这里没有
// 「当前选的是哪一套」这个状态，只看正文和库里哪一份一字不差。

const props = withDefaults(
  defineProps<{
    modelValue: string;
    /** 没有标题时存库用的名字。 */
    fallbackName?: string;
    subtitle?: string;
    placeholder?: string;
    inputId?: string;
    /** AI 写需要有能用的文本模型，没有时按钮置灰。 */
    generatable?: boolean;
  }>(),
  {
    fallbackName: "",
    subtitle: "她是谁、在乎什么、怎么说话。整份原样放在系统提示词最前面，群可以单独覆盖。",
    placeholder: "# 名字\n\n## 概述\n\n她是谁，我们希望她成为什么样的存在……",
    inputId: "soul-md",
    generatable: true
  }
);

const emit = defineEmits<{
  "update:modelValue": [value: string];
  generate: [];
  "world-book": [nodes: WorldBookNode[]];
}>();

const personas = ref<Persona[]>([]);
const loaded = ref(false);
const busy = ref(false);
// 替换正文之前的那一版。只留一步：够找回误点，多了反而让人搞不清退到了哪。
const previous = ref<string | null>(null);

const builtinPersonas = computed(() => personas.value.filter((persona) => persona.builtin));
const userPersonas = computed(() => personas.value.filter((persona) => !persona.builtin));
const libraryGroups = computed(() => [
  { label: "内置", items: builtinPersonas.value },
  { label: "我的", items: userPersonas.value }
]);
const activePersona = computed(() => matchingPersona(props.modelValue, personas.value));
const length = computed(() => soulLength(props.modelValue));

onMounted(load);

async function load(): Promise<void> {
  try {
    personas.value = (await listPersonas()).personas ?? [];
  } catch {
    // 人设库读不出来不该挡住编辑：它只是个快捷方式，正文照样能改能存。
    personas.value = [];
  } finally {
    loaded.value = true;
  }
}

// replace 换掉整份正文并记下上一版。AI 写的结果也走这里，撤销才是同一个按钮。
function replace(text: string): void {
  previous.value = props.modelValue;
  emit("update:modelValue", text);
}

function undo(): void {
  if (previous.value === null) return;
  emit("update:modelValue", previous.value);
  previous.value = null;
}

async function apply(persona: Persona): Promise<void> {
  const next = persona.system_prompt ?? "";
  if (next.trim() === props.modelValue.trim()) return;
  // 正文是自己写的、库里又没有这一份时，替换前问一句：撤销只能退一步。
  if (props.modelValue.trim() && !activePersona.value) {
    const ok = await askConfirm({
      title: `换成「${persona.name}」？`,
      message: "当前正文还没存进人设库。换掉之后可以点「撤销」退回一次。",
      confirmLabel: "换"
    });
    if (!ok) return;
  }
  replace(next);
}

// ── 存入人设库 ──
const saverOpen = ref(false);
const saverName = ref("");
const saverInput = ref<HTMLInputElement | null>(null);
const saverTarget = computed(() => userPersonas.value.find((persona) => persona.name === saverName.value));

function toggleSaver(): void {
  saverOpen.value = !saverOpen.value;
  if (!saverOpen.value) return;
  saverName.value = soulTitle(props.modelValue) || props.fallbackName.trim();
  void nextTick(() => saverInput.value?.focus());
}

async function storeCurrent(): Promise<void> {
  const name = saverName.value.trim();
  if (!name || !props.modelValue.trim()) return;
  // 同名的自己的人设直接覆盖（界面上按钮已经写着「覆盖」）；和内置的撞名就另起一个名字，内置的只读。
  const target = saverTarget.value;
  const savedName = target ? name : unusedPersonaName(name, personas.value);
  busy.value = true;
  try {
    const response = await savePersona({ ...(target ? { id: target.id } : {}), name: savedName, system_prompt: props.modelValue });
    personas.value = response.personas ?? personas.value;
    saverOpen.value = false;
    toastSuccess(target ? `已覆盖「${savedName}」` : `已存为「${savedName}」`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "存入人设库失败");
  } finally {
    busy.value = false;
  }
}

async function remove(persona: Persona): Promise<void> {
  const ok = await askConfirm({ title: `删除「${persona.name}」？`, message: "只删人设库里这一份，已经保存到机器人上的正文不受影响。", danger: true, confirmLabel: "删除" });
  if (!ok) return;
  busy.value = true;
  try {
    personas.value = (await deletePersona(persona.id)).personas ?? personas.value;
    toastSuccess(`已删除「${persona.name}」`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "删除失败");
  } finally {
    busy.value = false;
  }
}

function personaMeta(persona: Persona): string {
  const chars = `${soulLength(persona.system_prompt ?? "")} 字`;
  if (persona.builtin) return `内置 · ${chars}`;
  const date = persona.updated_at ? new Date(persona.updated_at) : null;
  return date && !Number.isNaN(date.getTime()) ? `${date.getMonth() + 1}/${date.getDate()} · ${chars}` : chars;
}

// ── 导入导出 ──
const fileInput = ref<HTMLInputElement | null>(null);

function download(name: string, text: string): void {
  const url = URL.createObjectURL(new Blob([text.endsWith("\n") ? text : `${text}\n`], { type: "text/markdown;charset=utf-8" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = soulFileName(name);
  link.click();
  URL.revokeObjectURL(url);
}

function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result).split(",", 2)[1] ?? "");
    reader.onerror = () => reject(reader.error ?? new Error("读取文件失败"));
    reader.readAsDataURL(file);
  });
}

// 酒馆角色卡：有 spec 标记，或带着卡特有的字段。旧版人设文件和世界书都没有这些。
function looksLikeCharacterCard(text: string): boolean {
  try {
    const record = JSON.parse(text) as Record<string, any>;
    if (!record || typeof record !== "object" || Array.isArray(record)) return false;
    if (typeof record.spec === "string" && record.spec.startsWith("chara_card")) return true;
    if (record.first_mes !== undefined || record.mes_example !== undefined) return true;
    return Boolean(record.data && typeof record.data === "object" && typeof record.data.name === "string");
  } catch {
    return false;
  }
}

async function importCard(file: File): Promise<void> {
  const result = await importCharacterCard(await fileToBase64(file));
  personas.value = result.personas ?? personas.value;
  if (result.nodes?.length) emit("world-book", result.nodes);
  const notes: string[] = [];
  if (result.persona) notes.push(`已导入角色卡「${result.persona.name}」${result.renamed ? "（重名已改名）" : ""}`);
  else if (result.skipped) notes.push("这张卡已经在库里，跳过");
  if (result.book_imported) notes.push(`世界书并入 ${result.book_imported} 条`);
  toastSuccess(notes.join("，") || "角色卡已处理");
}

async function importFile(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  // 先清掉选中的文件：不清的话连续选同一个文件不会再触发 change。
  input.value = "";
  if (!file) return;
  busy.value = true;
  try {
    const lower = file.name.toLowerCase();
    if (file.type === "image/png" || lower.endsWith(".png")) {
      await importCard(file);
      return;
    }
    const text = await file.text();
    if (lower.endsWith(".json") && looksLikeCharacterCard(text)) {
      await importCard(file);
      return;
    }
    // .md 是一份 SOUL.md；.yaml/.json 是旧版人设文件，后端把旧字段并进正文。
    const result = await importPersonaSource(text, file.name);
    personas.value = result.personas ?? personas.value;
    const notes = [`导入 ${result.imported} 套`];
    if (result.renamed) notes.push(`${result.renamed} 套重名已改名`);
    if (result.skipped) notes.push(`${result.skipped} 套已在库里`);
    if (result.dropped) notes.push(`${result.dropped} 套是空的`);
    toastSuccess(notes.join("，"));
  } catch (error) {
    toastError(error instanceof Error ? error.message : "导入失败");
  } finally {
    busy.value = false;
  }
}

defineExpose({ replace, reload: load, builtinDefault: () => builtinPersonas.value[0] });
</script>
