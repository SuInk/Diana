// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 自更新是「装好新的、关掉旧的、新进程接管」，中间必然有一段没人应答的空窗——
// 实测约 11 秒。这段时间里前端不该报故障：一次计划内的重启和一台真的挂掉的机器，
// 对用户的意义完全不同。
//
// 这里存的是「刚才点过升级」这件事，落在 localStorage 上，因为升级的结果就是
// 页面所连的进程被换掉，内存里的标记撑不过那一下。
const INSTALL_MARKER_KEY = "diana.update.installing";

// 装完还要跑健康检查，宽限期给得比实测空窗宽一些。
const INSTALL_MARKER_TTL_MS = 5 * 60 * 1000;

export function markUpdateInstalling(): void {
  try {
    window.localStorage.setItem(INSTALL_MARKER_KEY, String(Date.now()));
  } catch {
    /* 隐私模式下写不进去：拿不到这个提示而已，不影响重连本身 */
  }
}

export function clearUpdateInstalling(): void {
  try {
    window.localStorage.removeItem(INSTALL_MARKER_KEY);
  } catch {
    /* 同上 */
  }
}

// 宽限期结束时整页重新加载一次：自更新换掉的不只是后端进程，前端产物的文件名也
// 变了，重新加载才能拿到新版界面。只能来一次——重载之后后端要是还没回来，第二次
// 重载只会把用户扔进一个刷不完的循环。
const RELOAD_MARKER_KEY = "diana.backend.autoReloadAt";
const RELOAD_COOLDOWN_MS = 2 * 60 * 1000;

export function autoReloadAllowed(): boolean {
  try {
    const raw = window.sessionStorage.getItem(RELOAD_MARKER_KEY);
    const last = raw ? Number(raw) : 0;
    return !Number.isFinite(last) || Date.now() - last > RELOAD_COOLDOWN_MS;
  } catch {
    // 读不到就别自动重载：宁可让用户自己点，也不能冒刷新循环的险。
    return false;
  }
}

export function markAutoReloaded(): void {
  try {
    window.sessionStorage.setItem(RELOAD_MARKER_KEY, String(Date.now()));
  } catch {
    /* 同上 */
  }
}

// updateInstallingRecently 判断这次断线是不是刚点过的升级造成的。过期的标记顺手
// 清掉：一个三天前的升级不该让今天真正的宕机看起来像在升级。
export function updateInstallingRecently(): boolean {
  let raw: string | null = null;
  try {
    raw = window.localStorage.getItem(INSTALL_MARKER_KEY);
  } catch {
    return false;
  }
  if (!raw) return false;
  const startedAt = Number(raw);
  if (!Number.isFinite(startedAt) || Date.now() - startedAt > INSTALL_MARKER_TTL_MS) {
    clearUpdateInstalling();
    return false;
  }
  return true;
}
