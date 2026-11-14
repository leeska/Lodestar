// 后台总览：整个系统的汇总页，也是最常打开的一页。从上到下：
// 1. 一排关键数字（在线、实时速率、本周期流量、平均 CPU、每月成本、正在告警）；
// 2. 「需要处理」：把各模块卡片里语气为警告 / 严重的条目汇成一张待办，严重的排前面；
//    旁边是「节点负载」，按 CPU / 内存 / 磁盘里最高的那项排序；
// 3. 各模块的状态卡（GET /admin/overview），点卡片进对应页面；
// 4. 最近的告警事件和最近的管理操作。
// 停在本页时每 15 秒自动刷新一次；有弹窗或页面在后台时跳过。某一块读不到只影响那一块。
import { api, esc } from "./api.js";
import { content, go, render, state } from "./admin-state.js";
import { bytes, percent, kindName, monthlyCostIn, remainingValueIn, fxHint } from "./format.js";
import { actionName } from "./admin-remote.js";

const toneClass = { good: "good", warn: "warn", bad: "bad" };
const REFRESH_MS = 15000;
let poller;

const ago = (unix) => {
  const s = Math.max(0, Math.round(Date.now() / 1000 - unix));
  if (s < 60) return "刚刚";
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`;
  if (s < 86400) return `${Math.floor(s / 3600)} 小时前`;
  if (s < 86400 * 30) return `${Math.floor(s / 86400)} 天前`;
  return new Date(unix * 1000).toLocaleDateString("zh-CN");
};
// 审计里 HTTP 操作记的是「方法 路径」，总览里换成一句中文：删除 网站、同步 网站、新建 代理 · 规则模板。
const RESOURCES = { nodes: "节点", tasks: "任务", sites: "网站", proxy: "代理", inbounds: "入站", templates: "规则模板", sources: "外部订阅", forwards: "转发", tunnels: "隧道", users: "使用者", certs: "证书", dns: "DNS 账号", backups: "备份", docker: "Docker", containers: "容器", images: "镜像", compose: "Compose", "alert-rules": "告警规则", notification: "通知渠道", themes: "主题", plugins: "插件", panel: "面板设置", account: "账号", totp: "两步验证", agents: "Agent", "block-checks": "被墙检测", regiondrift: "地区偏移", "ip-guard": "地区偏移", events: "事件" };
const VERBS = { sync: "同步", run: "执行", test: "测试", issue: "签发", renew: "续期", deploy: "下发", upgrade: "升级", restart: "重启", start: "启动", stop: "停止", reset: "重置", rotate: "轮换", order: "排序", import: "导入", export: "导出", locate: "定位", pause: "暂停", resume: "恢复", enable: "启用", disable: "停用" };
function auditLabel(action) {
  const m = /^(GET|POST|PUT|PATCH|DELETE) \/api\/v1\/admin\/(.+)$/.exec(action || "");
  if (!m) return actionName(action);
  const parts = m[2].split("/").filter((p) => !p.startsWith("{"));
  const names = parts.filter((p) => RESOURCES[p]).map((p) => RESOURCES[p]);
  const last = parts[parts.length - 1];
  const verb = VERBS[last] || { POST: "新建", PUT: "修改", PATCH: "修改", DELETE: "删除" }[m[1]] || "查看";
  return `${verb} ${names.join(" · ") || parts.join("/")}`;
}
const fullTime = (unix) => new Date(unix * 1000).toLocaleString("zh-CN", { hour12: false });
const loadTone = (v) => (v >= 90 ? "bad" : v >= 75 ? "warn" : "");

function kpi(label, value, sub = "", tone = "", page = "") {
  const tag = page ? "button" : "div";
  const attrs = page ? ` type="button" data-go="${page}"` : "";
  return `<${tag} class="ov-kpi ${tone}"${attrs}><span>${label}</span><strong>${value}</strong><small>${sub}</small></${tag}>`;
}

function kpis(nodes, firing) {
  const online = nodes.filter((n) => n.online);
  const live = online.filter((n) => n.metrics && !n.metrics_stale);
  const rateIn = live.reduce((s, n) => s + (n.metrics.rate_in || 0), 0);
  const rateOut = live.reduce((s, n) => s + (n.metrics.rate_out || 0), 0);
  const traffic = nodes.reduce((s, n) => s + (n.traffic_used || 0), 0);
  const cpu = live.length ? live.reduce((s, n) => s + (n.metrics.cpu || 0), 0) / live.length : null;
  const costs = nodes.map((n) => monthlyCostIn(n, "CNY")).filter((v) => v != null);
  const cost = costs.reduce((s, v) => s + v, 0);
  const values = nodes.map((n) => remainingValueIn(n, "CNY")).filter((v) => v != null);
  const value = values.reduce((s, v) => s + v, 0);
  const offline = nodes.filter((n) => !n.online && n.last_seen).length;
  return [
    kpi("在线节点", `${online.length}<small> / ${nodes.length}</small>`, offline ? `${offline} 台离线` : nodes.length ? "全部在线" : "还没有节点", offline ? "bad" : nodes.length ? "good" : "", "nodes"),
    kpi("实时上行", `${bytes(rateOut)}<small>/s</small>`, `${live.length} 台在报指标`),
    kpi("实时下行", `${bytes(rateIn)}<small>/s</small>`, `${live.length} 台在报指标`),
    kpi("本周期流量", bytes(traffic), "各节点按自己的账单周期"),
    kpi("平均 CPU", cpu == null ? "—" : `${cpu.toFixed(1)}<small>%</small>`, "在线节点平均", cpu == null ? "" : loadTone(cpu)),
    kpi("每月成本", costs.length ? `¥${cost.toFixed(0)}` : "—", costs.length ? `${costs.length} 台已计价 · 参考汇率` : "没有填价格"),
    kpi("剩余价值", values.length ? `¥${value.toFixed(0)}` : "—", values.length ? `${values.length} 台已估算 · 参考汇率` : "没有填价格和到期"),
    kpi("正在告警", firing == null ? "—" : String(firing.length), firing == null ? "读取失败" : firing.length ? "点开看详情" : "没有", firing?.length ? "bad" : firing ? "good" : "", "alerts"),
  ].join("");
}

// 各卡片里需要处理的条目汇成一张表：卡片自己是警告 / 严重但没有条目时，用卡片的结论当一条。
function attention(cards) {
  const rows = [];
  for (const c of cards) {
    const items = (c.items || []).filter((i) => i.tone === "bad" || i.tone === "warn");
    for (const i of items) rows.push({ tone: i.tone, label: i.label, note: i.note, from: c.title, page: c.page });
    if (!items.length && (c.tone === "bad" || c.tone === "warn")) rows.push({ tone: c.tone, label: c.value, note: c.note, from: c.title, page: c.page });
  }
  rows.sort((a, b) => (a.tone === b.tone ? 0 : a.tone === "bad" ? -1 : 1));
  const bad = rows.filter((r) => r.tone === "bad").length;
  const list = rows.slice(0, 12).map((r) => {
    const tag = r.page ? "button" : "div";
    return `<${tag} class="ov-todo ${r.tone}" title="${esc(`${r.label}：${r.note || ""}`)}"${r.page ? ` type="button" data-go="${esc(r.page)}"` : ""}><i aria-hidden="true"></i><span><b>${esc(r.label)}</b><small>${esc(r.note || "")}</small></span><em>${esc(r.from)}</em></${tag}>`;
  }).join("");
  const more = rows.length > 12 ? `<p class="ov-more">还有 ${rows.length - 12} 条，见各模块卡片。</p>` : "";
  const head = rows.length ? `<span class="pill ${bad ? "bad" : "warn"}">${rows.length} 项${bad ? ` · ${bad} 项严重` : ""}</span>` : `<span class="pill good">全部正常</span>`;
  return `<section data-part="panel" class="panel ov-panel"><div class="panel-head"><h3>需要处理</h3>${head}</div>${rows.length ? `<div class="ov-todos">${list}</div>${more}` : '<p class="ov-empty">没有需要处理的事项。节点在线、证书和备份正常，也没有快到期的机器。</p>'}</section>`;
}

function bar(label, value) {
  if (value == null) return `<span class="ov-bar"><em>${label}</em><b>—</b></span>`;
  return `<span class="ov-bar ${loadTone(value)}" title="${label} ${value.toFixed(1)}%"><em>${label}</em><i><s data-w="${Math.min(100, value).toFixed(1)}"></s></i><b>${value.toFixed(0)}%</b></span>`;
}

function loadPanel(nodes) {
  const live = nodes
    .filter((n) => n.online && n.metrics && !n.metrics_stale)
    .map((n) => {
      const m = n.metrics;
      const cpu = m.cpu ?? null;
      const mem = m.memory_total ? percent(m.memory_used, m.memory_total) : null;
      const disk = m.disk_total ? percent(m.disk_used, m.disk_total) : null;
      return { n, cpu, mem, disk, peak: Math.max(cpu || 0, mem || 0, disk || 0) };
    })
    .sort((a, b) => b.peak - a.peak);
  const rows = live.slice(0, 8).map(({ n, cpu, mem, disk }) => `<button type="button" class="ov-load-row" data-go="nodes"><span class="ov-node"><b>${esc(n.name)}</b><small>↑ ${bytes(n.metrics.rate_out || 0)}/s · ↓ ${bytes(n.metrics.rate_in || 0)}/s</small></span>${bar("CPU", cpu)}${bar("内存", mem)}${bar("磁盘", disk)}</button>`).join("");
  const note = live.length > 8 ? `<p class="ov-more">按 CPU / 内存 / 磁盘中最高的一项排序，显示前 8 台，共 ${live.length} 台在报指标。</p>` : "";
  return `<section data-part="panel" class="panel ov-panel"><div class="panel-head"><h3>节点负载</h3><button type="button" class="quiet" data-go="nodes">全部节点 →</button></div>${rows ? `<div class="ov-loads">${rows}</div>${note}` : '<p class="ov-empty">没有在线节点在报指标。</p>'}</section>`;
}

function cardHTML(card) {
  const tag = card.page ? "button" : "div";
  const attrs = card.page ? ` type="button" data-go="${esc(card.page)}" aria-label="${esc(card.title)}：${esc(card.value)}，打开${esc(card.title)}"` : "";
  return `<${tag} class="overview-card ${toneClass[card.tone] || ""}" data-card="${esc(card.id)}"${attrs}><span class="overview-card-title">${esc(card.title)}</span><strong>${esc(card.value)}</strong><small>${esc(card.note || "")}</small></${tag}>`;
}

function eventsPanel(events) {
  const body = events == null
    ? '<p class="ov-empty">读取失败。</p>'
    : events.length
      ? `<ul class="ov-feed">${events.map((e) => `<li><time title="${esc(fullTime(e.at))}">${esc(ago(e.at))}</time><span><b>${esc(e.node_name || kindName[e.kind] || e.kind)}</b> ${esc(e.message)}</span>${e.active ? '<span class="pill bad">告警中</span>' : `<span class="pill">${esc(kindName[e.kind] || e.kind)}</span>`}</li>`).join("")}</ul>`
      : '<p class="ov-empty">还没有事件。</p>';
  return `<section data-part="panel" class="panel ov-panel"><div class="panel-head"><h3>最近事件</h3><button type="button" class="quiet" data-go="alerts" data-alert-view="events">全部事件 →</button></div>${body}</section>`;
}

function auditPanel(entries) {
  // 审计的 node_id 有时是路径里的其他 id（如网站 id），只有对得上节点时才显示节点名。
  const nodeName = (id) => state.nodes.find((n) => n.id === id)?.name || "";
  const body = entries == null
    ? '<p class="ov-empty">读取失败。</p>'
    : entries.length
      ? `<ul class="ov-feed">${entries.map((a) => `<li><time title="${esc(fullTime(a.at))}">${esc(ago(a.at))}</time><span><b title="${esc(a.action)}">${esc(auditLabel(a.action))}</b>${nodeName(a.node_id) ? ` · ${esc(nodeName(a.node_id))}` : ""}<small>${esc(a.actor || "")}${a.source ? ` · ${esc(a.source)}` : ""}</small></span>${a.status >= 400 ? `<span class="pill bad">${a.status}</span>` : ""}</li>`).join("")}</ul>`
      : '<p class="ov-empty">还没有操作记录。</p>';
  return `<section data-part="panel" class="panel ov-panel"><div class="panel-head"><h3>最近操作</h3><button type="button" class="quiet" data-go="audit">审计日志 →</button></div>${body}</section>`;
}

export async function overviewPage(nodesReady) {
  clearTimeout(poller);
  const soft = (p) => p.catch(() => null);
  const [, data, firing, events, audit] = await Promise.all([
    nodesReady,
    api("/admin/overview"),
    soft(api("/admin/events?active=1&limit=500")),
    soft(api("/admin/events?limit=8")),
    soft(api("/admin/audit?limit=8")),
  ]);
  const root = content();
  const time = new Date(data.generated_at * 1000).toLocaleTimeString("zh-CN", { hour12: false });
  root.innerHTML = `<section data-part="page-heading" class="page-heading"><div><div class="eyebrow">OVERVIEW</div><h1>总览</h1><p>整个系统的状态汇总。更新于 ${esc(time)}，停在本页时每 15 秒自动刷新。</p></div><div class="heading-actions"><button id="refresh-overview" class="quiet">↻ 刷新</button></div></section>
  <div class="ov-kpis" title="${esc(fxHint)}">${kpis(state.nodes, firing)}</div>
  <div class="ov-grid">${attention(data.cards)}${loadPanel(state.nodes)}</div>
  <h2 class="ov-section">各模块</h2>
  <div class="overview-cards">${data.cards.map(cardHTML).join("")}</div>
  <div class="ov-grid">${eventsPanel(events)}${auditPanel(audit)}</div>`;
  // 面板的 CSP 不许内联 style 属性，条的长度在渲染后经 CSSOM 设置。
  root.querySelectorAll("[data-w]").forEach((el) => (el.style.width = `${el.dataset.w}%`));
  root.querySelector("#refresh-overview").onclick = () => go("overview");
  root.querySelectorAll("[data-go]").forEach((b) => (b.onclick = () => go(b.dataset.go, b.dataset.alertView)));
  const tick = () => {
    if (state.tab !== "overview") return;
    if (document.hidden || document.querySelector("dialog[open]")) poller = setTimeout(tick, REFRESH_MS);
    else render().catch(() => (poller = setTimeout(tick, REFRESH_MS)));
  };
  poller = setTimeout(tick, REFRESH_MS);
}
