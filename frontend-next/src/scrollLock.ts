// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 弹窗打开时锁住背景滚动，避免在弹窗上滚动滚到底后带着整页一起滚（滑动穿透）。
// 用计数而不是布尔：弹窗可能叠开，先关的那个不能把还开着的那个的锁一起解掉。
let openCount = 0;
let restoreRootOverflow = "";
let restoreBodyOverflow = "";
let restorePaddingRight = "";

export function lockBodyScroll(): void {
  if (typeof document === "undefined") return;
  openCount += 1;
  if (openCount > 1) return;
  const root = document.documentElement;
  const body = document.body;
  // 这套布局真正的滚动容器是 html，只锁 body 的话背景照样能滚，两个一起锁才拦得住。
  restoreRootOverflow = root.style.overflow;
  restoreBodyOverflow = body.style.overflow;
  restorePaddingRight = body.style.paddingRight;
  // 滚动条消失会让整页横向跳一下，用等宽内边距补回去；宽度必须在隐藏之前量。
  const scrollbarWidth = window.innerWidth - root.clientWidth;
  if (scrollbarWidth > 0) {
    const current = Number.parseFloat(window.getComputedStyle(body).paddingRight) || 0;
    body.style.paddingRight = `${current + scrollbarWidth}px`;
  }
  root.style.overflow = "hidden";
  body.style.overflow = "hidden";
}

export function releaseBodyScroll(): void {
  if (typeof document === "undefined" || openCount === 0) return;
  openCount -= 1;
  if (openCount > 0) return;
  document.documentElement.style.overflow = restoreRootOverflow;
  document.body.style.overflow = restoreBodyOverflow;
  document.body.style.paddingRight = restorePaddingRight;
  restoreRootOverflow = "";
  restoreBodyOverflow = "";
  restorePaddingRight = "";
}

// 仅供测试重置计数。
export function resetBodyScrollLock(): void {
  openCount = 0;
  restoreRootOverflow = "";
  restoreBodyOverflow = "";
  restorePaddingRight = "";
}
