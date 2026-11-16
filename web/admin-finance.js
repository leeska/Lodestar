import { api, send, esc, guard, modal, toast } from "./api.js";
import { content, render, confirmAction, state, pageView, setPageView } from "./admin-state.js";
import { fxHint } from "./format.js";

let view = "overview", month = "", generation = 0;
let transactionFilter = { account: "", category: "" };
let draft = { csv: "", columns: {}, delimiter: ",", date_format: "2006-01-02", number_format: "plain", account_id: "", deduplicate: true, invert: false };
const accountKinds = { cash: "现金", bank: "银行", investment: "投资", liability: "负债", other: "其他" };
const stamp = (seconds) => new Date(seconds * 1000).toLocaleString("zh-CN", { hour12: false });
const money = (amount, currency) => `${esc(currency)} ${esc(amount)}`;
const options = (values, selected) => values.map((value) => `<option value="${esc(value)}" ${value === selected ? "selected" : ""}>${esc(value)}</option>`).join("");
const localInput = (seconds) => { const date = new Date(seconds * 1000); return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16); };
function bindForm(dialog, save) {
  const form = dialog.querySelector("form");
  form.querySelector("[data-cancel]").onclick = () => dialog.close();
  form.onsubmit = guard(async (event) => {
    event.preventDefault();
    const button = form.querySelector('[type="submit"]'); button.disabled = true;
    try { await save(new FormData(form)); dialog.close(); await render(); } finally { button.disabled = false; }
  });
  return form;
}
function accountForm(config, existing = null) {
  const account = existing || { name: "", kind: "bank", currency: config.settings.currency, opening_balance: "0.00", balance_mode: "ledger", manual_balance: "0.00" };
  const dialog = modal(existing ? "编辑记账账户" : "添加记账账户", `<form id="finance-account-form" method="dialog">
    <label>名称<input name="name" required maxlength="80" value="${esc(account.name)}"></label>
    <div class="form-grid"><label>类型<select name="kind">${Object.entries(accountKinds).map(([id, name]) => `<option value="${id}" ${id === account.kind ? "selected" : ""}>${name}</option>`).join("")}</select></label><label>币种<select name="currency">${options(config.currencies, account.currency)}</select></label></div>
    <label>余额方式<select name="balance_mode"><option value="ledger" ${account.balance_mode === "ledger" ? "selected" : ""}>期初余额加流水</option><option value="manual" ${account.balance_mode === "manual" ? "selected" : ""}>手工填写当前余额</option></select></label>
    <div class="form-grid"><label>期初余额<input name="opening_balance" required inputmode="decimal" value="${esc(account.opening_balance)}"></label><label>当前手工余额<input name="manual_balance" required inputmode="decimal" value="${esc(account.manual_balance)}"></label></div>
    <p class="hint">自动余额由期初与流水推算；手工模式以填写值为准。负债填负数，有流水的账户不能更换币种。</p>
    <div class="form-footer"><button type="button" data-cancel>取消</button><button type="submit" class="primary">保存账户</button></div></form>`);
  bindForm(dialog, async (fields) => { await send(`/admin/finance/accounts${existing ? "/" + account.id : ""}`, existing ? "PUT" : "POST", Object.fromEntries(fields)); view = "accounts"; setPageView(view); toast("记账账户已保存"); });
}
function transactionForm(accounts, existing = null) {
  if (!accounts.length) { toast("请先添加一个记账账户"); return; }
  const item = existing || { account_id: accounts[0].id, at: Math.floor(Date.now() / 1000), amount: "", category: "", note: "", ref: "" };
  const dialog = modal(existing ? "编辑流水" : "记录流水", `<form id="finance-transaction-form" method="dialog">
    <label>账户<select name="account_id">${accounts.map((account) => `<option value="${esc(account.id)}" ${account.id === item.account_id ? "selected" : ""}>${esc(account.name)} · ${esc(account.currency)}</option>`).join("")}</select></label>
    <div class="form-grid"><label>记账时间<input type="datetime-local" name="at" required value="${esc(localInput(item.at))}"></label><label>金额<input name="amount" required inputmode="decimal" placeholder="支出 -12.50，流入 12.50" value="${esc(item.amount)}"></label></div>
    <label>分类<input name="category" maxlength="60" value="${esc(item.category)}" placeholder="餐饮、订阅、工资……"></label><label>备注<textarea name="note" maxlength="2000">${esc(item.note)}</textarea></label>
    <p class="hint">金额按账户币种记录，流入填正数，支出填负数。时间按当前设备时区填写。</p>
    <div class="form-footer"><button type="button" data-cancel>取消</button><button type="submit" class="primary">保存流水</button></div></form>`);
  bindForm(dialog, async (fields) => {
    const at = Math.floor(new Date(fields.get("at")).getTime() / 1000);
    if (!Number.isFinite(at)) throw new Error("请选择有效时间");
    const body = { account_id: fields.get("account_id"), at, amount: fields.get("amount"), category: fields.get("category"), note: fields.get("note"), ref: item.ref };
    await send(`/admin/finance/transactions${existing ? "/" + item.id : ""}`, existing ? "PUT" : "POST", body);
    view = "transactions"; setPageView(view); toast("本地流水已保存");
  });
}
function budgetForm(config, existing = null) {
  const budget = existing || { category: "", period: month, amount: "", currency: config.settings.currency };
  const dialog = modal(existing ? "修改预算" : "设置月度预算", `<form id="finance-budget-form" method="dialog">
    <label>分类<input name="category" required maxlength="60" value="${esc(budget.category)}"></label><div class="form-grid"><label>月份<input name="period" type="month" required value="${esc(budget.period)}"></label><label>币种<select name="currency">${options(config.currencies, budget.currency)}</select></label></div>
    <label>预算金额<input name="amount" inputmode="decimal" required value="${esc(budget.amount)}"></label><p class="hint">同分类的正数退款会抵扣净支出。跨币种按固定参考汇率估算。</p>
    <div class="form-footer"><button type="button" data-cancel>取消</button><button type="submit" class="primary">保存预算</button></div></form>`);
  bindForm(dialog, async (fields) => { await send(`/admin/finance/budgets${existing ? "/" + budget.id : ""}`, existing ? "PUT" : "POST", Object.fromEntries(fields)); month = fields.get("period"); view = "budgets"; setPageView(view); toast("预算已保存"); });
}
function settingsForm(config) {
  const dialog = modal("财务汇总设置", `<form id="finance-settings-form" method="dialog"><label>汇总币种<select name="currency">${options(config.currencies, config.settings.currency)}</select></label><label>统计与快照时区<input name="timezone" required value="${esc(config.settings.timezone)}"></label><p class="hint">${esc(fxHint)} 切换汇总币种不会改写历史原币金额，曲线显示所选币种的快照。</p><div class="form-footer"><button type="button" data-cancel>取消</button><button type="submit" class="primary">保存设置</button></div></form>`);
  bindForm(dialog, async (fields) => { await send("/admin/finance/settings", "PUT", Object.fromEntries(fields)); month = ""; });
}
function curve(snapshots, currency) {
  if (!snapshots.length) return '<p class="empty-cell">还没有余额快照。建好账户后每天记录一次，也可更新今日快照。</p>';
  const values = snapshots.map((snapshot) => Number(snapshot.net_worth));
  const low = Math.min(...values), high = Math.max(...values), spread = high - low || Math.max(1, Math.abs(high) * 0.1);
  const points = snapshots.map((snapshot, index) => ({ x: snapshots.length === 1 ? 450 : 25 + index / (snapshots.length - 1) * 850, y: 180 - (Number(snapshot.net_worth) - low) / spread * 150, snapshot }));
  return `<div class="finance-curve"><svg viewBox="0 0 900 210" role="img" aria-label="每日净资产趋势"><path d="M25 180H875" class="finance-axis"/><polyline points="${points.map((point) => `${point.x.toFixed(2)},${point.y.toFixed(2)}`).join(" ")}" class="finance-line"/>${points.map((point) => `<circle cx="${point.x.toFixed(2)}" cy="${point.y.toFixed(2)}" r="3.5"><title>${esc(point.snapshot.day)} · ${money(point.snapshot.net_worth, currency)}</title></circle>`).join("")}</svg><div class="finance-curve-caption"><span>${esc(snapshots[0].day)}</span><span>${esc(snapshots.at(-1).day)}</span></div></div>`;
}
function rowsHTML(transactions) {
  return transactions.map((item) => `<tr><td data-label="时间">${esc(stamp(item.at))}</td><td data-label="账户">${esc(item.account_name)}</td><td data-label="金额" class="${item.amount.startsWith("-") ? "warn-text" : ""}">${money(item.amount, item.currency)}</td><td data-label="分类">${esc(item.category)}</td><td data-label="备注">${esc(item.note)}</td><td data-label="操作"><div class="row-actions"><button data-edit-transaction="${item.id}">编辑</button><button data-delete-transaction="${item.id}">删除</button></div></td></tr>`).join("");
}
export async function financePage() {
  view = pageView("overview", ["overview", "accounts", "transactions", "budgets", "recurring", "import"]);
  const request = ++generation, root = content();
  const [config, data] = await Promise.all([api("/admin/finance/settings"), api("/admin/finance/accounts")]);
  if (request !== generation || !root.isConnected || state.tab !== "finance") return;
  if (!month) month = config.current_month;
  const tabs = [["overview", "收支与净资产"], ["accounts", "账户"], ["transactions", "流水"], ["budgets", "预算"], ["recurring", "固定支出"], ["import", "导入 CSV"]];
  root.innerHTML = `<section class="page-heading"><div><h1>财务</h1><p>记录收支、账户和预算，保留每天的余额变化。</p></div><div class="heading-actions"><button id="finance-settings">汇总设置</button><button id="finance-add-account">添加账户</button><button id="finance-add-transaction" class="primary">记录流水</button></div></section><div class="segmented settings-tabs">${tabs.map(([id, label]) => `<button data-finance-view="${id}" class="${view === id ? "active" : ""}">${label}</button>`).join("")}</div><div id="finance-content"></div>`;
  root.querySelector("#finance-settings").onclick = () => settingsForm(config);
  root.querySelector("#finance-add-account").onclick = () => accountForm(config);
  root.querySelector("#finance-add-transaction").onclick = () => transactionForm(data.accounts);
  root.querySelectorAll("[data-finance-view]").forEach((button) => { button.onclick = () => { view = button.dataset.financeView; setPageView(view); render(); }; });
  const selected=root.querySelector("[data-finance-view].active"), strip=selected.parentElement;
  strip.scrollLeft+=selected.getBoundingClientRect().left-strip.getBoundingClientRect().left-(strip.clientWidth-selected.clientWidth)/2;
  const area = root.querySelector("#finance-content");
  const active = () => request === generation && root.isConnected && state.tab === "finance";
  if (view === "accounts") {
    area.innerHTML = `<section class="panel"><div class="table-scroll"><table class="stack"><thead><tr><th>账户</th><th>类型</th><th>余额</th><th>方式</th><th>操作</th></tr></thead><tbody>${data.accounts.map((account) => `<tr><td data-label="账户">${esc(account.name)}</td><td data-label="类型">${esc(accountKinds[account.kind] || account.kind)}</td><td data-label="余额">${money(account.balance, account.currency)}</td><td data-label="方式">${account.balance_mode === "manual" ? "手工余额" : "期初加流水"}</td><td data-label="操作"><div class="row-actions"><button data-edit-account="${esc(account.id)}">编辑</button><button data-delete-account="${esc(account.id)}">删除</button></div></td></tr>`).join("") || '<tr><td colspan="5" class="empty-cell">还没有记账账户。</td></tr>'}</tbody></table></div></section>`;
    area.querySelectorAll("[data-edit-account]").forEach((button) => { button.onclick = () => accountForm(config, data.accounts.find((account) => account.id === button.dataset.editAccount)); });
    area.querySelectorAll("[data-delete-account]").forEach((button) => { button.onclick = () => confirmAction("删除记账账户", "永久删除空账户，已有流水时会拒绝删除。历史快照保留。", async () => { await send(`/admin/finance/accounts/${button.dataset.deleteAccount}`, "DELETE"); await render(); }); });
  } else if (view === "transactions") {
    const params = new URLSearchParams({ month, account: transactionFilter.account, category: transactionFilter.category });
    const result = await api(`/admin/finance/transactions?${params}`); if (!active()) return;
    let transactions = result.transactions;
    area.innerHTML = `<section class="panel"><div class="panel-head finance-filters"><input type="month" id="finance-month" aria-label="记账月份" value="${esc(month)}"><select id="finance-account-filter" aria-label="记账账户"><option value="">全部账户</option>${data.accounts.map((account) => `<option value="${esc(account.id)}" ${account.id === transactionFilter.account ? "selected" : ""}>${esc(account.name)}</option>`).join("")}</select><input id="finance-category-filter" placeholder="按分类筛选" aria-label="分类" value="${esc(transactionFilter.category)}"></div><div class="table-scroll"><table class="stack"><thead><tr><th>时间</th><th>账户</th><th>金额</th><th>分类</th><th>备注</th><th>操作</th></tr></thead><tbody id="finance-transactions">${rowsHTML(transactions) || '<tr><td colspan="6" class="empty-cell">这个月份还没有流水。</td></tr>'}</tbody></table></div><div class="panel-body"><button id="finance-more" ${transactions.length === 200 ? "" : "hidden"}>加载更早记录</button></div></section>`;
    const wire = () => {
      area.querySelectorAll("[data-edit-transaction]").forEach((button) => { button.onclick = () => transactionForm(data.accounts, transactions.find((item) => item.id === Number(button.dataset.editTransaction))); });
      area.querySelectorAll("[data-delete-transaction]").forEach((button) => { button.onclick = () => confirmAction("删除本地流水", "永久删除账本记录，账户推算余额会随之改变。", async () => { await send(`/admin/finance/transactions/${button.dataset.deleteTransaction}`, "DELETE"); await render(); }); });
    };
    wire();
    area.querySelector("#finance-month").onchange = (event) => { if (event.target.value) { month = event.target.value; render(); } };
    area.querySelector("#finance-account-filter").onchange = (event) => { transactionFilter.account = event.target.value; render(); };
    area.querySelector("#finance-category-filter").onchange = (event) => { transactionFilter.category = event.target.value.trim(); render(); };
    area.querySelector("#finance-more").onclick = guard(async () => { params.set("before", transactions.at(-1).id); const next = await api(`/admin/finance/transactions?${params}`); if (!active()) return; transactions = transactions.concat(next.transactions); area.querySelector("#finance-transactions").insertAdjacentHTML("beforeend", rowsHTML(next.transactions)); area.querySelector("#finance-more").hidden = next.transactions.length < 200; wire(); });
  } else if (view === "budgets") {
    const result = await api(`/admin/finance/budgets?month=${encodeURIComponent(month)}`); if (!active()) return;
    area.innerHTML = `<section class="panel"><div class="panel-head"><input id="finance-month" type="month" aria-label="预算月份" value="${esc(month)}"><button id="finance-add-budget">设置预算</button></div><div class="panel-body"><ul class="finance-budgets">${result.budgets.map((budget) => `<li><div><strong>${esc(budget.category)}</strong><small>净支出 ${money(budget.spent, budget.currency)} / 预算 ${money(budget.amount, budget.currency)}</small><meter min="0" max="${Math.max(0.01, Number(budget.amount))}" value="${Math.max(0, Number(budget.spent))}">${esc(budget.spent)}</meter>${Number(budget.spent) > Number(budget.amount) ? '<span class="pill warn">超出预算</span>' : ""}</div><div class="row-actions"><button data-edit-budget="${budget.id}">编辑</button><button data-delete-budget="${budget.id}">删除</button></div></li>`).join("") || '<li class="muted">这个月还没有预算。</li>'}</ul></div></section>`;
    area.querySelector("#finance-month").onchange = (event) => { if (event.target.value) { month = event.target.value; render(); } };
    area.querySelector("#finance-add-budget").onclick = () => budgetForm(config);
    area.querySelectorAll("[data-edit-budget]").forEach((button) => { button.onclick = () => budgetForm(config, result.budgets.find((budget) => budget.id === Number(button.dataset.editBudget))); });
    area.querySelectorAll("[data-delete-budget]").forEach((button) => { button.onclick = () => confirmAction("删除预算", "永久删除这条预算，流水保留。", async () => { await send(`/admin/finance/budgets/${button.dataset.deleteBudget}`, "DELETE"); await render(); }); });
  } else if (view === "recurring") {
    const result = await api("/admin/finance/recurring"); if (!active()) return;
    area.innerHTML = `<section class="panel"><div class="panel-head"><div><h3>每月固定支出 · ${money(result.monthly, result.currency)}</h3><small>按 30 天估算 · 不自动记流水${result.unknown ? " · " + result.unknown + " 项费用未填写完整" : ""}</small></div></div><div class="table-scroll"><table class="stack"><thead><tr><th>资产或节点</th><th>周期费用</th><th>每月估算</th></tr></thead><tbody>${result.items.map((item) => `<tr><td data-label="资产或节点">${esc(item.name)}</td><td data-label="周期费用">${item.cost ? money(item.cost, item.currency) : "未填写"} ${esc(item.period)}</td><td data-label="每月估算">${item.monthly == null ? "未知" : money(item.monthly, result.currency)}</td></tr>`).join("") || '<tr><td colspan="3" class="empty-cell">还没有周期费用。</td></tr>'}</tbody></table></div><p class="hint panel-foot">${esc(fxHint)}</p></section>`;
  } else if (view === "import") {
    await importPage(area, data.accounts, active);
  } else {
    const [summary, history] = await Promise.all([api(`/admin/finance/summary?month=${encodeURIComponent(month)}`), api("/admin/finance/snapshots")]); if (!active()) return;
    const tiles = [["净资产", data.accounts.length ? money(data.net_worth, data.currency) : "尚未记账"], ["本月流入", money(summary.inflow, summary.currency)], ["本月流出", money(summary.outflow, summary.currency)], ["本月净流入", money(summary.net, summary.currency)]];
    area.innerHTML = `<div class="ov-kpis assets-kpis finance-kpis">${tiles.map(([label, value]) => `<div class="ov-kpi"><span>${label}</span><strong>${value}</strong></div>`).join("")}</div><section class="panel"><div class="panel-head"><h3>每日净资产</h3><button id="finance-snapshot">更新今日快照</button></div><div class="panel-body">${curve(history.snapshots, data.currency)}<p class="hint">${esc(fxHint)} 每日首次自动记录，手动更新会替换今日快照。</p></div></section><section class="panel"><div class="panel-head"><h3>月度账本</h3><input id="finance-month" type="month" aria-label="汇总月份" value="${esc(month)}"></div><div class="panel-body"><p>${esc(summary.month)} 共 ${summary.count} 条流水，统计时区 ${esc(config.settings.timezone)}。</p><p class="hint">账户余额可手工填写或由期初加流水推算。此处流入、流出汇总本地账本记录。</p></div></section>`;
    area.querySelector("#finance-month").onchange = (event) => { if (event.target.value) { month = event.target.value; render(); } };
    area.querySelector("#finance-snapshot").onclick = guard(async () => { const result = await send("/admin/finance/snapshot", "POST"); toast(result.snapshot ? "今日快照已更新" : "请先添加账户"); await render(); });
  }
}

