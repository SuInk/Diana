// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Diana 浏览器控制扩展的 Service Worker：连控制面、上报标签页、执行指令。
//
// 三条不变量，改这个文件时不要破坏：
//   1. 人工接管打开时一条指令都不执行，回的是 takeover 错误码。
//   2. 只对 welcome 帧里给的站点白名单动手，且必须已经拿到该站点的浏览器权限。
//   3. 不读写 Cookie、不抓密码框、不执行控制面发来的任意脚本——协议里也没有这种指令。

import { PROTOCOL_VERSION, hostAllowed, policyHost, writeOp } from './policy.js';

const RECONNECT_MIN_MS = 2_000;
const RECONNECT_MAX_MS = 60_000;
// MV3 的 Service Worker 空闲会被回收。WebSocket 上的收发会续命，但连接断了
// 就没有事件来唤醒它了，所以再挂一个闹钟定期把它叫起来重连。
const KEEPALIVE_ALARM = 'diana-browser-control-keepalive';
const TABS_DEBOUNCE_MS = 300;

let socket = null;
let policy = null;
let connectionId = '';
let takeover = false;
let takeoverReason = '';
let lastError = '';
let reconnectDelay = RECONNECT_MIN_MS;
let reconnectTimer = null;
let tabsTimer = null;

async function loadSettings() {
  const stored = await chrome.storage.local.get(['serverURL', 'token', 'label', 'takeover']);
  takeover = Boolean(stored.takeover);
  return {
    serverURL: (stored.serverURL || '').trim(),
    token: (stored.token || '').trim(),
    label: (stored.label || '').trim(),
  };
}

function socketURL(serverURL) {
  // 用户填的是 WebUI 地址（http/https），这里换成 ws/wss 并补上端点路径。
  const base = new URL(serverURL);
  base.protocol = base.protocol === 'https:' ? 'wss:' : 'ws:';
  base.pathname = '/browser-control/v1/socket';
  base.search = '';
  base.hash = '';
  return base.toString();
}

function send(frame) {
  if (socket?.readyState !== WebSocket.OPEN) return false;
  socket.send(JSON.stringify(frame));
  return true;
}

function reply(id, data, code, message) {
  if (!id) return;
  if (code) {
    send({ type: 'result', id, code, error: message || '执行失败' });
    return;
  }
  send({ type: 'result', id, data: data ?? {} });
}

async function setBadge() {
  const connected = socket?.readyState === WebSocket.OPEN;
  const text = takeover ? '接管' : connected ? '' : '离线';
  const color = takeover ? '#b45309' : '#6b7280';
  try {
    await chrome.action.setBadgeText({ text });
    await chrome.action.setBadgeBackgroundColor({ color });
  } catch {
    // 图标状态只是提示，设置失败不该影响连接。
  }
}

async function status() {
  return {
    connected: socket?.readyState === WebSocket.OPEN,
    connectionId,
    policy,
    takeover,
    takeoverReason,
    lastError,
    extensionId: chrome.runtime.id,
  };
}

/** 上报标签页。只报白名单内的页面：用户其余标签页不关 Diana 的事。 */
function scheduleTabsReport() {
  if (tabsTimer) clearTimeout(tabsTimer);
  tabsTimer = setTimeout(reportTabs, TABS_DEBOUNCE_MS);
}

async function reportTabs() {
  tabsTimer = null;
  if (socket?.readyState !== WebSocket.OPEN || !policy) return;
  const tabs = await chrome.tabs.query({});
  const payload = tabs
    .filter((tab) => hostAllowed(policy, tab.url))
    .map((tab) => ({
      id: tab.id,
      url: tab.url,
      title: tab.title || '',
      active: Boolean(tab.active),
      window: tab.windowId,
    }));
  send({ type: 'tabs', data: { tabs: payload } });
}

async function resolveTab(params) {
  if (params.tab_id) {
    const tab = await chrome.tabs.get(params.tab_id).catch(() => null);
    if (!tab) throw { code: 'tab_unknown', message: `标签页 ${params.tab_id} 已经不在了` };
    return tab;
  }
  const [active] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
  if (!active) throw { code: 'tab_unknown', message: '当前窗口没有活动标签页' };
  return active;
}

/** 执行前确认：站点在白名单里，而且用户真的把这个站点的权限给了扩展。 */
async function ensureAllowed(url) {
  if (!hostAllowed(policy, url)) {
    const host = policyHost(url) || url;
    throw { code: 'host_denied', message: `站点 ${host} 不在已授权范围内` };
  }
  const origin = new URL(url).origin;
  const granted = await chrome.permissions.contains({ origins: [`${origin}/*`] });
  if (!granted) {
    throw {
      code: 'host_denied',
      message: `扩展还没有 ${origin} 的访问权限，请在扩展选项页里点「授权这些站点」`,
    };
  }
}

