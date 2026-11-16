import { api, esc, guard } from "./api.js";
import { content, go, render, state } from "./admin-state.js";
import { actionName } from "./admin-remote.js";

const REFRESH_MS = 60000;
let generation = 0;
let poller;

const RESOURCES = { assets: "资产", reveal: "敏感字段", jobs: "定时任务", "api-keys": "访问密钥", nodes: "节点", tasks: "任务", sites: "网站", proxy: "代理", inbounds: "入站", templates: "规则模板", sources: "外部订阅", forwards: "转发", tunnels: "隧道", users: "使用者", certs: "证书", dns: "DNS 账号", backups: "备份", docker: "Docker", containers: "容器", images: "镜像", compose: "Compose", "alert-rules": "告警规则", notification: "通知渠道", themes: "主题", plugins: "插件", panel: "面板设置", account: "账号", totp: "两步验证", agents: "Agent", "block-checks": "被墙检测", regiondrift: "地区偏移", "ip-guard": "地区偏移", events: "事件" };
const VERBS = { sync: "同步", run: "执行", test: "测试", issue: "签发", renew: "续期", deploy: "下发", upgrade: "升级", restart: "重启", start: "启动", stop: "停止", reset: "重置", rotate: "轮换", order: "排序", import: "导入", export: "导出", locate: "定位", pause: "暂停", resume: "恢复", enable: "启用", disable: "停用" };

export function auditLabel(action) {
  const match = /^(GET|POST|PUT|PATCH|DELETE) \/api\/v1\/admin\/(.+)$/.exec(action || "");
  if (!match) return actionName(action);
  const parts = match[2].split("/").filter((part) => !part.startsWith("{"));
  const names = parts.filter((part) => RESOURCES[part]).map((part) => RESOURCES[part]);
  const verb = VERBS[parts.at(-1)] || { POST: "新建", PUT: "修改", PATCH: "修改", DELETE: "删除" }[match[1]] || "查看";
  return `${verb} ${names.join(" · ") || parts.join("/")}`;
}

const clock = (seconds) => new Date(seconds * 1000).toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false });
const dateTime = (seconds) => new Date(seconds * 1000).toLocaleString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false });
const empty = (message) => `<p class="ov-empty">${esc(message)}</p>`;
const heading = (title, page, label, view = "") => `<div class="panel-head"><h2>${esc(title)}</h2><button type="button" class="quiet" data-go="${page}"${view ? ` data-view="${view}"` : ""}>${esc(label)}</button></div>`;

function attention(cards, todos, start) {
  const rows = [];
  for (const card of cards || []) {
    if (card.page === "todo" && todos) continue;
    const items = (card.items || []).filter((item) => ["bad", "warn"].includes(item.tone));
    for (const item of items) rows.push({ ...item, from: card.title, page: card.page });
    if (!items.length && ["bad", "warn"].includes(card.tone)) {
      rows.push({
        tone: card.tone, label: card.id === "automation" ? "有操作等待确认" : `${card.title}：${card.value}`,
        note: card.note, from: card.title, page: card.page, view: card.id === "automation" ? "pending" : "",
      });
    }
  }
  for (const item of todos?.items || []) {
    if (item.due_at > 0 && item.due_at < start) rows.push({
      tone: "warn", label: item.title, note: `已逾期 · ${dateTime(item.due_at)}`, from: "待办", page: "todo", view: "open",
    });
  }
  rows.sort((first, second) => (first.tone === second.tone ? 0 : first.tone === "bad" ? -1 : 1));
  const list = rows.slice(0, 8).map((row) => `<button type="button" class="ov-todo ${row.tone}" data-go="${esc(row.page || "timeline")}"${row.view ? ` data-view="${row.view}"` : ""}><i aria-hidden="true"></i><span><b>${esc(row.label)}</b><small>${esc(row.note || "")}</small></span><em>${esc(row.from)}</em></button>`).join("");
  const warnings = `${cards ? "" : empty("状态摘要读取失败，请刷新重试。")}${todos ? "" : empty("待办读取失败，尚不能确认是否有逾期事项。")}`;
  return `<section data-part="panel" class="panel today-attention"><div class="panel-head"><div><h2>需要我处理</h2><p>先处理异常、到期与等待确认的事</p></div>${rows.length ? `<span class="pill warn">${rows.length} 项</span>` : ""}</div>${warnings}${list ? `<div class="ov-todos">${list}</div>` : cards && todos ? empty("当前没有需要处理的事项。") : ""}${rows.length > 8 ? empty(`还有 ${rows.length - 8} 项，请进入对应页面查看。`) : ""}</section>`;
}

function agenda(todos, calendar, start, end) {
  const events = (calendar?.events || []).map((event) => ({
    title: event.title, at: event.all_day ? start : Math.max(start, event.start_at), page: "todo", view: "calendar",
    time: event.all_day ? "全天" : event.start_at < start ? "进行中" : clock(event.start_at),
    note: [event.source_name, event.location].filter(Boolean).join(" · "),
  }));
  const tasks = (todos?.items || []).filter((item) => item.due_at >= start && item.due_at < end).map((item) => ({
    title: item.title, at: item.due_at, page: "todo", view: "open", time: clock(item.due_at), note: "待办到期",
  }));
  const items = [...events, ...tasks].sort((first, second) => first.at - second.at);
  const list = items.slice(0, 6).map((item) => `<li><button type="button" class="today-agenda-item" data-go="${item.page}" data-view="${item.view}"><time>${esc(item.time)}</time><span><b>${esc(item.title)}</b><small>${esc(item.note)}</small></span></button></li>`).join("");
  const unscheduled = (todos?.items || []).filter((item) => !item.due_at).length;
  return `<section data-part="panel" class="panel today-schedule">${heading("今天的安排", "todo", "查看日程", "calendar")}${!calendar ? empty("日历读取失败，请刷新重试。") : ""}${!todos ? empty("待办读取失败，请刷新重试。") : ""}${list ? `<ul class="today-agenda">${list}</ul>` : calendar && todos ? empty("今天没有已安排的日程或到期待办。") : ""}${items.length > 6 ? empty(`另有 ${items.length - 6} 项，进入日程查看。`) : ""}${unscheduled ? `<button type="button" class="today-unscheduled" data-go="todo" data-view="open">还有 ${unscheduled} 项待办未设时间</button>` : ""}</section>`;
}

