// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { originPatterns } from './policy.js';

const field = (id) => document.getElementById(id);
const text = (id, value) => {
  field(id).textContent = value;
};

let lastPolicy = null;

async function refresh() {
  const status = await chrome.runtime.sendMessage({ type: 'status' });
  lastPolicy = status?.policy || null;
  text('extension-id', status?.extensionId || chrome.runtime.id);
  text('connection', status?.connected ? `已连接（${status.connectionId || '—'}）` : '未连接');
  if (!lastPolicy) {
    text('mode', '—');
    text('hosts', '—');
  } else {
    text('mode', lastPolicy.write_enabled ? '可读写（能点击和输入）' : '只读（只能读取和截图）');
    const hosts = lastPolicy.allowed_hosts || [];
    text('hosts', hosts.length ? hosts.join('、') : '控制面还没授权任何站点');
  }
  text('error', status?.lastError || '—');
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
  // 握手要一个来回，等一下再读状态，省掉用户自己刷新。
  setTimeout(refresh, 600);
});

field('disconnect').addEventListener('click', async () => {
  await chrome.runtime.sendMessage({ type: 'disconnect' });
  await refresh();
});

// 权限申请必须由用户点击触发，所以只能放在这一页，不能由 Service Worker 自己发起。
field('grant').addEventListener('click', async () => {
  if (!lastPolicy) {
    text('error', '先连上 Diana，拿到站点白名单之后才知道要申请哪些权限');
    return;
  }
  const origins = originPatterns(lastPolicy);
  if (!origins.length) {
    text('error', 'Diana 还没授权任何站点，没有可申请的权限');
    return;
  }
  const granted = await chrome.permissions.request({ origins });
  text('error', granted ? '—' : '你拒绝了站点权限，Diana 仍然无法操作这些页面');
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

load();
