import { api, send, esc, guard, modal, toast } from "./api.js";
import { content, render, confirmAction, state } from "./admin-state.js";

let generation = 0;
const stamp = (seconds) => seconds ? new Date(seconds * 1000).toLocaleString("zh-CN", { hour12: false }) : "未记录";
const factorNames = { enabled: "已开启", disabled: "未开启", unknown: "未确认" };
const dnsNames = { ok: "解析正常", error: "解析失败", private: "含非公网地址，请核实", unknown: "未检查" };
function passwordState(account, config) {
  if (!account.has_password) return "未在保险箱保存密码";
  if (account.outdated) return "密码已更新，旧检测结果已过期";
  if (account.check_error) return `查询失败${account.checked_at ? "，保留上次结果" : "，尚无结果"}`;
  if (!account.checked_at) return account.monitor_password && config.password_checks ? "等待检查" : "未检查";
  if (account.breach_count > 0) return `上次命中 ${account.breach_count} 次`;
  return "上次未命中已知库";
}
function settingsForm(config) {
  const dialog = modal("安全检查设置", `<form id="security-settings-form" method="dialog">
    <label class="checkbox"><input name="password_checks" type="checkbox" ${config.password_checks ? "checked" : ""}>启用 HIBP 密码检查</label>
    <p class="hint">只检查逐个勾选的账户，每个密码每周检查一次。仅向固定 HIBP 端点发送 SHA-1 前五位，并请求响应填充；不发送密码原文、完整哈希或邮箱。对方仍能看到服务器的网络地址。</p>
    <label class="checkbox"><input name="dns_checks" type="checkbox" ${config.dns_checks ? "checked" : ""}>每日检查域名 DNS</label>
    <p class="hint">使用服务器的 DNS 解析器查询域名资产；资产名称不是域名时，可在资产的「域名」字段填写。不会探测域名指向的 HTTP 服务。</p>
    <label class="checkbox"><input name="login_alerts" type="checkbox" ${config.login_alerts ? "checked" : ""}>提醒后台登录失败和新设备登录</label>
    <p class="hint">新设备指没有有效的既有设备 Cookie。提醒沿用告警通知渠道；同类来源每五分钟最多一条。</p>
    <div class="form-footer"><button type="button" data-cancel>取消</button><button type="submit" class="danger">确认保存检查设置</button></div></form>`);
  dialog.querySelector("[data-cancel]").onclick = () => dialog.close();
  const form = dialog.querySelector("form");
  form.onsubmit = guard(async (event) => {
    event.preventDefault(); const fields = new FormData(form), button = form.querySelector('[type="submit"]'); button.disabled = true;
    try {
      await send("/admin/security/settings", "PUT", { password_checks: fields.has("password_checks"), dns_checks: fields.has("dns_checks"), login_alerts: fields.has("login_alerts") });
      dialog.close(); toast("安全检查设置已保存"); await render();
    } finally { button.disabled = false; }
  });
}
function accountForm(account) {
  const dialog = modal("安全记录 · " + account.name, `<form id="security-account-form" method="dialog">
    <label>两步验证<select name="two_factor">${Object.entries(factorNames).map(([value,label])=>`<option value="${value}" ${account.two_factor===value?"selected":""}>${label}</option>`).join("")}</select></label>
    <label>恢复方式放在哪里<input name="recovery_location" maxlength="500" value="${esc(account.recovery_location)}" placeholder="例如：密码管理器的家庭保险库"></label>
    <p class="hint">这里只记录存放位置，请勿填写恢复码。账户现有恢复码仍在保险箱，不会被这里覆盖。</p>
    <label class="checkbox"><input name="monitor_password" type="checkbox" ${account.monitor_password?"checked":""}>同意对这个账户的密码做 HIBP 前缀查询</label>
    <p class="hint">还需在全局设置启用 HIBP。密码更新后旧结果自动过期；未命中不能证明密码安全。</p>
    <label class="checkbox"><input name="review_now" type="checkbox" checked>已人工复核，记录本次检查时间</label>
    <p class="hint">上次人工检查：${stamp(account.last_review)}</p>
    <div class="form-footer"><button type="button" data-cancel>取消</button><button type="submit" class="primary">保存安全记录</button></div></form>`);
  dialog.querySelector("[data-cancel]").onclick = () => dialog.close();
  const form = dialog.querySelector("form");
  form.onsubmit = guard(async(event)=>{
    event.preventDefault();const fields = new FormData(form),button=form.querySelector('[type="submit"]');button.disabled=true;
    try { await send(`/admin/security/accounts/${account.asset_id}`,"PUT",{two_factor:fields.get("two_factor"),recovery_location:fields.get("recovery_location"),monitor_password:fields.has("monitor_password"),review_now:fields.has("review_now")});dialog.close();toast("安全记录已保存");await render(); }
    finally {button.disabled=false;}
  });
}
function expiry(days) { return days == null ? "未记录到期日" : days < 0 ? `已过期 ${-days} 天` : days === 0 ? "今天到期" : `${days} 天后到期`; }
export async function securityPage() {
  const request=++generation,root=content();
  const [config,accountData,risks]=await Promise.all([api("/admin/security/settings"),api("/admin/security/accounts"),api("/admin/security/risks")]);
  if(request!==generation||state.tab!=="security"||!root.isConnected)return;
  const accounts=accountData.accounts,attention=accounts.filter((account)=>account.two_factor!=="enabled").length,breached=accounts.filter((account)=>account.has_password&&!account.outdated&&account.breach_count>0).length;
  root.innerHTML=`<section class="page-heading"><div><h1>数字身份与安全</h1><p>复核账号保护，留意到期和登录异常。</p></div><div class="heading-actions"><button id="security-settings">检查设置</button><button id="security-refresh">刷新</button><button id="security-check" ${config.password_checks?"":"disabled"}>检查已选密码</button></div></section>
    <div class="ov-kpis assets-kpis security-kpis"><div class="ov-kpi"><span class="ov-kpi-label">账户资产</span><strong class="ov-kpi-value">${accounts.length}</strong></div><div class="ov-kpi"><span class="ov-kpi-label">2FA 待复核</span><strong class="ov-kpi-value">${attention}</strong></div><div class="ov-kpi"><span class="ov-kpi-label">当前密码历史命中</span><strong class="ov-kpi-value">${breached}</strong></div></div>
    <p class="hint security-notice">HIBP ${config.password_checks?"已启用，仅检查同意的账号":"已关闭"} · DNS ${config.dns_checks?"每日检查":"已关闭"} · 登录提醒${config.login_alerts?"开启":"关闭"}。未检查或未命中均不代表安全；泄露记录也不代表这个账户已被入侵。</p>
    <section class="panel"><div class="panel-head"><h2>账号保护</h2><span class="hint">复用资产中的账户</span></div>${accounts.length?`<div class="table-scroll"><table><thead><tr><th>账户</th><th>两步验证</th><th>恢复方式位置</th><th>密码观察</th><th>人工检查</th><th>操作</th></tr></thead><tbody>${accounts.map((account)=>`<tr><td><strong>${esc(account.name)}</strong><div class="hint">${esc(account.provider)}</div></td><td>${esc(factorNames[account.two_factor]||"未确认")}</td><td>${esc(account.recovery_location||"未记录")}<div class="hint">${account.has_recovery?"保险箱已有恢复码":"未保存恢复码"}</div></td><td>${esc(passwordState(account,config))}<div class="hint">${account.checked_at?"上次检测："+stamp(account.checked_at):""}</div></td><td>${stamp(account.last_review)}</td><td><button data-security-account="${esc(account.asset_id)}">复核</button></td></tr>`).join("")}</tbody></table></div>`:'<div class="empty">在「资产」添加账户后，这里就能记录两步验证和恢复方式。</div>'}</section>
    <section class="panel"><div class="panel-head"><h2>域名风险</h2><span class="hint">到期日来自资产登记</span></div>${risks.domains.length?`<div class="table-scroll"><table><thead><tr><th>域名</th><th>到期</th><th>DNS 观察</th></tr></thead><tbody>${risks.domains.map((domain)=>`<tr><td>${esc(domain.name)}<div class="hint">${esc(domain.domain||"请在资产中填写有效域名")}</div></td><td class="${domain.days_left!=null&&domain.days_left<=30?"warn-text":""}">${esc(expiry(domain.days_left))}<div class="hint">${esc(domain.expires_on)}</div></td><td>${esc(dnsNames[domain.dns_status]||"未检查")}<div class="hint">${domain.dns_checked_at?"上次："+stamp(domain.dns_checked_at):"对外 DNS 检查需先启用"}</div></td></tr>`).join("")}</tbody></table></div>`:'<div class="empty">还没有域名资产。</div>'}</section>
    <section class="panel"><div class="panel-head"><h2>证书风险</h2><span class="hint">不返回证书私钥</span></div>${risks.certificates.length?`<div class="table-scroll"><table><thead><tr><th>域名</th><th>到期</th><th>签发状态</th></tr></thead><tbody>${risks.certificates.map((cert)=>`<tr><td>${esc(cert.domains.join(", "))}</td><td class="${cert.days_left!=null&&cert.days_left<=30?"warn-text":""}">${esc(expiry(cert.days_left))}<div class="hint">${cert.not_after?stamp(cert.not_after):"尚未签发"}</div></td><td>${cert.has_error?"上次签发失败，请到证书页查看":cert.status==="valid"?"有效":esc(cert.status)}</td></tr>`).join("")}</tbody></table></div>`:'<div class="empty">还没有托管证书。</div>'}</section>
    <section class="panel"><div class="panel-head"><h2>登录异常</h2></div><div class="panel-body"><p>登录失败和新设备登录记录在审计日志中；开启提醒后，同步进入告警与通知。</p><p class="hint">新设备依据已签名的设备 Cookie 判断。清理 Cookie、过期或改密码后可能再次提醒；IP 来源链为记录值。</p><div class="row-actions"><button id="security-audit">查看审计日志</button><button id="security-alerts">查看告警与通知</button></div></div></section>`;
  root.querySelector("#security-settings").onclick=()=>settingsForm(config);
  root.querySelector("#security-refresh").onclick=guard(render);
  root.querySelectorAll("[data-security-account]").forEach((button)=>{button.onclick=()=>accountForm(accounts.find((account)=>account.asset_id===button.dataset.securityAccount));});
  root.querySelector("#security-check").onclick=()=>confirmAction("检查已选账号密码","将读取保险箱里已同意检查的密码，只发送 SHA-1 前五位到 HIBP。继续查询？",async()=>{const button=root.querySelector("#security-check");button.disabled=true;try{const result=await send("/admin/security/password-check","POST");toast(`已处理 ${result.checked} 项，请逐项查看结果`);await render();}finally{button.disabled=false;}});
  for(const [id,tab] of [["security-audit","audit"],["security-alerts","alerts"]])root.querySelector("#"+id).onclick=guard(async()=>{state.tab=tab;await render();});
}
