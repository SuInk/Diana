// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import type { InjectionKey, Ref } from "vue";

/**
 * 运行记录两档共用的页头动作位。
 *
 * 传的是元素本身，不是 `#id` 选择器：侧边栏的视图被 KeepAlive 缓存着，事件和日志
 * 两个地址各留一个实例，用 id 选择器时 document 里会同时存在两个同名容器，
 * querySelector 只认第一个——两档的按钮于是全挤进同一个（还可能是隐藏的那个），
 * 看起来就是切一次档多出一排。元素引用跟着实例走，缓存起来的那份自然跟着隐藏。
 */
export const recordsActionsHost: InjectionKey<Ref<HTMLElement | null>> = Symbol("records-actions-host");
