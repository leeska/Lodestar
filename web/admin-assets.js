// 后台「资产」：服务器之外自己拥有或在付费的东西（域名、订阅服务、设备、证件、账户、保险……）。
// 每项有费用、周期、到期日和提醒；敏感字段（密码、证件号、恢复码）存在保险箱里，只在点「查看」时取出，
// 每次查看都记进审计日志。到期提醒由定时任务「资产到期提醒」经告警渠道发送。
import { api, send, esc, toast, modal, guard } from "./api.js";
import { monthlyCostIn, fxHint } from "./format.js";
import { wireTodoRefs } from "./admin-todo.js";
import { content, confirmAction, moreMenu, wireMoreMenus, render, go } from "./admin-state.js";

let filter = { kind: "", q: "", archived: false };

const kpi = (label, value, sub = "", tone = "") => `<div class="ov-kpi ${tone}"><span>${label}</span><strong>${value}</strong><small>${sub}</small></div>`;
const monthly = (a) => monthlyCostIn({ billing: { price: a.cost, currency: a.currency, price_period: a.period } }, "CNY");
const money = (v, currency) => (v ? `${esc(currency)} ${Number(v).toLocaleString("zh-CN", { maximumFractionDigits: 2 })}` : "");

function leftPill(days) {
  if (days == null) return '<span class="muted">不过期</span>';
  if (days < 0) return `<span class="pill bad">已过期 ${-days} 天</span>`;
  if (days === 0) return '<span class="pill bad">今天到期</span>';
  return `<span class="pill ${days <= 7 ? "bad" : days <= 30 ? "warn" : ""}">${days} 天</span>`;
}

