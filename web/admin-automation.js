import { api, send, esc, guard, modal, toast } from "./api.js";
import { content, render, confirmAction, state, pageView, setPageView } from "./admin-state.js";

let generation = 0;
const statusNames = { pending: "待确认", running: "执行中 / 中断待核实", succeeded: "成功", failed: "失败", cancelled: "已拒绝", interrupted: "中断待核实" };
const stamp = (seconds) => seconds ? new Date(seconds * 1000).toLocaleString("zh-CN", { hour12: false }) : "尚未触发";
const list = (value) => String(value || "").split(",").map((item) => item.trim()).filter(Boolean);
function tokenDialog(token) {
  const url = location.origin + "/api/v1/hooks/" + token;
  const dialog = modal("保存 Webhook 地址", `<p>地址只显示这一次，请妥善保存。持有地址的人可以触发这条规则。</p><label>POST 地址<textarea rows="4" readonly>${esc(url)}</textarea></label><div class="form-footer"><button data-close>已保存</button></div>`);
  dialog.querySelector("[data-close]").onclick = () => dialog.close();
}
function ruleForm(existing, actions) {
  const rule = existing || { name: "", enabled: false, trigger: "timeline", cron: "0 9 * * *", timezone: "UTC", sources: [], kinds: [], levels: [], conditions: [], action: "todo.create", cooldown: 60 };
  const dialog = modal(existing ? "编辑自动化规则" : "新建自动化规则", `<form id="automation-rule-form" method="dialog">
    <label>规则名称<input name="name" required maxlength="160" value="${esc(rule.name)}"></label>
    <div class="form-grid"><label>触发方式<select name="trigger">${[["timeline","时间线新条目"],["cron","定时（cron）"],["webhook","Webhook"]].map(([value,label])=>`<option value="${value}" ${rule.trigger===value?"selected":""}>${label}</option>`).join("")}</select></label><label>最小间隔（秒）<input name="cooldown" type="number" min="60" max="604800" required value="${rule.cooldown}"></label></div>
    <div class="form-grid"><label>cron（分 时 日 月 周）<input name="cron" value="${esc(rule.cron)}" placeholder="0 9 * * *"></label><label>时区<input name="timezone" value="${esc(rule.timezone)}" required></label></div>
    <div class="form-grid"><label>时间线来源<input name="sources" value="${esc(rule.sources?.join(", "))}" placeholder="event, intel"></label><label>类型 / 级别<input name="kinds" value="${esc(rule.kinds?.join(", "))}" placeholder="类型：逗号分隔"><input name="levels" value="${esc(rule.levels?.join(", "))}" placeholder="info, good, warn, bad"></label></div>
    <label>条件（JSON 数组）<textarea name="conditions" rows="3">${esc(JSON.stringify(rule.conditions || [], null, 2))}</textarea></label>
    <p class="hint">例如 [{"field":"kind","op":"eq","value":"offline"}]。支持 eq / ne / contains / gt / gte / lt / lte，Webhook 内容位于 data。</p>
    <label>调用能力<select name="action" required>${actions.filter((item)=>item.module!=="automation").map((item)=>`<option value="${esc(item.name)}" ${item.name===rule.action?"selected":""}>${esc(item.title)} · ${esc(item.name)} · ${esc(item.risk)}</option>`).join("")}</select></label>
    <label>能力参数（JSON 对象）<textarea name="args" rows="6" ${existing?"":"required"} placeholder="${existing?"参数已加密保存，留空保留原值":'{"body":{"title":"处理 {{title}}"}}'}"></textarea></label>
    <p class="hint">只替换 JSON 的值，支持 {{title}}、{{ref}}、{{data.field}}。Danger 能力始终等待人工确认。cron 只处理当前分钟，不补跑错过的时间。</p>
    <label class="checkbox"><input name="enabled" type="checkbox" ${rule.enabled?"checked":""}>启用规则</label>
    <div class="form-footer"><button type="button" data-cancel>取消</button><button type="submit" class="primary">保存规则</button></div></form>`);
  dialog.querySelector("[data-cancel]").onclick = () => dialog.close();
  const form = dialog.querySelector("form");
  form.onsubmit = guard(async (event) => {
    event.preventDefault();
    const fields = new FormData(form), conditions = JSON.parse(fields.get("conditions") || "[]");
    if (!Array.isArray(conditions)) throw new Error("条件必须为 JSON 数组");
    const body = { name: fields.get("name"), enabled: fields.has("enabled"), trigger: fields.get("trigger"), cron: fields.get("cron"), timezone: fields.get("timezone"), sources: list(fields.get("sources")), kinds: list(fields.get("kinds")), levels: list(fields.get("levels")), conditions, action: fields.get("action"), cooldown: Number(fields.get("cooldown")) };
    const args = fields.get("args").trim(); if (args) body.set_args = JSON.parse(args);
    const button = form.querySelector('[type="submit"]'); button.disabled = true;
    try {
      const saved = await send("/admin/automation/rules" + (existing ? "/" + rule.id : ""), existing ? "PUT" : "POST", body);
      dialog.close(); toast("规则已保存"); await render();
      if (saved.webhook_token) tokenDialog(saved.webhook_token);
    } finally { button.disabled = false; }
  });
}
async function runDetails(run) {
  const details = await send(`/admin/automation/runs/${run.id}/details`, "POST");
  const dialog = modal("执行详情 · " + run.rule_name, `<p>${esc(run.action)} · ${esc(statusNames[run.status] || run.status)}</p><p class="hint">请核对下面的冻结参数。结果可能包含秘密，请留意屏幕隐私。</p><pre class="automation-json">${esc(JSON.stringify(details.args, null, 2))}</pre>${details.result ? `<pre class="automation-json">${esc(JSON.stringify(details.result, null, 2))}</pre>` : ""}${run.status === "pending" ? '<div class="form-footer"><button data-reject>拒绝</button><button class="danger" data-approve>确认执行上述参数</button></div>' : ""}`);
  for (const [selector, approve] of [["[data-reject]",false],["[data-approve]",true]]) {
    const button = dialog.querySelector(selector);
    if (button) button.onclick = guard(async () => {
      dialog.querySelectorAll("button").forEach((item) => { item.disabled = true; });
      try { await send(`/admin/automation/runs/${run.id}/confirm`, "POST", { approve }); dialog.close(); toast(approve ? "确认已处理" : "已拒绝"); await render(); }
      finally { dialog.querySelectorAll("button").forEach((item) => { item.disabled = false; }); }
    });
  }
}
export async function automationPage() {
  const request = ++generation, root = content();
  const [rulesData, runsData, actionsData] = await Promise.all([api("/admin/automation/rules"), api("/admin/automation/runs"), api("/actions")]);
  if (request !== generation || state.tab !== "automation" || !root.isConnected) return;
  const view = pageView("rules", ["rules", "pending", "runs"]);
  const pending = runsData.runs.filter((run) => run.status === "pending");
  const rules = rulesData.rules, runs = view === "pending" ? pending : runsData.runs;
  root.innerHTML = `<section class="page-heading"><div><h1>自动化</h1><p>让重复工作按规则执行，危险操作留给你确认。</p></div><div class="heading-actions"><button id="automation-refresh">刷新记录</button><button class="primary" id="automation-add">新建规则</button></div></section>
    <section class="panel"><div class="panel-head"><h2>规则 <span class="muted">${rules.length}</span></h2></div>${rules.length ? `<div class="table-scroll"><table><thead><tr><th>规则</th><th>触发与能力</th><th>最近触发</th><th>操作</th></tr></thead><tbody>${rules.map((rule)=>`<tr><td><strong>${esc(rule.name)}</strong><div class="hint">${rule.enabled?"已启用":"已停用"} · 最小间隔 ${rule.cooldown} 秒</div></td><td>${esc(rule.trigger)}<div class="hint">${esc(rule.action)}</div></td><td>${stamp(rule.last_attempt)}</td><td><div class="row-actions"><button data-edit="${esc(rule.id)}">编辑</button>${rule.trigger==="webhook"?`<button data-rotate="${esc(rule.id)}">轮换链接</button>`:""}<button class="danger" data-delete="${esc(rule.id)}">删除</button></div></td></tr>`).join("")}</tbody></table></div>` : '<div class="empty">还没有规则。先创建一条，保存后再启用。</div>'}</section>
    <section class="panel"><div class="panel-head"><h2>执行记录</h2><span class="hint">最多显示最近 200 次</span></div>${runs.length ? `<div class="table-scroll"><table><thead><tr><th>规则 / 时间</th><th>动作</th><th>状态</th><th>详情</th></tr></thead><tbody>${runs.map((run)=>`<tr><td>${esc(run.rule_name)}<div class="hint">${stamp(run.at)}</div></td><td>${esc(run.action)}<div class="hint">${esc(run.trigger_key)}</div></td><td><span class="${run.status==="pending"||run.status==="failed"?"warn-text":""}">${esc(statusNames[run.status] || run.status)}</span></td><td><button data-run="${esc(run.id)}">${run.status==="pending"?"核对并确认":"查看"}</button></td></tr>`).join("")}</tbody></table></div>` : '<div class="empty">规则触发后，这里会保留执行状态。</div>'}</section>`;
  const [rulesPanel, runsPanel] = root.querySelectorAll("section.panel");
  if (view === "rules") runsPanel.remove();
  else rulesPanel.remove();
  if (view === "pending") {
    runsPanel.querySelector("h2").textContent = "待确认操作";
    runsPanel.querySelector(".hint").textContent = "最近 200 次执行中的待确认项";
    const empty = runsPanel.querySelector(".empty");
    if (empty) empty.textContent = "没有待确认的操作。";
  }
  root.querySelector(".page-heading").insertAdjacentHTML("afterend", `<div class="segmented settings-tabs" role="group" aria-label="自动化内容">${[["rules", "规则"], ["pending", `待确认 (${pending.length})`], ["runs", "执行记录"]].map(([id, label]) => `<button type="button" data-automation-view="${id}" class="${view === id ? "active" : ""}" aria-pressed="${view === id}">${esc(label)}</button>`).join("")}</div>`);
  root.querySelectorAll("[data-automation-view]").forEach((button) => {
    button.onclick = guard(async () => { setPageView(button.dataset.automationView); await render(); });
  });
  root.querySelector("#automation-add").onclick = () => ruleForm(null, actionsData.actions);
  root.querySelector("#automation-refresh").onclick = guard(render);
  root.querySelectorAll("[data-edit]").forEach((button)=>{button.onclick=()=>ruleForm(rules.find((rule)=>rule.id===button.dataset.edit),actionsData.actions);});
  root.querySelectorAll("[data-run]").forEach((button)=>{button.onclick=guard(()=>runDetails(runs.find((run)=>run.id===button.dataset.run)));});
  root.querySelectorAll("[data-delete]").forEach((button)=>{button.onclick=()=>confirmAction("删除规则","删除后将停止触发，并取消待确认项。",async()=>{await send(`/admin/automation/rules/${button.dataset.delete}`,"DELETE");await render();});});
  root.querySelectorAll("[data-rotate]").forEach((button)=>{button.onclick=()=>confirmAction("轮换 Webhook 地址","原地址将立即失效。",async()=>{const result=await send(`/admin/automation/rules/${button.dataset.rotate}/rotate`,"POST");await render();tokenDialog(result.webhook_token);});});
}