async function importPage(area, accounts, active) {
  if (!accounts.length) { area.innerHTML = '<section class="panel"><p class="empty-cell">请先添加一个记账账户，再导入 CSV。</p></section>'; return; }
  if (!accounts.some((account) => account.id === draft.account_id)) draft.account_id = accounts[0].id;
  area.innerHTML = `<section class="panel"><div class="panel-head"><h3>导入本地账本</h3><span class="hint">UTF-8 · 最多 1 MB / 2000 行</span></div><div class="panel-body"><form id="finance-import-form" method="post">
    <div class="form-grid"><label>目标账户<select name="account_id">${accounts.map((account) => `<option value="${esc(account.id)}" ${account.id === draft.account_id ? "selected" : ""}>${esc(account.name)} · ${esc(account.currency)}</option>`).join("")}</select></label><label>选择文件<input type="file" id="finance-csv-file" accept=".csv,text/csv,text/plain"></label></div>
    <label>或粘贴 CSV<textarea name="csv" rows="7" required>${esc(draft.csv)}</textarea></label><div class="form-grid"><label>分隔符<select name="delimiter"><option value="," ${draft.delimiter === "," ? "selected" : ""}>逗号</option><option value=";" ${draft.delimiter === ";" ? "selected" : ""}>分号</option><option value="tab" ${draft.delimiter === "\t" ? "selected" : ""}>制表符</option></select></label><label>数字格式<select name="number_format"><option value="plain" ${draft.number_format === "plain" ? "selected" : ""}>1234.56（无千分位）</option><option value="en" ${draft.number_format === "en" ? "selected" : ""}>1,234.56</option><option value="eu" ${draft.number_format === "eu" ? "selected" : ""}>1.234,56</option></select></label></div>
    <button type="button" class="button" id="finance-csv-columns">读取表头与映射</button><div id="finance-csv-mapping"></div>
    <label>日期格式<select name="date_format">${[["2006-01-02", "YYYY-MM-DD"], ["2006/01/02", "YYYY/MM/DD"], ["02/01/2006", "DD/MM/YYYY"], ["01/02/2006", "MM/DD/YYYY"], ["2006-01-02 15:04:05", "YYYY-MM-DD HH:mm:ss"], ["2006-01-02T15:04:05Z07:00", "RFC 3339"]].map(([value, label]) => `<option value="${value}" ${value === draft.date_format ? "selected" : ""}>${label}</option>`).join("")}</select></label>
    <label class="checkbox"><input type="checkbox" name="invert" ${draft.invert ? "checked" : ""}>反转金额符号（原表用正数记支出）</label><label class="checkbox"><input type="checkbox" name="deduplicate" ${draft.deduplicate ? "checked" : ""}>跳过已经导入的相同行</label>
    <p class="hint">先检查映射和金额预览，再导入。日期按财务设置时区解析；币种须与账户一致。整份文件校验失败时不会部分记账。</p><div class="form-footer"><button type="submit">预览导入</button><button type="button" class="primary" id="finance-csv-import" disabled>确认导入</button></div></form><div id="finance-csv-preview"></div></div></section>`;
  const form = area.querySelector("form"), importButton = area.querySelector("#finance-csv-import");
  let previewed = "";
  const collect = () => {
    const fields = new FormData(form), columns = {};
    form.querySelectorAll("[data-map]").forEach((select) => { if (select.value) columns[select.dataset.map] = select.value; });
    return { account_id: fields.get("account_id"), csv: fields.get("csv"), columns, delimiter: fields.get("delimiter") === "tab" ? "\t" : fields.get("delimiter"), number_format: fields.get("number_format"), date_format: fields.get("date_format"), invert: fields.has("invert"), deduplicate: fields.has("deduplicate") };
  };
  form.oninput = () => { previewed = ""; importButton.disabled = true; draft = collect(); };
  form.onchange = form.oninput;
  area.querySelector("#finance-csv-file").onchange = guard(async (event) => {
    const file = event.target.files[0]; if (!file) return;
    if (file.size > 1 << 20) throw new Error("CSV 文件超过 1 MB");
    form.elements.csv.value = new TextDecoder("utf-8", { fatal: true }).decode(await file.arrayBuffer());
    form.oninput();
  });
  area.querySelector("#finance-csv-columns").onclick = guard(async () => {
    draft = collect();
    const result = await send("/admin/finance/csv/columns", "POST", { csv: draft.csv, delimiter: draft.delimiter }); if (!active()) return;
    area.querySelector("#finance-csv-mapping").innerHTML = `<div class="form-grid">${[["at", "日期（必选）"], ["amount", "金额（必选）"], ["category", "分类"], ["note", "备注"], ["currency", "币种"], ["ref", "引用"]].map(([field, label]) => `<label>${label}<select data-map="${field}" aria-label="${label}" ${["at", "amount"].includes(field) ? "required" : ""}><option value="">不映射</option>${options(result.columns, draft.columns[field])}</select></label>`).join("")}</div>`;
    previewed = ""; importButton.disabled = true;
  });
  form.onsubmit = guard(async (event) => {
    event.preventDefault();
    draft = collect(); const fingerprint = JSON.stringify(draft);
    const result = await send("/admin/finance/csv/preview", "POST", draft); if (!active() || JSON.stringify(collect()) !== fingerprint) return;
    previewed = fingerprint; importButton.disabled = false;
    area.querySelector("#finance-csv-preview").innerHTML = `<p>共 ${result.total} 行，跳过 ${result.skipped} 行重复记录。预览前 ${result.rows.length} 行。</p><div class="table-scroll"><table class="stack"><thead><tr><th>行</th><th>时间</th><th>金额</th><th>分类</th><th>备注</th><th>状态</th></tr></thead><tbody>${result.rows.map((row) => `<tr><td data-label="行">${row.line}</td><td data-label="时间">${esc(stamp(row.transaction.at))}</td><td data-label="金额">${money(row.transaction.amount, row.transaction.currency)}</td><td data-label="分类">${esc(row.transaction.category)}</td><td data-label="备注">${esc(row.transaction.note)}</td><td data-label="状态">${row.duplicate ? "跳过重复" : "将导入"}</td></tr>`).join("")}</tbody></table></div>`;
  });
  importButton.onclick = guard(async () => {
    if (!previewed || previewed !== JSON.stringify(collect())) throw new Error("导入内容已改变，请重新预览");
    const submitted=collect(), fingerprint=JSON.stringify(submitted);
    const disabled=Array.from(form.querySelectorAll("input,select,textarea,button")).map((element)=>[element,element.disabled]);
    disabled.forEach(([element])=>{element.disabled=true;});
    try {
      const result=await send("/admin/finance/csv/import","POST",submitted);
      toast(`已导入 ${result.imported} 行，跳过 ${result.skipped} 行`);
      if(JSON.stringify(draft)===fingerprint)draft={...draft,csv:"",columns:{}};
      if(active()){view="transactions";setPageView(view);await render();}
    } finally { disabled.forEach(([element,wasDisabled])=>{element.disabled=wasDisabled;}); }
  });
}
