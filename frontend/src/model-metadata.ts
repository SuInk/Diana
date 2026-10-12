// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
import type { LLMModelInfo } from "./api";

export function normalizeModelModalities(values?: string[]): string[] {
  return [...new Set((values ?? []).map(value => value.trim().toLowerCase()).filter(Boolean))];
}

export function effectiveModelInfo(model: LLMModelInfo): LLMModelInfo {
  return {
    ...model,
    input_modalities: normalizeModelModalities(model.capabilities_override?.input_modalities ?? model.input_modalities),
    output_modalities: normalizeModelModalities(model.capabilities_override?.output_modalities ?? model.output_modalities)
  };
}

/** 上游清单优先更新普通信息，本地覆盖值始终独立保留。 */
export function mergeModelMetadata(preferred: LLMModelInfo, fallback?: LLMModelInfo): LLMModelInfo {
  return effectiveModelInfo({
    ...fallback,
    ...preferred,
    custom: preferred.custom || fallback?.custom || undefined,
    context_window_override: preferred.context_window_override ?? fallback?.context_window_override,
    capabilities_override: preferred.capabilities_override ?? fallback?.capabilities_override,
    input_modalities: normalizeModelModalities([...(preferred.input_modalities ?? []), ...(fallback?.input_modalities ?? [])]),
    output_modalities: normalizeModelModalities([...(preferred.output_modalities ?? []), ...(fallback?.output_modalities ?? [])])
  });
}

/** 同步淘汰旧目录项，但保留手动添加的 ID 和已编辑的模型。 */
export function syncModelMetadata(synced: LLMModelInfo[], stored: LLMModelInfo[]): LLMModelInfo[] {
  const previous = new Map(stored.map(model => [model.id, model]));
  const result = new Map<string, LLMModelInfo>();
  for (const model of synced) {
    const saved = previous.get(model.id);
    result.set(model.id, {
      ...model,
      custom: saved?.custom,
      context_window_override: saved?.context_window_override,
      capabilities_override: saved?.capabilities_override
    });
  }
  for (const model of stored) {
    if (!result.has(model.id) && (model.custom || model.context_window_override || model.capabilities_override)) {
      result.set(model.id, { ...model });
    }
  }
  return [...result.values()];
}

export function modelCapabilitySummary(model: LLMModelInfo): string {
  const effective = effectiveModelInfo(model);
  const input = effective.input_modalities ?? [];
  const output = effective.output_modalities ?? [];
  const labels: string[] = [];
  if (output.includes("text")) labels.push(input.includes("image") ? "文字 / 视觉理解" : "文字");
  if (output.includes("image")) labels.push(input.includes("image") ? "图片生成 / 编辑" : "图片生成");
  if (output.includes("audio")) labels.push("音频输出");
  if (output.includes("video")) labels.push("视频输出");
  return labels.join(" · ") || "能力未标注";
}

export interface ModelMetadataDraft {
  id: string;
  context: string;
  overrideCapabilities: boolean;
  input: string[];
  output: string[];
}

export function createModelMetadataDraft(model?: LLMModelInfo): ModelMetadataDraft {
  const effective = model ? effectiveModelInfo(model) : undefined;
  return {
    id: model?.id ?? "",
    context: model?.context_window_override ? String(model.context_window_override) : "",
    overrideCapabilities: model ? Boolean(model.capabilities_override) : true,
    // 创建时显式展示默认文字能力，用户可以改为视觉或生图。
    input: [...(effective?.input_modalities?.length ? effective.input_modalities : ["text"])],
    output: [...(effective?.output_modalities?.length ? effective.output_modalities : ["text"])]
  };
}

export function applyModelMetadataDraft(draft: ModelMetadataDraft, models: LLMModelInfo[], editingID?: string): LLMModelInfo[] {
  const id = (editingID ?? draft.id).trim();
  if (!id || /[,，\s]/.test(id)) throw new Error("请输入一个完整模型 ID，不要包含空格或逗号");
  if (!editingID && models.some(model => model.id === id)) throw new Error("这个模型已在列表中，请直接编辑它");
  const context = draft.context.trim();
  const window = context ? Number(context) : 0;
  if (context && (!/^\d+$/.test(context) || !Number.isSafeInteger(window) || window <= 0)) {
    throw new Error("上下文窗口请填写正整数 Token 数，留空表示取消覆盖");
  }
  const input = normalizeModelModalities(draft.input);
  const output = normalizeModelModalities(draft.output);
  if (draft.overrideCapabilities && (!input.length || !output.length)) throw new Error("请至少选择一种输入能力和一种输出能力");
  const current = models.find(model => model.id === editingID);
  const next: LLMModelInfo = {
    ...current,
    id,
    custom: current?.custom || !editingID || undefined,
    context_window_override: window || undefined,
    capabilities_override: draft.overrideCapabilities ? { input_modalities: input, output_modalities: output } : undefined
  };
  return current ? models.map(model => model.id === editingID ? next : model) : [...models, next];
}
