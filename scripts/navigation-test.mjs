import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import path from "node:path";

const require = createRequire(path.join(process.env.LODESTAR_UI_DEPS || "/tmp/lodestar-ui-deps", "package.json"));
const { JSDOM } = require("jsdom");
const { build } = require("esbuild");
const bundle = await build({
  stdin: {
    contents: 'export * from "./web/admin-navigation.js"; export { navHTML, adminPages } from "./web/admin-pages.js"; export { overviewPage } from "./web/admin-overview.js"; export { state, restoreRoute, go, setRenderer, setPageView } from "./web/admin-state.js";',
    resolveDir: fileURLToPath(new URL("../", import.meta.url)),
  },
  bundle: true, format: "iife", globalName: "Navigation", write: false,
});
const dom = new JSDOM('<nav id="nav"></nav><main id="admin-content"></main><div id="toast"></div>', {
  url: "http://localhost/admin", runScripts: "outside-only",
});
const window = dom.window;
window.eval(bundle.outputFiles[0].text);
const app = window.Navigation;
app.state.container = window.document;
window.document.querySelector("#nav").innerHTML = app.navHTML();
assert.equal(window.document.querySelectorAll(".nav-section").length, 6);
assert.equal(new Set(app.adminPages.map((page) => page.id)).size, app.adminPages.length);
assert(app.adminPages.every((page) => typeof page.page === "function"), "every route needs a renderer");
assert.equal(app.adminPages.find((page) => page.id === "security").section, "assets");
assert.equal(app.adminPages.find((page) => page.id === "backup").section, "system");
assert.equal(app.adminPages.find((page) => page.id === "notifications").section, "system");
assert.equal(app.routeFromHash("#unknown").tab, "overview");
assert.equal(app.routeFromHash("#finance/%ZZ").view, "");
assert.equal(app.routeFromHash("#finance/unknown").view, "");
window.history.replaceState(null, "", "#finance/transactions");
app.restoreRoute();
assert.equal(app.state.tab, "finance");
assert.equal(app.state.pageViews.finance, "transactions");
await app.go("automation");
app.setPageView("pending");
assert.equal(window.location.hash, "#automation/pending");
window.history.replaceState(null, "", "#settings/assistant");
app.restoreRoute();
assert.equal(app.state.settingsView, "assistant");
await app.go("overview");

const now = new Date();
const start = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime() / 1000;
const unsafe = '<img src=x onerror="alert(1)">';
let fail = false;
const cards = [
  ...["nodes", "billing", "tasks", "alerts", "assets", "finance", "security"].map((id) => ({ id, title: id, page: id === "billing" ? "nodes" : id, value: "正常", tone: "good" })),
  { id: "automation", title: "自动化", page: "automation", value: "1 条规则", note: "1 项待确认", tone: "warn" },
];
window.fetch = async (url) => {
  if (fail) return new Response(JSON.stringify({ error: "不可用" }), { status: 503 });
  const data = url.includes("/overview") ? { cards, generated_at: Date.now() / 1000 }
    : url.includes("/todo?") ? { items: [
      { id: "overdue", title: unsafe, due_at: start - 60 },
      { id: "today", title: "今天到期", due_at: start + 36000 },
      { id: "tomorrow", title: "明天的任务", due_at: start + 86400 + 60 },
      { id: "unscheduled", title: "未排期", due_at: 0 },
    ] }
    : url.includes("/calendar/today") ? { events: [{ title: "今天的日程", source_name: "私人日历", start_at: start + 32400, all_day: false }] }
    : url.includes("/intel/items") ? { items: [{ title: unsafe, at: start, source_name: "消息源" }] }
    : { entries: [{ title: "今日简报", body: unsafe, source: "brief" }] };
  return new Response(JSON.stringify(data));
};
await app.overviewPage(Promise.resolve());
const content = window.document.querySelector("#admin-content");
assert.equal(content.querySelector("img"), null, "all source content must be escaped");
assert(content.querySelector(".today-attention").textContent.includes(unsafe));
assert(content.querySelector('.today-attention [data-go="automation"][data-view="pending"]'));
assert(content.querySelector(".today-schedule").textContent.includes("今天到期"));
assert(content.querySelector(".today-schedule").textContent.includes("今天的日程"));
assert(!content.querySelector(".today-schedule").textContent.includes("明天的任务"));
assert(content.querySelector(".today-schedule").textContent.includes("1 项待办未设时间"));
assert(content.querySelector(".today-brief").textContent.includes(unsafe));
assert.equal(content.querySelector(".today-status-details").open, false, "secondary metrics stay collapsed");
await content.querySelector('[data-go="todo"][data-view="calendar"]').onclick();
assert.equal(app.state.tab, "todo");
assert.equal(window.location.hash, "#todo/calendar");
app.state.pageCleanup?.();

await app.go("overview");
fail = true;
await app.overviewPage(Promise.resolve());
assert(content.textContent.includes("状态摘要读取失败"));
assert(content.textContent.includes("日历读取失败"));
assert(content.textContent.includes("情报读取失败"));
assert(!content.textContent.includes("当前没有需要处理的事项"), "failed reads must not claim everything is clear");
app.state.pageCleanup?.();

const responses = [];
window.fetch = () => new Promise((resolve) => { responses.push(resolve); });
const rendering = app.overviewPage(Promise.resolve());
app.state.tab = "finance";
content.textContent = "财务页面";
for (const respond of responses) respond(new Response("{}"));
await rendering;
assert.equal(content.textContent, "财务页面", "a late home response must not overwrite the next page");
responses.length = 0;
window.fetch = (url) => new Promise((resolve) => {
  responses.push(() => resolve(new Response(JSON.stringify(url.includes("/notification") ? { channel: "none" } : []))));
});
app.state.tab = "alerts";
const oldAlerts = app.adminPages.find((page) => page.id === "alerts").page(Promise.resolve());
app.state.tab = "notifications";
content.textContent = "通知配置页面";
for (const respond of responses) respond();
await oldAlerts;
assert.equal(content.textContent, "通知配置页面", "a late alert response must not overwrite notification settings");
dom.window.close();
console.log("PASS: navigation routes, hierarchy, daily agenda, escaped content, partial failures and stale responses.");
