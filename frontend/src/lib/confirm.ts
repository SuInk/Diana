// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { reactive } from "vue";

/**
 * 应用内确认弹窗。
 *
 * 不能用 window.confirm：嵌入式浏览器、沙箱 iframe、以及浏览器的
 * 「阻止此页面创建更多对话框」都会让它直接返回 false 且不弹框，
 * 结果是删除按钮点了完全没反应。
 */
export interface ConfirmRequest {
  title: string;
  message: string;
  confirmLabel?: string;
  danger?: boolean;
}

interface ConfirmState extends ConfirmRequest {
  open: boolean;
  resolve: ((ok: boolean) => void) | null;
}

export const confirmState = reactive<ConfirmState>({
  open: false,
  title: "",
  message: "",
  confirmLabel: "确定",
  danger: false,
  resolve: null
});

export function askConfirm(request: ConfirmRequest): Promise<boolean> {
  // 同时只允许一个确认框；新的请求把旧的当作取消处理。
  if (confirmState.resolve) {
    confirmState.resolve(false);
  }
  confirmState.open = true;
  confirmState.title = request.title;
  confirmState.message = request.message;
  confirmState.confirmLabel = request.confirmLabel ?? "确定";
  confirmState.danger = request.danger ?? false;
  return new Promise<boolean>((resolve) => {
    confirmState.resolve = resolve;
  });
}

export function settleConfirm(ok: boolean): void {
  const resolve = confirmState.resolve;
  confirmState.open = false;
  confirmState.resolve = null;
  resolve?.(ok);
}