// 下面几个 pageXxx 函数会被注入到页面里执行，只能用自己的参数，不能引用模块作用域。
function pageReadScript(selector, maxChars) {
  const element = selector ? document.querySelector(selector) : document.body;
  const text = element ? element.innerText || element.textContent || '' : '';
  const limit = maxChars > 0 ? maxChars : text.length;
  return {
    url: location.href,
    title: document.title,
    selector: selector || '',
    found: Boolean(element),
    text: text.slice(0, limit),
    truncated: text.length > limit,
  };
}

function pageClickScript(selector) {
  const element = document.querySelector(selector);
  if (!element) return { url: location.href, title: document.title, clicked: false };
  element.scrollIntoView({ block: 'center' });
  element.click();
  return { url: location.href, title: document.title, clicked: true, selector };
}

function pageTypeScript(selector, text, submit) {
  const element = document.querySelector(selector);
  if (!element) return { url: location.href, title: document.title, typed: false };
  // 密码框不填。要提交凭据得用户自己来，这不是 Diana 该替人做的事。
  if (element.type === 'password') {
    return { url: location.href, title: document.title, typed: false, refused: 'password_field' };
  }
  element.focus();
  if (element.isContentEditable) {
    element.textContent = text;
  } else {
    element.value = text;
  }
  element.dispatchEvent(new Event('input', { bubbles: true }));
  element.dispatchEvent(new Event('change', { bubbles: true }));
  let submitted = false;
  if (submit) {
    const form = element.form;
    if (form) {
      form.requestSubmit ? form.requestSubmit() : form.submit();
      submitted = true;
    } else {
      for (const type of ['keydown', 'keypress', 'keyup']) {
        element.dispatchEvent(
          new KeyboardEvent(type, { key: 'Enter', code: 'Enter', keyCode: 13, bubbles: true }),
        );
      }
      submitted = true;
    }
  }
  return { url: location.href, title: document.title, typed: true, submitted, selector };
}

async function runInPage(tabId, func, args) {
  const [result] = await chrome.scripting.executeScript({ target: { tabId }, func, args });
  return result?.result ?? {};
}

/** 等一次导航结束。没有它的话紧接着的读取会读到旧页面。 */
function waitForLoad(tabId, timeoutMS = 15_000) {
  return new Promise((resolve) => {
    const done = () => {
      chrome.tabs.onUpdated.removeListener(listener);
      clearTimeout(timer);
      resolve();
    };
    const listener = (id, info) => {
      if (id === tabId && info.status === 'complete') done();
    };
    const timer = setTimeout(done, timeoutMS);
    chrome.tabs.onUpdated.addListener(listener);
  });
}

async function execute(frame) {
  const params = frame.params || {};
  const op = frame.op || '';
  if (takeover) {
    reply(frame.id, null, 'takeover', '用户正在人工接管，扩展不执行任何操作');
    return;
  }
  if (writeOp(op) && !policy?.write_enabled) {
    reply(frame.id, null, 'write_disabled', '当前授权为只读');
    return;
  }
  if (op === 'page.open') {
    await ensureAllowed(params.url);
    let tab;
    if (params.new_tab || !params.tab_id) {
      tab = await chrome.tabs.create({ url: params.url, active: false });
    } else {
      tab = await chrome.tabs.update(params.tab_id, { url: params.url });
    }
    await waitForLoad(tab.id);
    const refreshed = await chrome.tabs.get(tab.id);
    reply(frame.id, { tab_id: refreshed.id, url: refreshed.url, title: refreshed.title || '' });
    scheduleTabsReport();
    return;
  }

  const tab = await resolveTab(params);
  await ensureAllowed(tab.url);
  switch (op) {
    case 'page.read':
      reply(frame.id, await runInPage(tab.id, pageReadScript, [params.selector || '', params.max_chars || 0]));
      return;
    case 'page.click': {
      const result = await runInPage(tab.id, pageClickScript, [params.selector]);
      if (!result.clicked) {
        reply(frame.id, null, 'bad_request', `选择器没有命中元素：${params.selector}`);
        return;
      }
      reply(frame.id, result);
      scheduleTabsReport();
      return;
    }
    case 'page.type': {
      const result = await runInPage(tab.id, pageTypeScript, [
        params.selector,
        params.text ?? '',
        Boolean(params.submit),
      ]);
      if (result.refused === 'password_field') {
        reply(frame.id, null, 'bad_request', '这是密码框，扩展不会替用户填密码');
        return;
      }
      if (!result.typed) {
        reply(frame.id, null, 'bad_request', `选择器没有命中输入框：${params.selector}`);
        return;
      }
      reply(frame.id, result);
      return;
    }
    case 'page.screenshot': {
      const image = await chrome.tabs.captureVisibleTab(tab.windowId, { format: 'png' });
      reply(frame.id, { url: tab.url, title: tab.title || '', image });
      return;
    }
    default:
      reply(frame.id, null, 'unsupported_op', `扩展不认识这条指令：${op}`);
  }
}