export async function assetsPage() {
  const params = new URLSearchParams({ kind: filter.kind, q: filter.q, archived: filter.archived ? "1" : "" });
  const [data, upcoming] = await Promise.all([api(`/admin/assets?${params}`), api("/admin/assets/upcoming?days=60")]);
  const kinds = Object.fromEntries(data.kinds.map((k) => [k.id, k]));
  const active = data.assets.filter((a) => a.status === "active");
  const costs = active.map(monthly).filter((v) => v != null);
  const soon = active.filter((a) => a.days_left != null && a.days_left >= 0 && a.days_left <= 30).length;
  const expired = active.filter((a) => a.days_left != null && a.days_left < 0).length;
  const rows = data.assets.map((a) => {
    const k = kinds[a.kind] || { name: a.kind, fields: [] };
    const tags = a.tags.map((t) => `<span class="tag">${esc(t)}</span>`).join("");
    const secretCount = Object.keys(a.secrets || {}).length;
    const detail = k.fields.filter((f) => !f.secret && a.fields[f.key]).slice(0, 2).map((f) => `${esc(f.label)} ${esc(a.fields[f.key])}`).join(" · ");
    const more = `<button data-todo-ref="asset:${esc(a.id)}">转成待办</button>${secretCount ? `<button data-reveal="${esc(a.id)}">查看敏感字段</button>` : ""}<button data-archive="${esc(a.id)}">${a.status === "active" ? "归档" : "恢复"}</button><button class="menu-danger" data-delete="${esc(a.id)}">删除</button>`;
    return `<tr class="${a.status === "archived" ? "muted-row" : ""}"><td data-label="名称"><strong>${esc(a.name)}</strong>${tags}<small>${detail}${secretCount ? `${detail ? " · " : ""}🔒 ${secretCount} 项敏感字段` : ""}</small></td><td data-label="类型">${esc(k.name)}</td><td data-label="费用">${a.cost ? `${money(a.cost, a.currency)}<small>${esc(a.period || "")}</small>` : '<span class="muted">—</span>'}</td><td data-label="到期">${a.expires_on ? `${esc(a.expires_on)}<small>${a.auto_renew ? "自动续费" : "手动续费"}</small>` : ""}</td><td data-label="剩余">${leftPill(a.days_left)}</td><td data-label="操作"><div class="row-actions"><button data-edit="${esc(a.id)}">编辑</button>${moreMenu(more)}</div></td></tr>`;
  }).join("");
  const upcomingRows = upcoming.items.map((u) => `<li><span>${leftPill(u.days_left)}</span><strong>${esc(u.name)}</strong><small>${esc(u.kind)} · ${esc(u.expires_on)}${u.cost ? ` · ${money(u.cost, u.currency)}${u.period ? " / " + esc(u.period) : ""}` : ""}</small>${u.source === "node" ? '<button type="button" data-go="nodes">节点</button>' : `<button type="button" data-edit="${esc(u.id)}">续期</button>`}</li>`).join("");
  const kindOptions = `<option value="">全部类型</option>${data.kinds.map((k) => `<option value="${esc(k.id)}" ${filter.kind === k.id ? "selected" : ""}>${esc(k.name)}</option>`).join("")}`;
  content().innerHTML = `<section data-part="page-heading" class="page-heading"><div><div class="eyebrow">ASSETS</div><h1>资产<span>${active.length}</span></h1><p>服务器之外自己拥有或在付费的东西：域名、订阅服务、设备、证件、账户、保险。到期前会经告警渠道提醒；密码、证件号这类敏感字段加密存在保险箱里。</p></div><div class="heading-actions"><button id="add-asset" class="primary">＋ 登记资产</button></div></section>
  <section class="ov-kpis assets-kpis">${kpi("资产", String(active.length), filter.kind || filter.q ? "当前筛选" : "未归档")}${kpi("每月合计", costs.length ? `¥${costs.reduce((s, v) => s + v, 0).toFixed(0)}` : "—", costs.length ? `${costs.length} 项已计价 · 参考汇率` : "没有填费用和周期")}${kpi("30 天内到期", String(soon), soon ? "记得续费" : "没有", soon ? "warn" : "good")}${kpi("已过期", String(expired), expired ? "续了就改到期日" : "没有", expired ? "bad" : "good")}</section>
  <section data-part="panel" class="panel"><div class="panel-head"><h3>60 天内到期 <span class="muted">${upcoming.items.length}</span></h3><small class="muted">含面板里的服务器</small></div><div class="panel-body">${upcomingRows ? `<ul class="asset-upcoming">${upcomingRows}</ul>` : '<p class="muted">60 天内没有到期的。</p>'}</div></section>
  <section data-part="panel" class="panel"><div class="panel-head asset-filters"><select id="asset-kind" aria-label="类型">${kindOptions}</select><input type="search" id="asset-q" placeholder="搜索名称、备注、字段" value="${esc(filter.q)}" aria-label="搜索"><label class="checkbox"><input type="checkbox" id="asset-archived" ${filter.archived ? "checked" : ""}>含已归档</label></div><div class="table-scroll"><table class="stack"><thead><tr><th>名称</th><th>类型</th><th>费用</th><th>到期</th><th>剩余</th><th>操作</th></tr></thead><tbody>${rows || '<tr><td colspan="6" class="empty-cell">还没有资产。点「登记资产」把域名、订阅服务这些记进来。</td></tr>'}</tbody></table></div><p class="hint panel-foot">${esc(fxHint)}</p></section>`;
  const byId = (id) => data.assets.find((a) => a.id === id);
  wireMoreMenus(content());
  content().querySelectorAll(".asset-upcoming > li").forEach((row,index) => {
    const asset=upcoming.items[index];
    if (asset.source !== "asset") return;
    const button=document.createElement("button");
    button.type="button"; button.textContent="转成待办"; button.dataset.todoRef="asset:"+asset.id;
    const actions=document.createElement("div");
    actions.className="row-actions asset-upcoming-actions";
    const renew=row.querySelector("button"); if (renew) actions.append(renew);
    actions.append(button); row.append(actions);
  });
  wireTodoRefs(content());
  content().querySelector("#add-asset").onclick = () => assetForm(data, null);
  content().querySelectorAll("[data-edit]").forEach((b) => (b.onclick = guard(async () => assetForm(data, byId(b.dataset.edit) || (await api(`/admin/assets/${b.dataset.edit}`))))));
  content().querySelectorAll("[data-go]").forEach((b) => (b.onclick = () => go(b.dataset.go)));
  content().querySelectorAll("[data-reveal]").forEach((b) => (b.onclick = () => revealDialog(byId(b.dataset.reveal), kinds)));
  content().querySelectorAll("[data-archive]").forEach((b) => (b.onclick = guard(async () => {
    const a = byId(b.dataset.archive);
    await send(`/admin/assets/${a.id}`, "PUT", { ...a, status: a.status === "active" ? "archived" : "active", secrets: undefined, days_left: undefined });
    await render();
  })));
  content().querySelectorAll("[data-delete]").forEach((b) => (b.onclick = () => {
    const a = byId(b.dataset.delete);
    confirmAction(`删除资产 · ${a.name}`, `删除「${a.name}」和它的敏感字段，不能恢复。只是不用了可以「归档」。`, async () => { await send(`/admin/assets/${a.id}`, "DELETE"); await render(); }, { label: "删除" });
  }));
  content().querySelector("#asset-kind").onchange = (e) => { filter.kind = e.target.value; render(); };
  content().querySelector("#asset-archived").onchange = (e) => { filter.archived = e.target.checked; render(); };
  let timer;
  content().querySelector("#asset-q").oninput = (e) => { clearTimeout(timer); timer = setTimeout(() => { filter.q = e.target.value.trim(); render().then(() => { const i = content().querySelector("#asset-q"); i?.focus(); i?.setSelectionRange(i.value.length, i.value.length); }); }, 400); };
}

function fieldsHTML(kind, a) {
  if (!kind.fields.length) return '<p class="muted">这种类型没有专门的字段，写在备注里即可。</p>';
  return `<div class="form-grid">${kind.fields.map((f) => {
    if (f.secret) {
      const stored = a?.secrets?.[f.key];
      return `<label>${esc(f.label)} 🔒<input name="secret:${esc(f.key)}" type="password" autocomplete="new-password" placeholder="${stored ? "已保存，留空不改" : "不填就不存"}">${stored ? `<span class="checkbox"><input type="checkbox" name="clear:${esc(f.key)}">清除已保存的</span>` : ""}</label>`;
    }
    const type = f.type === "date" ? "date" : f.type === "url" ? "url" : f.type === "email" ? "email" : "text";
    return `<label>${esc(f.label)}<input name="field:${esc(f.key)}" type="${type}" value="${esc(a?.fields?.[f.key] || "")}"></label>`;
  }).join("")}</div>`;
}

