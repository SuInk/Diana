import { reactive, readonly } from "vue";

export type ToastKind = "success" | "error" | "info";

export interface Toast {
  id: number;
  kind: ToastKind;
  message: string;
}

const state = reactive<{ toasts: Toast[] }>({ toasts: [] });

let nextID = 1;
const timers = new Map<number, number>();

function push(kind: ToastKind, message: string, duration = 3600): void {
  // 相同内容的 toast 还在展示时只重置倒计时，避免连续操作刷出一摞重复提示。
  const existing = state.toasts.find((toast) => toast.kind === kind && toast.message === message);
  if (existing) {
    window.clearTimeout(timers.get(existing.id));
    timers.set(
      existing.id,
      window.setTimeout(() => {
        dismiss(existing.id);
      }, duration)
    );
    return;
  }
  const id = nextID++;
  state.toasts.push({ id, kind, message });
  timers.set(
    id,
    window.setTimeout(() => {
      dismiss(id);
    }, duration)
  );
}

export function dismiss(id: number): void {
  window.clearTimeout(timers.get(id));
  timers.delete(id);
  const index = state.toasts.findIndex((toast) => toast.id === id);
  if (index >= 0) {
    state.toasts.splice(index, 1);
  }
}

export function toastSuccess(message: string): void {
  push("success", message);
}

export function toastError(message: string): void {
  push("error", message, 5200);
}

export function toastInfo(message: string): void {
  push("info", message);
}

export const toastState = readonly(state);
