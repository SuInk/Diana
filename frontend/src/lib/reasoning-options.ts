// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

const levels = [
  { value: "none", label: "关闭思考" },
  { value: "minimal", label: "最低" },
  { value: "low", label: "低" },
  { value: "medium", label: "中" },
  { value: "high", label: "高" },
  { value: "xhigh", label: "更高" },
  { value: "max", label: "最高" }
];

export function reasoningOptions(current: string, supported: string[] | undefined, defaultValue: string, defaultLabel: string) {
  const options = [{ value: defaultValue, label: defaultLabel }, ...levels];
  if (!options.some(option => option.value === current)) options.push({ value: current, label: current });
  return options.map(option => {
    const disabled = option.value !== defaultValue && !!supported?.length && !supported.includes(option.value);
    return { ...option, disabled, hint: disabled ? "当前模型不支持；旧设置将在请求时自动适配" : undefined };
  });
}
