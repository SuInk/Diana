<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import CodingAgentsField from "./CodingAgentsField.vue";
import AppSelect from "./AppSelect.vue";
import PluginSizeInput from "./PluginSizeInput.vue";
import { getConfig, type LLMConfig, type PluginSettingSpec } from "../api";

// 插件设置项的单个控件。抽出来是为了让 GitHub 仓库这类需要把设置项
// 分组重排的弹窗能复用同一套渲染，而不是把整段模板复制几份。
const props = defineProps<{
  spec: PluginSettingSpec;
  // 表单对象直接改：这里是弹窗内的草稿，保存前不落库。
  form: Record<string, any>;
  clearing?: boolean;
  secretConfigured?: boolean;
  secretPlaceholder?: string;
  fieldID?: string;
}>();

const emit = defineEmits<{
  (event: "toggle-clear", key: string): void;
  (event: "blur", key: string, focusEvent: FocusEvent): void;
}>();

const controlID = computed(() => props.fieldID ?? `setting-${props.spec.key}`);
const labelID = computed(() => `${controlID.value}-label`);

// llm_profile：先选提供商，再选它目录里的模型，存成 JSON 的 [提供商ID, 模型]；空串跟随对话模型。
// 只在出现这种设置时才拉配置。
const llmChannels = ref<LLMConfig[]>([]);
const textChannels = computed(() => llmChannels.value.filter(channel => channel.id && !["image", "embedding", "tts", "stt", "video"].includes(channel.group || "")));
const llmPair = computed<[string, string]>(() => {
  const raw = String(props.form[props.spec.key] ?? "");
  if (raw.startsWith("[")) {
    try {
      const [provider, model] = JSON.parse(raw) as [string, string];
      return [provider ?? "", model ?? ""];
    } catch {
      return ["", ""];
    }
  }
  return [raw, ""];
});
const llmProviderOptions = computed(() => {
  const options = [{ value: "", label: "跟随对话模型" }];
  for (const channel of textChannels.value) options.push({ value: channel.id!, label: channel.name || channel.id! });
  const [provider] = llmPair.value;
  if (provider && !options.some(option => option.value === provider)) options.push({ value: provider, label: `配置已不可用：${provider}` });
  return options;
});
const llmModelOptions = computed(() => {
  const [provider, current] = llmPair.value;
  const channel = textChannels.value.find(item => item.id === provider);
  const disabled = new Set((channel?.models ?? []).filter(model => model.disabled).map(model => model.id));
  const models = [...new Set([channel?.model, ...(channel?.models ?? []).map(model => model.id)].filter((model): model is string => Boolean(model) && !disabled.has(model!)))];
  if (current && !models.includes(current)) models.push(current);
  return models.map(model => ({ value: model, label: model }));
});
function setLLMProvider(provider: string): void {
  const channel = textChannels.value.find(item => item.id === provider);
  props.form[props.spec.key] = provider ? JSON.stringify([provider, channel?.model || ""]) : "";
}
function setLLMModel(model: string): void {
  props.form[props.spec.key] = JSON.stringify([llmPair.value[0], model]);
}
onMounted(async () => {
  if (props.spec.type !== "llm_profile") return;
  try {
    const config = await getConfig();
    llmChannels.value = config.profiles?.length ? config.profiles : [config];
  } catch {
    llmChannels.value = [];
  }
});

function multiSelected(option: string): boolean {
  const value = props.form[props.spec.key];
  return Array.isArray(value) && value.includes(option);
}

function toggleMultiSelect(option: string, event: Event): void {
  const checked = (event.target as HTMLInputElement).checked;
  const current: string[] = Array.isArray(props.form[props.spec.key]) ? [...props.form[props.spec.key]] : [];
  const index = current.indexOf(option);
  if (checked && index < 0) {
    current.push(option);
  }
  if (!checked && index >= 0) {
    current.splice(index, 1);
  }
  props.form[props.spec.key] = current;
}
</script>

