import { api, send, esc, guard, modal, toast } from "./api.js";
import { content, render, confirmAction, state, pageView, setPageView } from "./admin-state.js";
import { wireTodoRefs } from "./admin-todo.js";

let view = "items";
let generation = 0;
const filter = { source: "", q: "", unread: false, starred: false };
const ACTIONS = { notify: "立即通知", brief: "放进简报", ignore: "忽略" };
const stamp = (seconds) => seconds ? new Date(seconds * 1000).toLocaleString("zh-CN", { hour12: false }) : "尚未抓取";

function itemHTML(item) {
  let link = "";
  try { const parsed = new URL(item.url); if (["http:", "https:"].includes(parsed.protocol) && parsed.hostname) link = parsed.href; } catch {}
  return `<article class="intel-item" data-item="${item.id}">
    <div class="intel-item-meta"><span>${esc(item.source_name)}</span><time>${esc(stamp(item.at))}</time>${item.disposition === "ignore" ? '<span class="pill">已忽略</span>' : ""}</div>
    <h3>${link ? `<a href="${esc(link)}" target="_blank" rel="noopener noreferrer">${esc(item.title)}</a>` : esc(item.title)}</h3>
    ${item.body ? `<details><summary>查看内容</summary><p class="intel-body">${esc(item.body)}</p></details>` : ""}
    <div class="row-actions"><button type="button" data-read="${item.id}">${item.read ? "标为未读" : "标为已读"}</button><button type="button" data-star="${item.id}">${item.starred ? "取消星标" : "加星标"}</button><button type="button" data-todo-ref="intel:${item.id}">转成待办</button></div>
  </article>`;
}