function handleFrame(frame) {
  switch (frame.type) {
    case 'welcome': {
      const data = frame.data || {};
      policy = data.policy || null;
      connectionId = data.connection_id || '';
      lastError = '';
      reconnectDelay = RECONNECT_MIN_MS;
      setBadge();
      // 控制面也要知道当前是不是有人在接管，重连之后状态不能对不上。
      send({ type: 'takeover', data: { active: takeover, reason: takeoverReason } });
      scheduleTabsReport();
      return;
    }
    case 'command':
      execute(frame).catch((err) => {
        const code = err?.code || 'extension_error';
        reply(frame.id, null, code, err?.message || String(err));
      });
      return;
    case 'takeover': {
      // 控制面（WebUI）那边也能切接管，两侧状态保持一致。
      const data = frame.data || {};
      takeover = Boolean(data.active);
      takeoverReason = data.reason || '';
      chrome.storage.local.set({ takeover });
      setBadge();
      return;
    }
    case 'ping':
      send({ type: 'pong' });
      return;
    case 'error':
      lastError = frame.error || '控制面拒绝了连接';
      // 鉴权类失败重连也没用，退到最大间隔，等用户去改配置。
      if (frame.code === 'unauthorized' || frame.code === 'protocol_version') {
        reconnectDelay = RECONNECT_MAX_MS;
      }
      return;
    default:
      // 不认识的帧丢掉：控制面版本更新可能多发帧，不该让扩展崩在这里。
      return;
  }
}

function scheduleReconnect() {
  if (reconnectTimer) return;
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null;
    connect();
  }, reconnectDelay);
  reconnectDelay = Math.min(reconnectDelay * 2, RECONNECT_MAX_MS);
}

async function connect() {
  if (socket && (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)) {
    return;
  }
  const settings = await loadSettings();
  if (!settings.serverURL || !settings.token) {
    lastError = '还没填 Diana 地址和令牌';
    await setBadge();
    return;
  }
  let target;
  try {
    target = socketURL(settings.serverURL);
  } catch {
    lastError = 'Diana 地址不是合法的 URL';
    await setBadge();
    return;
  }
  socket = new WebSocket(target);
  socket.onopen = () => {
    // 令牌放在握手帧里，不放在 URL 上：URL 会进访问日志和历史记录。
    send({
      type: 'hello',
      data: {
        protocol_version: PROTOCOL_VERSION,
        token: settings.token,
        extension_id: chrome.runtime.id,
        extension_name: chrome.runtime.getManifest().name,
        browser: navigator.userAgentData?.brands?.at(-1)?.brand || 'Chromium',
        browser_version: navigator.userAgentData?.brands?.at(-1)?.version || '',
        label: settings.label,
        capabilities: ['page.read', 'page.screenshot', 'page.open', 'page.click', 'page.type'],
      },
    });
  };
  socket.onmessage = (event) => {
    let frame;
    try {
      frame = JSON.parse(event.data);
    } catch {
      return;
    }
    handleFrame(frame);
  };
  socket.onclose = () => {
    socket = null;
    policy = null;
    connectionId = '';
    setBadge();
    scheduleReconnect();
  };
  socket.onerror = () => {
    lastError = lastError || '连不上 Diana';
  };
}

function disconnect() {
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
  if (socket) {
    socket.onclose = null;
    socket.close();
    socket = null;
  }
  policy = null;
  connectionId = '';
  setBadge();
}

async function setTakeover(active, reason) {
  takeover = Boolean(active);
  takeoverReason = reason || '';
  await chrome.storage.local.set({ takeover });
  send({ type: 'takeover', data: { active: takeover, reason: takeoverReason } });
  await setBadge();
}

// 工具栏图标就是接管开关：用户不需要翻到选项页才能把 Diana 挡在外面。
chrome.action.onClicked.addListener(() => {
  setTakeover(!takeover, takeover ? '' : '用户点击扩展图标接管');
});

chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  switch (message?.type) {
    case 'status':
      status().then(sendResponse);
      return true;
    case 'connect':
      reconnectDelay = RECONNECT_MIN_MS;
      disconnect();
      connect().then(() => status().then(sendResponse));
      return true;
    case 'disconnect':
      disconnect();
      status().then(sendResponse);
      return true;
    case 'takeover':
      setTakeover(message.active, message.reason).then(() => status().then(sendResponse));
      return true;
    default:
      return false;
  }
});

chrome.tabs.onUpdated.addListener(scheduleTabsReport);
chrome.tabs.onRemoved.addListener(scheduleTabsReport);
chrome.tabs.onActivated.addListener(scheduleTabsReport);

chrome.alarms.create(KEEPALIVE_ALARM, { periodInMinutes: 1 });
chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === KEEPALIVE_ALARM) connect();
});

chrome.runtime.onStartup.addListener(() => connect());
chrome.runtime.onInstalled.addListener(() => connect());
connect();
