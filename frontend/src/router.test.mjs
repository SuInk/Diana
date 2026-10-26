import assert from "node:assert/strict";
import { test } from "node:test";

// router 在模块加载时就读 window.location，所以每个用例先铺好假地址栏，再用带
// 查询串的 specifier 拿一份全新的模块实例。
let instance = 0;
async function load(href) {
  const url = new URL(href, "http://console.local");
  const window = {
    location: { pathname: url.pathname, search: url.search, hash: url.hash },
    history: {
      calls: [],
      pushState(_state, _title, path) { this.calls.push(["push", path]); apply(path); },
      replaceState(_state, _title, path) { this.calls.push(["replace", path]); apply(path); }
    },
    listeners: {},
    addEventListener(name, fn) { (this.listeners[name] ||= []).push(fn); }
  };
  function apply(path) {
    const next = new URL(path, "http://console.local");
    window.location.pathname = next.pathname;
    window.location.search = next.search;
    window.location.hash = next.hash;
  }
  globalThis.window = window;
  const router = await import(`./router.ts?case=${instance++}`);
  return { router, window, here: () => `${window.location.pathname}${window.location.search}${window.location.hash}` };
}

test("clean paths map to views and keep their query", async () => {
  for (const [href, view] of [["/", "dashboard"], ["/admin-chat", "admin-chat"], ["/bot", "bot"], ["/groups?group=123", "groups"], ["/plugins?settings=rss", "plugins"]]) {
    const { router, here } = await load(href);
    router.setupRouter();
    assert.equal(router.currentView.value, view);
    assert.equal(here(), href, `${href} must stay as written`);
  }
  const { router } = await load("/plugins?settings=rss");
  assert.equal(router.viewQuery().get("settings"), "rss");
});

// hash 路由时代贴出去的链接不能废：连 query 一起搬到干净地址上。
test("legacy #/ links are rewritten in place, not pushed onto history", async () => {
  const { router, window, here } = await load("/#/groups?group=123");
  router.setupRouter();
  assert.equal(router.currentView.value, "groups");
  assert.equal(here(), "/groups?group=123");
  assert.deepEqual(window.history.calls, [["replace", "/groups?group=123"]]);

  const home = await load("/#/dashboard");
  home.router.setupRouter();
  assert.equal(home.router.currentView.value, "dashboard");
  assert.equal(home.here(), "/");
});

// 这一页的路由标识从 llm 改成 provider，老地址要认，并且收敛到新写法。
test("renamed views resolve and canonicalize", async () => {
  const { router, here } = await load("/llm");
  router.setupRouter();
  assert.equal(router.currentView.value, "provider");
  assert.equal(here(), "/provider");
});

// 认不出来的地址一律回首页，参数一并丢掉：路径都不认识，带过去只会更迷惑。
test("unknown paths redirect to the home page without their query", async () => {
  for (const href of ["/nope", "/bot/extra/../../etc", "/settings2?x=1", "/#/nope?x=1"]) {
    const { router, here } = await load(href);
    router.setupRouter();
    assert.equal(router.currentView.value, "dashboard", href);
    assert.equal(here(), "/", `${href} must land on the home page`);
  }
});

test("navigate pushes one entry and updates the view without a popstate", async () => {
  const { router, window, here } = await load("/");
  router.setupRouter();
  router.navigate("groups", { group: "5" });
  assert.equal(router.currentView.value, "groups");
  assert.equal(here(), "/groups?group=5");
  assert.deepEqual(window.history.calls, [["push", "/groups?group=5"]]);

  // 同一个地址不再重复入栈，否则返回键要按两次才回得去。
  router.navigate("groups", { group: "5" });
  assert.equal(window.history.calls.length, 1);
  // 首页回到根路径，而不是 /dashboard。
  router.navigate("dashboard");
  assert.equal(here(), "/");
});

test("back and forward resolve through popstate", async () => {
  const { router, window, here } = await load("/bot");
  router.setupRouter();
  assert.equal(router.currentView.value, "bot");
  window.location.pathname = "/tasks";
  window.location.search = "";
  window.listeners.popstate.forEach((fn) => fn());
  assert.equal(router.currentView.value, "tasks");
  assert.equal(here(), "/tasks");
});

test("viewPath builds the addresses used in links", async () => {
  const { router } = await load("/");
  assert.equal(router.viewPath("dashboard"), "/");
  assert.equal(router.viewPath("bot"), "/bot");
  assert.equal(router.viewPath("groups", new URLSearchParams({ group: "7" })), "/groups?group=7");
});

// 世界书从机器人人设页搬进了记忆：地址要能直达，侧边栏亮「记忆」那一项。
test("world book has its own address under the archive nav item", async () => {
  const { router, here } = await load("/world-book");
  router.setupRouter();
  assert.equal(router.currentView.value, "world-book");
  assert.equal(here(), "/world-book");
  assert.equal(router.navItemForView("world-book"), "users");
});

// 浏览器与扩展各自拥有导航入口，直达和返回时不能高亮到另一页。
test("browser has independent navigation from extensions", async () => {
  const { router, here } = await load("/browser");
  router.setupRouter();
  assert.equal(router.currentView.value, "browser");
  assert.equal(here(), "/browser");
  assert.equal(router.navItemForView("browser"), "browser");
  assert.equal(router.navItems.find(item => item.id === "browser")?.label, "浏览器");
  router.navigate("plugins");
  assert.equal(here(), "/plugins");
  assert.equal(router.navItemForView(router.currentView.value), "plugins");
});

test("memory and home routes belong to separate navigation entries", async () => {
  const { router } = await load("/users");
  for (const view of ["users", "notebook", "world-book"]) {
    assert.equal(router.navItemForView(view), "users");
  }
  for (const view of ["space", "self-notes", "feed"]) {
    router.navigate(view);
    assert.equal(router.currentView.value, view);
    assert.equal(router.navItemForView(view), "space");
  }
  assert.equal(router.navItems.find(item => item.id === "users")?.label, "记忆");
  assert.equal(router.navItems.find(item => item.id === "space")?.label, "小窝");
});