<template>
  <div v-if="spec.key !== 'agent_api_keys'" class="field">
    <template v-if="spec.type === 'bool'">
      <div class="plugin-setting-switch">
        <div class="plugin-setting-switch-text">
          <label :for="controlID">{{ spec.label }}</label>
          <span v-if="spec.description" class="hint">{{ spec.description }}</span>
        </div>
        <label class="switch">
          <input :id="controlID" v-model="form[spec.key]" type="checkbox" />
          <span class="track" aria-hidden="true"></span>
        </label>
      </div>
    </template>
    <template v-else-if="spec.type === 'multi_select'">
      <!-- 一组复选框没有单一控件可以 for，用 aria-labelledby 把组名接上去。 -->
      <span :id="labelID" class="plugin-setting-group-label">{{ spec.label }}</span>
      <div class="plugin-setting-checks" role="group" :aria-labelledby="labelID">
        <label v-for="option in spec.options ?? []" :key="option.value" class="check-item">
          <input
            type="checkbox"
            :checked="multiSelected(option.value)"
            @change="toggleMultiSelect(option.value, $event)"
          />
          <span>{{ option.label }}</span>
        </label>
      </div>
      <span class="hint">{{ spec.description ? `${spec.description} ` : "" }}不勾选表示全部停用；全部勾选表示全部启用。</span>
    </template>
    <template v-else-if="spec.type === 'coding_agents'">
      <span :id="labelID" class="plugin-setting-group-label">{{ spec.label }}</span>
      <CodingAgentsField :id="controlID" v-model="form[spec.key]" :key-drafts="form.agent_api_keys || ''" @update:key-drafts="form.agent_api_keys = $event" :aria-labelledby="labelID" />
      <span v-if="spec.description" class="hint">{{ spec.description }}</span>
    </template>
    <template v-else>
      <label :for="controlID">{{ spec.label }}</label>
      <div v-if="spec.type === 'number'" class="plugin-setting-number">
        <input
          :id="controlID"
          v-model.number="form[spec.key]"
          class="input"
          type="number"
          :min="spec.min"
          :max="spec.max"
          :step="spec.step || 1"
        />
        <span v-if="spec.unit" class="plugin-setting-unit">{{ spec.unit }}</span>
      </div>
      <PluginSizeInput
        v-else-if="spec.type === 'size'"
        :id="controlID"
        v-model="form[spec.key] as number"
        :min="spec.min"
        :max="spec.max"
      />
      <div v-else-if="spec.type === 'llm_profile'" class="plugin-setting-llm">
        <AppSelect :id="controlID" :model-value="llmPair[0]" :options="llmProviderOptions" aria-label="提供商" @update:model-value="setLLMProvider" />
        <AppSelect
          v-if="llmPair[0]"
          :model-value="llmPair[1]"
          :options="llmModelOptions"
          searchable
          search-placeholder="搜索模型"
          aria-label="模型"
          @update:model-value="setLLMModel"
        />
      </div>
      <AppSelect
        v-else-if="spec.type === 'select'"
        :id="controlID"
        v-model="form[spec.key]"
        :options="spec.options ?? []"
      />
      <div v-else-if="spec.secret" class="input-group">
        <input
          :id="controlID"
          v-model="form[spec.key]"
          class="input"
          type="password"
          autocomplete="off"
          :disabled="clearing"
          :placeholder="secretPlaceholder"
          @blur="emit('blur', spec.key, $event)"
        />
        <button
          v-if="secretConfigured"
          class="btn small"
          type="button"
          :aria-pressed="clearing ? 'true' : 'false'"
          data-secret-clear
          @click="emit('toggle-clear', spec.key)"
        >
          {{ clearing ? "取消清除" : "清除" }}
        </button>
      </div>
      <textarea
        v-else-if="spec.type === 'text'"
        :id="controlID"
        v-model="form[spec.key]"
        class="input plugin-setting-textarea"
        :rows="spec.rows && spec.rows > 0 ? spec.rows : 4"
      ></textarea>
      <input v-else :id="controlID" v-model="form[spec.key]" class="input" type="text" />
      <span v-if="spec.description" class="hint">{{ spec.description }}</span>
    </template>
  </div>
</template>

<style scoped>
.plugin-setting-llm {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 8px;
}
</style>
