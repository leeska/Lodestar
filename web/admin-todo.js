import { api, send, esc, guard, modal, toast } from "./api.js";
import { content, render, confirmAction, state, pageView, setPageView } from "./admin-state.js";

let view = "open";
let generation = 0;
const stamp = (seconds) => seconds ? new Date(seconds * 1000).toLocaleString("zh-CN", { hour12: false }) : "未设置";
const localInput = (seconds) => {
  if (!seconds) return "";
  const date = new Date(seconds * 1000);
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
};

export async function fromRef(ref) {
  const item = await send("/admin/todo/from-ref", "POST", { ref });
  toast("已加入待办");
  todoForm(item);
}

export function wireTodoRefs(root) {
  root.querySelectorAll("[data-todo-ref]").forEach((button) => {
    button.onclick = guard(async () => { button.disabled = true; try { await fromRef(button.dataset.todoRef); } finally { button.disabled = false; } });
  });
}

export function todoForm(existing = null) {
  const item = existing || { title: "", note: "", priority: 1, due_at: 0, ref: "", source: "manual" };
  const dialog = modal(existing ? "编辑待办" : "新建待办", `<form id="todo-form" method="dialog">
    <label>标题<input name="title" required maxlength="160" value="${esc(item.title)}"></label>
    <div class="form-grid"><label>截止时间<input name="due" type="datetime-local" value="${esc(localInput(item.due_at))}"></label>
    <label>优先级<select name="priority">${[[0, "低"], [1, "普通"], [2, "高"], [3, "紧急"]].map(([value, label]) => `<option value="${value}" ${item.priority === value ? "selected" : ""}>${label}</option>`).join("")}</select></label></div>
    <p class="hint">时间按当前设备时区填写；截止前 24 小时、1 小时和到期时提醒。</p>
    <label>备注<textarea name="note" maxlength="4000" rows="4">${esc(item.note)}</textarea></label>
    ${item.ref ? `<p class="hint">来源：${esc(item.ref)}</p>` : ""}
    <div class="form-footer"><button type="button" data-cancel>取消</button><button type="submit" class="primary">保存待办</button></div>
  </form>`);
  dialog.querySelector("[data-cancel]").onclick = () => dialog.close();
  const form = dialog.querySelector("form");
  form.onsubmit = guard(async (event) => {
    event.preventDefault();
    const fields = new FormData(form);
    const due = fields.get("due");
    const body = { title: fields.get("title"), note: fields.get("note"), priority: Number(fields.get("priority")), due_at: due ? Math.floor(new Date(due).getTime() / 1000) : 0, ref: item.ref, source: item.source };
    const button = form.querySelector('[type="submit"]'); button.disabled = true;
    try {
      await send(`/admin/todo${existing ? "/" + item.id : ""}`, existing ? "PUT" : "POST", body);
      if (!existing && state.tab === "todo") { view = "open"; setPageView(view); }
      dialog.close(); toast("待办已保存"); await render();
    } finally { button.disabled = false; }
  });
}

function sourceForm(existing = null) {
  const source = existing || { name: "", timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "Asia/Shanghai", every_minutes: 30, enabled: true };
  const dialog = modal(existing ? "编辑日历订阅" : "订阅只读日历", `<form id="calendar-source-form" method="dialog">
    <label>名称<input name="name" required maxlength="80" value="${esc(source.name)}"></label>
    <label>私有 ics 地址<input name="url" type="password" autocomplete="new-password" ${existing ? "" : "required"} placeholder="${existing && source.has_url ? "已保存，留空保留" : "https://…/private.ics"}"></label>
    <p class="hint">只读同步，不修改远端日历。地址加密保存，列表不返回；webcal 地址请改用 https。</p>
    <div class="form-grid"><label>日历时区<input name="timezone" required value="${esc(source.timezone)}"></label><label>同步间隔（分钟）<input name="every" type="number" min="5" max="10080" required value="${source.every_minutes}"></label></div>
    <label class="checkbox"><input name="enabled" type="checkbox" ${source.enabled ? "checked" : ""}>启用同步</label>
    <div class="form-footer"><button type="button" data-cancel>取消</button><button type="submit" class="primary">保存订阅</button></div>
  </form>`);
  dialog.querySelector("[data-cancel]").onclick = () => dialog.close();
  const form = dialog.querySelector("form");
  form.onsubmit = guard(async (event) => {
    event.preventDefault();
    const fields = new FormData(form);
    const body = { name: fields.get("name"), timezone: fields.get("timezone"), enabled: fields.has("enabled"), every_minutes: Number(fields.get("every")) };
    if (fields.get("url")) body.set_url = fields.get("url");
    const button = form.querySelector('[type="submit"]'); button.disabled = true;
    try {
      await send(`/admin/calendar/sources${existing ? "/" + source.id : ""}`, existing ? "PUT" : "POST", body);
      view = "sources";
      setPageView(view);
      dialog.close(); toast("日历订阅已保存"); await render();
    } finally { button.disabled = false; }
  });
}

