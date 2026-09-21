// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { originPatterns } from './policy.js';

const field = (id) => document.getElementById(id);
const text = (id, value) => {
  field(id).textContent = value;
};

let lastPolicy = null;
// 这一页自己产生的错误（比如申请站点权限失败）。Service Worker 不知道它，
// 所以渲染时要单独保留，否则下一次轮询就把提示冲掉了。
let localError = '';

function setLocalError(message) {
  localError = message;
  text('error', message || '—');
}

async function refresh() {
  render(await chrome.runtime.sendMessage({ type: 'status' }));
}

function render(status) {
  lastPolicy = status?.policy || null;
  text('extension-id', status?.extensionId || chrome.runtime.id);
  text('connection', status?.connected ? `已连接（${status.connectionId || '—'}）` : '未连接');
  if (!lastPolicy) {
    text('mode', '—');
    text('hosts', '—');
  } else {
    text('mode', lastPolicy.write_enabled ? '可读写（能点击和输入）' : '只读（只能读取，不点不填不跳转）');
    const hosts = lastPolicy.allowed_hosts || [];
    text('hosts', hosts.length ? hosts.join('、') : '控制面还没授权任何站点');
  }
  text('error', status?.lastError || localError || '—');
  field('takeover').textContent = status?.takeover ? '交还控制权' : '人工接管';
}

async function load() {
  const stored = await chrome.storage.local.get(['serverURL', 'token', 'label']);
  field('server').value = stored.serverURL || '';
  field('token').value = stored.token || '';
  field('label').value = stored.label || '';
  await refresh();
}

field('save').addEventListener('click', async () => {
  await chrome.storage.local.set({
    serverURL: field('server').value.trim(),
    token: field('token').value.trim(),
    label: field('label').value.trim(),
  });
  await chrome.runtime.sendMessage({ type: 'connect' });
  await refresh();
});

field('disconnect').addEventListener('click', async () => {
  await chrome.runtime.sendMessage({ type: 'disconnect' });
  await refresh();
});

// 权限申请必须由用户点击触发，所以只能放在这一页，不能由 Service Worker 自己发起。
field('grant').addEventListener('click', async () => {
  if (!lastPolicy) {
    setLocalError('还没拿到 Diana 的站点白名单，等连接状态显示已连接再试一次');
    return;
  }
  const origins = originPatterns(lastPolicy);
  if (!origins.length) {
    setLocalError('Diana 还没授权任何站点，没有可申请的权限');
    return;
  }
  setLocalError('');
  try {
    const granted = await chrome.permissions.request({ origins });
    setLocalError(granted ? '' : '你拒绝了站点权限，Diana 仍然无法操作这些页面');
  } catch (err) {
    // 申请本身被浏览器拒收（比如模式不在 manifest 的可选权限里）时，
    // 不能让这一步静默失败：用户点了按钮什么都没发生是最难排查的一种。
    setLocalError(`申请站点权限失败：${err?.message || err}`);
  }
  await refresh();
});

field('takeover').addEventListener('click', async () => {
  const status = await chrome.runtime.sendMessage({ type: 'status' });
  await chrome.runtime.sendMessage({
    type: 'takeover',
    active: !status?.takeover,
    reason: status?.takeover ? '' : '用户在扩展选项页接管',
  });
  await refresh();
});

// Service Worker 的状态是异步到的：保存之后握手还要一个来回，welcome 帧里
// 才带着策略。一次性延时刷新会停在「已连接（—）」上，连「授权这些站点」
// 都会因为还没拿到白名单而拒绝干活，所以这里改成事件驱动 + 兜底轮询。
chrome.runtime.onMessage.addListener((message) => {
  if (message?.type === 'state') {
    render(message.state);
  }
  return false;
});

// 不做定时轮询：每次 sendMessage 都会把已经休眠的 Service Worker 叫醒，
// 而它一醒就会重新建一条控制面连接，轮询等于按秒制造重连。
// 广播之外只在这一页重新回到前台时补一次。
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible') {
    refresh().catch(() => {});
  }
});

load();