export async function intelPage() {
  view = pageView("items", ["items", "sources", "rules"]);
  const request = ++generation;
  const root = content();
  const [data, ruleData] = await Promise.all([api("/admin/intel/sources"), view === "rules" ? api("/admin/intel/rules") : Promise.resolve({ rules: [] })]);
  if (request !== generation || !root.isConnected || state.tab !== "intel") return;
  const sourceName = (id) => data.sources.find((source) => source.id === id)?.name || id;
  const tabs = [["items", "条目"], ["sources", "来源"], ["rules", "规则"]].map(([id, label]) => `<button type="button" data-intel-view="${id}" class="${view === id ? "active" : ""}">${label}</button>`).join("");
  root.innerHTML = `<section data-part="page-heading" class="page-heading"><div><h1>情报</h1><p>收集关注的更新，重要的立即提醒，其余留在简报里。</p></div><div class="heading-actions"><button class="quiet" id="intel-refresh">刷新</button><button id="intel-add-rule">添加规则</button><button class="primary" id="intel-add-source">添加源</button></div></section><div class="segmented settings-tabs">${tabs}</div><div id="intel-content"></div>`;
  root.querySelectorAll("[data-intel-view]").forEach((button) => { button.onclick = () => { view = button.dataset.intelView; setPageView(view); render(); }; });
  root.querySelector("#intel-refresh").onclick = () => render();
  root.querySelector("#intel-add-source").onclick = () => sourceForm(data, null);
  root.querySelector("#intel-add-rule").onclick = () => ruleForm(data, null);
  const area = root.querySelector("#intel-content");
  if (view === "sources") {
    area.innerHTML = `<section class="panel"><div class="table-scroll"><table class="stack"><thead><tr><th>来源</th><th>连接器</th><th>间隔</th><th>最近抓取</th><th>状态</th><th>操作</th></tr></thead><tbody>${data.sources.map((source) => `<tr>
      <td data-label="来源"><strong>${esc(source.name)}</strong></td><td data-label="连接器">${esc(data.connectors.find((kind) => kind.id === source.connector)?.name || source.connector)}</td><td data-label="间隔">${source.every_minutes} 分钟</td><td data-label="最近">${esc(stamp(source.last_fetch))}</td><td data-label="状态">${source.last_error ? `<span class="pill bad">失败</span><small>${esc(source.last_error)}</small>` : `<span class="pill ${source.enabled ? "good" : ""}">${source.enabled ? "启用" : "暂停"}</span>`}</td><td data-label="操作"><div class="row-actions"><button data-fetch="${esc(source.id)}">立即抓取</button><button data-edit-source="${esc(source.id)}">编辑</button><button data-delete-source="${esc(source.id)}">删除</button></div></td></tr>`).join("") || '<tr><td colspan="6" class="empty-cell">还没有来源。添加 RSS、GitHub 仓库或要监控的网页。</td></tr>'}</tbody></table></div></section>`;
    area.querySelectorAll("[data-edit-source]").forEach((button) => { button.onclick = () => sourceForm(data, data.sources.find((source) => source.id === button.dataset.editSource)); });
    area.querySelectorAll("[data-fetch]").forEach((button) => { button.onclick = guard(async () => {
      button.disabled = true;
      try { await send(`/admin/intel/sources/${button.dataset.fetch}/fetch`, "POST"); toast("抓取完成"); await render(); } finally { button.disabled = false; }
    }); });
    area.querySelectorAll("[data-delete-source]").forEach((button) => { button.onclick = () => {
      const source = data.sources.find((entry) => entry.id === button.dataset.deleteSource);
      confirmAction(`删除来源 · ${source.name}`, "同时删除这个源的全部条目（包含星标）和凭据，不能恢复。", async () => { await send(`/admin/intel/sources/${source.id}`, "DELETE"); if (filter.source === source.id) filter.source = ""; await render(); });
    }; });
  } else if (view === "rules") {
    area.innerHTML = `<section class="panel"><div class="panel-body"><p class="hint">按创建顺序匹配，第一条命中的规则生效；没有命中则放进简报。修改规则只影响后续新条目。</p><ul class="intel-rules">${ruleData.rules.map((rule) => `<li><div><strong>${esc(rule.name)}</strong><p>${esc(rule.keywords.join(" / ") || "全部关键词")} · ${esc(rule.sources.map(sourceName).join("、") || "全部来源")} · ${esc(ACTIONS[rule.action])}</p></div><div class="row-actions"><button data-edit-rule="${rule.id}">编辑</button><button data-delete-rule="${rule.id}">删除</button></div></li>`).join("") || '<li class="muted">还没有规则，所有新条目默认放进简报。</li>'}</ul></div></section>`;
    area.querySelectorAll("[data-edit-rule]").forEach((button) => { button.onclick = () => ruleForm(data, ruleData.rules.find((rule) => rule.id === Number(button.dataset.editRule))); });
    area.querySelectorAll("[data-delete-rule]").forEach((button) => { button.onclick = () => {
      const rule = ruleData.rules.find((entry) => entry.id === Number(button.dataset.deleteRule));
      confirmAction(`删除规则 · ${rule.name}`, "删除后不能恢复，不改变已有条目的处置状态。", async () => { await send(`/admin/intel/rules/${rule.id}`, "DELETE"); await render(); });
    }; });
  } else {
    const params = new URLSearchParams({ source: filter.source, q: filter.q, unread: filter.unread ? "1" : "", starred: filter.starred ? "1" : "" });
    const result = await api(`/admin/intel/items?${params}`);
    if (request !== generation || !root.isConnected || state.tab !== "intel") return;
    let items = result.items;
    area.innerHTML = `<section class="panel"><div class="panel-head intel-filters"><select id="intel-source-filter" aria-label="情报来源"><option value="">全部来源</option>${data.sources.map((source) => `<option value="${esc(source.id)}" ${source.id === filter.source ? "selected" : ""}>${esc(source.name)}</option>`).join("")}</select><input id="intel-search" type="search" maxlength="200" placeholder="搜索标题或内容" aria-label="搜索情报" value="${esc(filter.q)}"><label class="checkbox"><input id="intel-unread" type="checkbox" ${filter.unread ? "checked" : ""}>未读</label><label class="checkbox"><input id="intel-starred" type="checkbox" ${filter.starred ? "checked" : ""}>星标</label></div><div id="intel-items">${items.map(itemHTML).join("") || '<p class="empty-cell">还没有匹配的条目。先添加来源，后台会定期抓取。</p>'}</div>${items.length === 80 ? '<div class="panel-body"><button id="intel-more">加载更早的</button></div>' : ""}</section>`;
    const wireItems = () => {
      wireTodoRefs(area);
      for (const [selector, field, attribute] of [["[data-read]", "read", "read"], ["[data-star]", "starred", "star"]]) {
        area.querySelectorAll(selector).forEach((button) => { button.onclick = guard(async () => {
          const item = items.find((entry) => entry.id === Number(button.dataset[attribute]));
          button.disabled = true;
          try {
            await send(`/admin/intel/items/${item.id}/mark`, "POST", { [field]: !item[field] });
            item[field] = !item[field];
            button.textContent = field === "read" ? (item.read ? "标为未读" : "标为已读") : (item.starred ? "取消星标" : "加星标");
            if ((filter.unread && item.read) || (filter.starred && !item.starred)) button.closest(".intel-item").remove();
          } finally { button.disabled = false; }
        }); });
      }
    };
    wireItems();
    area.querySelector("#intel-source-filter").onchange = (event) => { filter.source = event.target.value; render(); };
    for (const name of ["unread", "starred"]) area.querySelector(`#intel-${name}`).onchange = (event) => { filter[name] = event.target.checked; render(); };
    let timer;
    area.querySelector("#intel-search").oninput = (event) => {
      clearTimeout(timer);
      timer = setTimeout(() => { filter.q = event.target.value.trim(); render(); }, 400);
    };
    const more = area.querySelector("#intel-more");
    if (more) more.onclick = guard(async () => {
      params.set("before", items.at(-1).id);
      const next = await api(`/admin/intel/items?${params}`);
      items = items.concat(next.items);
      area.querySelector("#intel-items").insertAdjacentHTML("beforeend", next.items.map(itemHTML).join(""));
      wireItems();
      if (next.items.length < 80) more.remove();
    });
  }
}

