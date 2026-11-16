// 后台「时间线」：告警事件、操作记录和各模块写的条目（资产到期、定时任务失败……）按时间排成一条，
// 下面是后台定时任务的运行状态。数据来自 /admin/timeline 与 /admin/jobs。
import { api, send, esc, toast, guard } from "./api.js";
import { content, render, pageView, setPageView } from "./admin-state.js";
import { auditLabel } from "./admin-overview.js";

const SOURCES = [["", "全部"], ["event", "告警事件"], ["audit", "操作记录"], ["modules", "模块"], ["brief", "简报"]];
const SOURCE_NAME = { event: "告警", audit: "操作", assets: "资产", jobs: "定时任务", brief: "简报", intel: "情报", assistant: "助理", todo:"待办", calendar:"日历", finance:"财务", automation:"自动化", security:"安全" };
let source = "";

const time = (ts) => new Date(ts * 1000).toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false });
const day = (ts) => new Date(ts * 1000).toLocaleDateString("zh-CN", { month: "long", day: "numeric", weekday: "short" });
const ago = (ts) => {
  if (!ts) return "—";
  const s = Date.now() / 1000 - ts;
  if (s < 90) return "刚刚";
  if (s < 3600) return `${Math.round(s / 60)} 分钟前`;
  if (s < 86400) return `${Math.round(s / 3600)} 小时前`;
  return `${Math.round(s / 86400)} 天前`;
};
const every = (s) => (s % 86400 === 0 ? `每 ${s / 86400} 天` : s % 3600 === 0 ? `每 ${s / 3600} 小时` : `每 ${Math.round(s / 60)} 分钟`);

function entryHTML(e) {
  const title = e.source === "audit" ? auditLabel(e.title) : e.title;
  return `<li class="tl-entry ${esc(e.level)}"><time datetime="${new Date(e.at * 1000).toISOString()}">${time(e.at)}</time><i aria-hidden="true"></i><div><strong>${esc(title)}</strong><span class="tl-source">${esc(SOURCE_NAME[e.source] || e.source)}</span>${e.body ? `<small>${esc(e.body)}</small>` : ""}</div></li>`;
}

function entriesHTML(list) {
  let current = "";
  return list.map((e) => {
    const d = day(e.at);
    const head = d !== current ? `<li class="tl-day">${d}</li>` : "";
    current = d;
    return head + entryHTML(e);
  }).join("");
}

export async function timelinePage() {
  source = pageView("", SOURCES.map(([id]) => id));
  const q = source ? `&sources=${source}` : "";
  const [tl, jobs] = await Promise.all([api(`/admin/timeline?limit=80${q}`), api("/admin/jobs").catch(() => ({ jobs: [] }))]);
  const chips = SOURCES.map(([id, label]) => `<button type="button" class="chip ${id === source ? "active" : ""}" data-source="${id}">${label}</button>`).join("");
  const jobRows = jobs.jobs.map((j) => `<tr><td data-label="任务">${esc(j.title)}<small><code>${esc(j.name)}</code></small></td><td data-label="间隔">${every(j.every_seconds)}</td><td data-label="上次">${ago(j.last_end)}${j.runs ? `<small>共 ${j.runs} 次${j.failures ? `，失败 ${j.failures} 次` : ""}</small>` : ""}</td><td data-label="状态">${j.running ? '<span class="pill">运行中</span>' : j.last_error ? `<span class="pill bad">失败</span><small>${esc(j.last_error)}</small>` : j.runs ? '<span class="pill good">正常</span>' : '<span class="pill">还没运行</span>'}</td><td data-label="操作"><button data-run-job="${esc(j.name)}">立即运行</button></td></tr>`).join("");
  content().innerHTML = `<section data-part="page-heading" class="page-heading"><div><div class="eyebrow">TIMELINE</div><h1>时间线</h1><p>发生过的一切按时间排成一条：告警事件、谁做了什么操作，以及资产到期、定时任务失败这类模块记下的事。外部 agent 用 <code>timeline.list</code> 读同一份。</p></div></section>
  <section data-part="panel" class="panel"><div class="panel-head"><div class="chips">${chips}</div></div><div class="panel-body"><ol class="timeline">${entriesHTML(tl.entries) || '<li class="muted">还没有记录。</li>'}</ol>${tl.entries.length >= 80 ? '<button type="button" id="tl-more">加载更早的</button>' : ""}</div></section>
  <section data-part="panel" class="panel"><div class="panel-head"><h3>定时任务</h3></div><div class="table-scroll"><table class="stack"><thead><tr><th>任务</th><th>间隔</th><th>上次运行</th><th>状态</th><th>操作</th></tr></thead><tbody>${jobRows || '<tr><td colspan="5" class="empty-cell">没有定时任务。</td></tr>'}</tbody></table></div></section>`;
  content().querySelectorAll("[data-source]").forEach((b) => (b.onclick = () => { source = b.dataset.source; setPageView(source); render(); }));
  content().querySelectorAll("[data-run-job]").forEach((b) => (b.onclick = guard(async () => { await send(`/admin/jobs/${encodeURIComponent(b.dataset.runJob)}/run`, "POST"); toast("已开始运行"); setTimeout(() => render(), 1500); })));
  let oldest = tl.entries.at(-1)?.at;
  const more = content().querySelector("#tl-more");
  if (more) more.onclick = guard(async () => {
    const next = await api(`/admin/timeline?limit=80&before=${oldest}${q}`);
    content().querySelector(".timeline").insertAdjacentHTML("beforeend", entriesHTML(next.entries));
    oldest = next.entries.at(-1)?.at || oldest;
    if (next.entries.length < 80) more.remove();
  });
}