export async function todoPage() {
  view = pageView("open", ["open", "done", "calendar", "sources"]);
  const request = ++generation;
  const root = content();
  const [todos, calendars] = await Promise.all([api(`/admin/todo?state=${view === "done" ? "done" : "open"}`), api("/admin/calendar/sources")]);
  if (request !== generation || !root.isConnected || state.tab !== "todo") return;
  root.innerHTML = `<section class="page-heading"><div><h1>日程与待办</h1><p>把需要处理的事放在一起，今天的安排也会进入简报。</p></div><div class="heading-actions"><button id="calendar-add">订阅日历</button><button class="primary" id="todo-add">新建待办</button></div></section>
    <div class="segmented settings-tabs">${[["open", "未完成"], ["done", "已完成"], ["calendar", "日历"], ["sources", "日历订阅"]].map(([id, label]) => `<button data-todo-view="${id}" class="${view === id ? "active" : ""}">${label}</button>`).join("")}</div><div id="todo-content"></div>`;
  root.querySelector("#todo-add").onclick = () => todoForm();
  root.querySelector("#calendar-add").onclick = () => sourceForm();
  root.querySelectorAll("[data-todo-view]").forEach((button) => { button.onclick = () => { view = button.dataset.todoView; setPageView(view); render(); }; });
  const area = root.querySelector("#todo-content");
  if (view === "sources") {
    area.innerHTML = `<section class="panel"><div class="table-scroll"><table class="stack"><thead><tr><th>名称</th><th>时区</th><th>上次同步</th><th>状态</th><th>操作</th></tr></thead><tbody>${calendars.sources.map((source) => `<tr>
      <td data-label="名称">${esc(source.name)}</td><td data-label="时区">${esc(source.timezone)}</td><td data-label="上次同步">${source.last_fetch ? esc(stamp(source.last_fetch)) : "尚未同步"}</td>
      <td data-label="状态">${source.last_error ? `<span class="pill warn">${esc(source.last_error)}</span>` : source.enabled ? '<span class="pill good">启用</span>' : '<span class="pill">暂停</span>'}</td>
      <td data-label="操作"><div class="row-actions"><button data-sync="${esc(source.id)}">立即同步</button><button data-edit-calendar="${esc(source.id)}">编辑</button><button data-delete-calendar="${esc(source.id)}">删除</button></div></td></tr>`).join("") || '<tr><td colspan="5" class="empty-cell">还没有订阅日历。</td></tr>'}</tbody></table></div></section>`;
    area.querySelectorAll("[data-edit-calendar]").forEach((button) => { button.onclick = () => sourceForm(calendars.sources.find((source) => source.id === button.dataset.editCalendar)); });
    area.querySelectorAll("[data-sync]").forEach((button) => { button.onclick = guard(async () => {
      button.disabled = true;
      try { await send(`/admin/calendar/sources/${button.dataset.sync}/sync`, "POST"); toast("日历已同步"); await render(); } finally { button.disabled = false; }
    }); });
    area.querySelectorAll("[data-delete-calendar]").forEach((button) => { button.onclick = () => confirmAction("删除日历订阅", "删除本地同步内容和私有地址，不能恢复。", async () => { await send(`/admin/calendar/sources/${button.dataset.deleteCalendar}`, "DELETE"); await render(); }); });
  } else if (view === "calendar") {
    const start = new Date(); start.setHours(0, 0, 0, 0);
    const end = new Date(start); end.setDate(end.getDate() + 7);
    const result = await api(`/admin/calendar/range?from=${Math.floor(start.getTime() / 1000)}&to=${Math.floor(end.getTime() / 1000)}`);
    if (request !== generation || !root.isConnected || state.tab !== "todo") return;
    area.innerHTML = `<section class="panel"><div class="panel-head"><h3>未来七天</h3><span class="hint">按设备时区显示 · 只读同步</span></div><div class="panel-body"><ul class="agenda-list">${result.events.map((event) => `<li><div><strong>${esc(event.title)}</strong><small>${esc(event.all_day ? new Date(event.start_at * 1000).toLocaleDateString("zh-CN") + " 全天" : stamp(event.start_at) + " — " + stamp(event.end_at))}</small><small>${esc(event.source_name)}${event.location ? " · " + esc(event.location) : ""}</small>${event.body ? `<details><summary>备注</summary><p>${esc(event.body)}</p></details>` : ""}</div></li>`).join("") || '<li class="muted">未来七天没有日程。可先订阅 Apple 或 Google 的私有 ics 地址。</li>'}</ul></div></section>`;
  } else {
    area.innerHTML = `<section class="panel"><div class="panel-body"><ul class="agenda-list">${todos.items.map((item) => `<li data-todo="${esc(item.id)}"><div>
      <strong>${esc(item.title)}</strong> ${item.priority >= 2 ? `<span class="pill warn">${item.priority === 3 ? "紧急" : "高优先级"}</span>` : ""}
      <small class="${!item.done_at && item.due_at && item.due_at <= Date.now() / 1000 ? "warn-text" : ""}">截止：${esc(stamp(item.due_at))}${item.ref ? " · " + esc(item.ref) : ""}</small>
      ${item.note ? `<details><summary>备注</summary><p>${esc(item.note)}</p></details>` : ""}</div><div class="row-actions">
      <button data-complete="${esc(item.id)}">${item.done_at ? "重新打开" : "完成"}</button><button data-edit-todo="${esc(item.id)}">编辑</button><button data-delete-todo="${esc(item.id)}">删除</button></div></li>`).join("") || '<li class="muted">这里还没有待办。</li>'}</ul>${todos.items.length === 500 ? '<p class="hint">当前显示前 500 项。</p>' : ""}</div></section>`;
    area.querySelectorAll("[data-edit-todo]").forEach((button) => { button.onclick = () => todoForm(todos.items.find((item) => item.id === button.dataset.editTodo)); });
    area.querySelectorAll("[data-complete]").forEach((button) => { button.onclick = guard(async () => { const item = todos.items.find((item) => item.id === button.dataset.complete); await send(`/admin/todo/${item.id}/complete`, "POST", { done: !item.done_at }); await render(); }); });
    area.querySelectorAll("[data-delete-todo]").forEach((button) => { button.onclick = () => confirmAction("删除待办", "永久删除这项待办，不能恢复。", async () => { await send(`/admin/todo/${button.dataset.deleteTodo}`, "DELETE"); await render(); }); });
  }
}