function intelligence(data, brief) {
  const items = data?.items || [];
  const list = items.slice(0, 4).map((item) => `<li><button type="button" class="today-intel-item" data-go="intel" data-view="items"><span>${esc(item.source_name || "订阅更新")}</span><b>${esc(item.title)}</b><small>${esc(dateTime(item.at))}${item.starred ? " · 已星标" : ""}</small></button></li>`).join("");
  const latest = brief?.entries?.[0];
  return `<section data-part="panel" class="panel today-intelligence">${heading("值得关注", "intel", "查看情报")}${data ? list ? `<ul class="today-intel-list">${list}</ul>` : empty("还没有未读更新。可以在情报中添加订阅来源。") : empty("情报读取失败，请刷新重试。")}
    <div class="today-brief"><div><h3>${latest ? esc(latest.title) : "每日简报"}</h3><p>${latest ? esc((latest.body || "").slice(0, 220)) : brief ? "把到期、日程和关注的更新汇成一份简报。" : "简报记录读取失败，请刷新重试。"}</p></div><button type="button" data-go="brief">${latest ? "查看简报" : "设置简报"}</button></div></section>`;
}

function cardHTML(card) {
  return `<button type="button" class="overview-card ${["good", "warn", "bad"].includes(card.tone) ? card.tone : ""}" data-card="${esc(card.id)}" data-go="${esc(card.page || "timeline")}"><span class="overview-card-title">${esc(card.title)}</span><strong>${esc(card.value)}</strong><small>${esc(card.note || "")}</small></button>`;
}

function status(data) {
  if (!data) return `<section class="today-status"><h2>状态摘要</h2>${empty("状态读取失败，请刷新重试。")}</section>`;
  const primary = ["assets", "finance", "nodes", "security"];
  const cards = data.cards || [];
  const main = primary.map((id) => cards.find((card) => card.id === id)).filter(Boolean);
  const rest = cards.filter((card) => !primary.includes(card.id));
  return `<section class="today-status"><h2>状态摘要</h2><div class="today-status-grid">${main.map(cardHTML).join("")}</div>${rest.length ? `<details class="today-status-details"><summary>其他状态 <span>${rest.length} 项</span></summary><div class="overview-cards">${rest.map(cardHTML).join("")}</div></details>` : '<div class="overview-cards"></div>'}</section>`;
}

export async function overviewPage(nodesReady) {
  clearTimeout(poller);
  const request = ++generation;
  const now = new Date();
  const start = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime() / 1000;
  const end = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1).getTime() / 1000;
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const soft = (promise) => promise.catch(() => null);
  const [data, todos, calendar, intel, brief] = await Promise.all([
    soft(api("/admin/overview")),
    soft(api("/admin/todo?state=open")),
    soft(api(`/admin/calendar/today?timezone=${encodeURIComponent(timezone)}`)),
    soft(api("/admin/intel/items?unread=1&limit=4")),
    soft(api("/admin/timeline?sources=brief&limit=1")),
    nodesReady,
  ]);
  if (request !== generation || state.tab !== "overview") return;
  const root = content();
  const date = now.toLocaleDateString("zh-CN", { month: "long", day: "numeric", weekday: "long" });
  root.innerHTML = `<div class="today-workspace"><section data-part="page-heading" class="page-heading today-heading"><div><h1>今天</h1><p>${esc(date)} · 按你的本地时间安排</p></div><div class="heading-actions"><button type="button" id="refresh-overview" class="quiet">刷新</button><button type="button" id="ask-assistant" class="primary">帮我梳理今天</button></div></section>
    ${attention(data?.cards, todos, start)}
    <div class="today-columns">${agenda(todos, calendar, start, end)}${intelligence(intel, brief)}</div>
    ${status(data)}
    <p class="today-updated">更新于 ${esc(clock(Date.now() / 1000))} · 每分钟自动刷新</p></div>`;
  root.querySelector("#refresh-overview").onclick = guard(() => go("overview"));
  root.querySelector("#ask-assistant").onclick = guard(async () => {
    state.assistantPrompt = "帮我梳理今天的日程、到期待办和需要处理的事项";
    await go("assistant");
  });
  root.querySelectorAll("[data-go]").forEach((button) => {
    button.onclick = guard(async () => {
      if (button.dataset.view) {
        state.pageViews[button.dataset.go] = button.dataset.view;
        if (button.dataset.go === "alerts") state.alertView = button.dataset.view;
      }
      await go(button.dataset.go);
    });
  });
  const tick = () => {
    if (state.tab !== "overview") return;
    if (document.hidden || document.querySelector("dialog[open]") || document.body.classList.contains("admin-nav-open")) {
      poller = setTimeout(tick, REFRESH_MS);
    } else render().catch(() => { poller = setTimeout(tick, REFRESH_MS); });
  };
  poller = setTimeout(tick, REFRESH_MS);
  state.pageCleanup = () => clearTimeout(poller);
}
