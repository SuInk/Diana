<script setup lang="ts">
import { computed } from "vue";
import AppSelect from "./AppSelect.vue";
import PluginSizeInput from "./PluginSizeInput.vue";
import type { PluginSettingSpec } from "../api";

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

const emit = defineEmits<{ (event: "toggle-clear", key: string): void }>();

const controlID = computed(() => props.fieldID ?? `setting-${props.spec.key}`);
const labelID = computed(() => `${controlID.value}-label`);

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
  <div class="field">
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
        />
        <button
          v-if="secretConfigured"
          class="btn small"
          type="button"
          :aria-pressed="clearing ? 'true' : 'false'"
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