function assetForm(data, existing) {
  const a = existing || { kind: data.kinds[0].id, name: "", tags: [], fields: {}, cost: 0, currency: "CNY", period: "年", expires_on: "", auto_renew: false, remind_days: 30, note: "", status: "active" };
  const kindSelect = data.kinds.map((k) => `<option value="${esc(k.id)}" ${k.id === a.kind ? "selected" : ""}>${esc(k.name)}</option>`).join("");
  const currencies = data.currencies.map((c) => `<option ${c === a.currency ? "selected" : ""}>${esc(c)}</option>`).join("");
  const periods = `<option value="">—</option>${data.periods.map((p) => `<option ${p === a.period ? "selected" : ""}>${esc(p)}</option>`).join("")}`;
  const d = modal(existing ? `编辑资产 · ${existing.name}` : "登记资产", `<form id="asset-form">
    <section class="form-section"><h3>基本</h3><div class="form-grid"><label>类型<select name="kind">${kindSelect}</select></label><label>名称<input name="name" required maxlength="80" value="${esc(a.name)}"></label><label>标签（逗号分隔）<input name="tags" value="${esc(a.tags.join("，"))}"></label></div></section>
    <section class="form-section"><h3>详情</h3><div id="kind-fields">${fieldsHTML(data.kinds.find((k) => k.id === a.kind) || data.kinds[0], a)}</div></section>
    <section class="form-section"><h3>费用与到期</h3><div class="form-grid"><label>费用<input name="cost" type="number" min="0" step="0.01" value="${a.cost || ""}"></label><label>币种<select name="currency">${currencies}</select></label><label>周期<select name="period">${periods}</select></label><label>到期日<input name="expires_on" type="date" value="${esc(a.expires_on)}"></label><label>提前几天提醒<input name="remind_days" type="number" min="0" max="365" value="${a.remind_days || 30}"></label></div><label class="checkbox"><input type="checkbox" name="auto_renew" ${a.auto_renew ? "checked" : ""}>自动续费（照样提醒，方便留意扣款）</label></section>
    <section class="form-section"><h3>备注</h3><label><textarea name="note" rows="3" maxlength="2000">${esc(a.note)}</textarea></label></section>
    <div class="form-footer"><button type="submit" class="primary">保存</button></div></form>`);
  const form = d.querySelector("#asset-form");
  form.elements.kind.onchange = (e) => {
    const kind = data.kinds.find((k) => k.id === e.target.value);
    form.querySelector("#kind-fields").innerHTML = fieldsHTML(kind, e.target.value === a.kind ? a : null);
  };
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const el = form.elements;
    const fields = {}, secrets = {};
    for (const input of form.querySelectorAll("[name^='field:']")) if (input.value.trim()) fields[input.name.slice(6)] = input.value.trim();
    for (const input of form.querySelectorAll("[name^='secret:']")) if (input.value) secrets[input.name.slice(7)] = input.value;
    for (const input of form.querySelectorAll("[name^='clear:']")) if (input.checked) secrets[input.name.slice(6)] = "";
    const body = {
      kind: el.kind.value, name: el.name.value.trim(), tags: el.tags.value.split(/[,，]/).map((t) => t.trim()).filter(Boolean),
      fields, set_secrets: secrets, cost: Number(el.cost.value || 0), currency: el.currency.value, period: el.period.value,
      expires_on: el.expires_on.value, auto_renew: el.auto_renew.checked, remind_days: Number(el.remind_days.value || 0), note: el.note.value, status: a.status,
    };
    await send(existing ? `/admin/assets/${existing.id}` : "/admin/assets", existing ? "PUT" : "POST", body);
    d.close();
    toast("已保存");
    await render();
  });
}

function revealDialog(a, kinds) {
  const k = kinds[a.kind];
  const stored = (k?.fields || []).filter((f) => f.secret && a.secrets?.[f.key]);
  const d = modal(`敏感字段 · ${a.name}`, `<p class="hint">每次查看都会记进审计日志。</p><ul class="asset-secrets">${stored.map((f) => `<li><span>${esc(f.label)}</span><code data-value="${esc(f.key)}">••••••••</code><button type="button" data-show="${esc(f.key)}">查看</button></li>`).join("")}</ul>`);
  d.querySelectorAll("[data-show]").forEach((b) => (b.onclick = guard(async () => {
    const out = await send(`/admin/assets/${a.id}/reveal`, "POST", { field: b.dataset.show });
    d.querySelector(`[data-value="${CSS.escape(b.dataset.show)}"]`).textContent = out.value;
    b.textContent = "复制";
    b.onclick = () => navigator.clipboard?.writeText(out.value).then(() => toast("已复制"), () => toast("复制失败，请手动选择"));
  })));
}