function sourceForm(data, existing) {
  const source = existing || { name: "", connector: "rss", config: {}, enabled: true, every_minutes: 15 };
  const dialog = modal(existing ? `编辑来源 · ${source.name}` : "添加情报源", `<form id="intel-source-form"><div class="form-grid">
    <label>名称<input name="name" required maxlength="80" value="${esc(source.name)}"></label><label>连接器<select name="connector">${data.connectors.map((kind) => `<option value="${esc(kind.id)}" ${source.connector === kind.id ? "selected" : ""}>${esc(kind.name)}</option>`).join("")}</select></label>
    <label data-source-field="url">地址<input name="url" type="url" value="${esc(source.config.url || "")}" placeholder="https://example.com/feed.xml"></label>
    <label data-source-field="repository">GitHub 仓库<input name="repository" value="${esc(source.config.repository || "")}" placeholder="owner/repo"></label>
    <label data-source-field="pattern">提取正则（可选）<input name="pattern" value="${esc(source.config.pattern || "")}" placeholder="第一捕获组作为内容"></label>
    <label>抓取间隔（分钟）<input name="every_minutes" type="number" min="1" max="10080" required value="${source.every_minutes}"></label>
    <label data-source-field="token">GitHub Token（可选）<input name="token" type="password" autocomplete="new-password" placeholder="${source.has_secret ? "已保存，留空不改" : "不填使用公开接口"}"></label></div>
    <label class="checkbox" data-source-field="token"><input name="clear_token" type="checkbox">清除已保存的 Token</label>
    <label class="checkbox"><input name="enabled" type="checkbox" ${source.enabled ? "checked" : ""}>启用定期抓取</label>
    <p class="hint">只允许公网 HTTP / HTTPS 目标。抓取最多 1 MB，不执行网页脚本；令牌加密保存，列表不会返回。网页初次抓取会记录初始内容，后续只记录变化。</p><div class="form-footer"><button type="submit" class="primary">保存来源</button></div></form>`);
  const form = dialog.querySelector("#intel-source-form"), fields = form.elements;
  const update = () => {
    const kind = fields.connector.value;
    form.querySelectorAll("[data-source-field]").forEach((field) => { field.hidden = !({ url: kind !== "github_release", repository: kind === "github_release", token: kind === "github_release", pattern: kind === "webpage" }[field.dataset.sourceField]); });
    fields.url.required = kind !== "github_release";
    fields.repository.required = kind === "github_release";
  };
  fields.connector.onchange = update;
  update();
  form.onsubmit = guard(async (event) => {
    event.preventDefault();
    const kind = fields.connector.value;
    const config = kind === "github_release" ? { repository: fields.repository.value.trim() } : { url: fields.url.value.trim(), ...(kind === "webpage" ? { pattern: fields.pattern.value } : {}) };
    const payload = { name: fields.name.value.trim(), connector: kind, config, enabled: fields.enabled.checked, every_minutes: Number(fields.every_minutes.value) };
    if (fields.clear_token.checked) payload.set_secret = "";
    else if (kind === "github_release" && fields.token.value) payload.set_secret = fields.token.value;
    await send("/admin/intel/sources" + (existing ? "/" + source.id : ""), existing ? "PUT" : "POST", payload);
    dialog.close();
    view = "sources";
    setPageView(view);
    await render();
  });
}

function ruleForm(data, existing) {
  const rule = existing || { name: "", keywords: [], sources: [], action: "brief" };
  const dialog = modal(existing ? `编辑规则 · ${rule.name}` : "添加情报规则", `<form id="intel-rule-form"><label>名称<input name="name" required maxlength="80" value="${esc(rule.name)}"></label>
    <label>关键词（每行一个，任意一个命中）<textarea name="keywords" rows="4">${esc(rule.keywords.join("\n"))}</textarea></label>
    <fieldset class="intel-rule-sources"><legend>来源（不选表示全部）</legend>${data.sources.map((source) => `<label class="checkbox"><input name="source" type="checkbox" value="${esc(source.id)}" ${rule.sources.includes(source.id) ? "checked" : ""}>${esc(source.name)}</label>`).join("") || '<p class="hint">还没有来源，保存后会匹配后续添加的来源。</p>'}</fieldset>
    <label>匹配后<select name="action">${Object.entries(ACTIONS).map(([id, label]) => `<option value="${id}" ${rule.action === id ? "selected" : ""}>${label}</option>`).join("")}</select></label>
    <p class="hint">关键词留空匹配全部。立即通知使用现有通知出口；没有匹配任何规则的条目默认放进简报。</p><div class="form-footer"><button type="submit" class="primary">保存规则</button></div></form>`);
  const form = dialog.querySelector("#intel-rule-form");
  form.onsubmit = guard(async (event) => {
    event.preventDefault();
    await send("/admin/intel/rules" + (existing ? "/" + rule.id : ""), existing ? "PUT" : "POST", { name: form.elements.name.value.trim(), keywords: form.elements.keywords.value.split(/[\n,，]/).map((word) => word.trim()).filter(Boolean), sources: [...form.querySelectorAll('[name="source"]:checked')].map((field) => field.value), action: form.elements.action.value });
    dialog.close();
    view = "rules";
    setPageView(view);
    await render();
  });
}
