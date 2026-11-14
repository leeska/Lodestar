// 后台「Agent 接入」：给外部 agent（Claude Code、Codex 等）的入口。API、MCP 和 Skill 用同一份能力清单
// （服务端的能力注册表）和同一套访问密钥；每把密钥有权限档和可用模块的限制，可以随时吊销。
import { api, send, esc, toast, modal, guard } from "./api.js";
import { content, confirmAction, render } from "./admin-state.js";

const RISK = { read: ["只读", "good", "只能读取，不能改任何东西"], write: ["可写", "warn", "可以新建和修改，不能删除、换密钥或执行命令"], danger: ["高风险", "bad", "包括删除、换密钥、在节点上执行命令；每次调用仍要带上确认"] };
const DAY = 86400;

const when = (ts) => (ts ? new Date(ts * 1000).toLocaleString("zh-CN", { hour12: false }) : "");
const copyButton = (text, label = "复制") => `<button type="button" class="primary" data-copy="${esc(text)}">${label}</button>`;
const block = (text) => `<div class="command-block"><pre>${esc(text)}</pre>${copyButton(text)}</div>`;

function wireCopy(root) {
  root.querySelectorAll("[data-copy]").forEach((b) => (b.onclick = () => navigator.clipboard?.writeText(b.dataset.copy).then(() => toast("已复制"), () => toast("复制失败，请手动选择"))));
}

function keyStatus(k) {
  if (k.revoked_at) return ["已吊销", ""];
  if (k.expires_at && k.expires_at * 1000 <= Date.now()) return ["已过期", "bad"];
  return ["可用", "good"];
}

const claudeCommand = (kit, key) => `claude mcp add --transport http lodestar ${kit.mcp_url} --header "Authorization: Bearer ${key}"`;
const codexConfig = (kit) => `[mcp_servers.lodestar]\nurl = "${kit.mcp_url}"\nbearer_token_env_var = "LODESTAR_API_KEY"`;

export async function agentsPage() {
  const kit = await api("/admin/api-keys");
  const moduleName = Object.fromEntries(kit.modules.map((m) => [m.id, m.name]));
  const rows = kit.keys.map((k) => {
    const [risk, tone] = RISK[k.max_risk] || [k.max_risk, ""];
    const [status, statusTone] = keyStatus(k);
    const modules = k.modules.length ? k.modules.map((m) => esc(moduleName[m] || m)).join("、") : '<span class="muted">全部</span>';
    const used = k.last_used_at ? `${when(k.last_used_at)}<small>${esc(k.last_ip)}</small>` : '<span class="muted">还没用过</span>';
    const action = k.revoked_at ? `<button class="danger-text" data-delete-key="${esc(k.id)}">删除</button>` : `<button class="danger-text" data-revoke-key="${esc(k.id)}">吊销</button>`;
    return `<tr><td data-label="名字">${esc(k.name)}<small>创建于 ${when(k.created_at)}</small></td><td data-label="权限"><span class="pill ${tone}">${risk}</span></td><td data-label="模块">${modules}</td><td data-label="最近使用">${used}</td><td data-label="到期">${k.expires_at ? when(k.expires_at) : '<span class="muted">不过期</span>'}</td><td data-label="状态"><span class="pill ${statusTone}">${status}</span></td><td data-label="操作">${action}</td></tr>`;
  }).join("");
  const groups = {};
  for (const a of kit.actions) (groups[a.module] ||= []).push(a);
  const catalog = Object.keys(groups).sort().map((m) => `<section class="agent-catalog-group" data-module="${esc(m)}"><h4>${esc(moduleName[m] || m)} <small>${esc(m)} · ${groups[m].length}</small></h4><ul>${groups[m].map((a) => {
    const [risk, tone] = RISK[a.risk] || [a.risk, ""];
    return `<li data-search="${esc(`${a.name} ${a.title} ${a.description}`.toLowerCase())}"><div><code>${esc(a.name)}</code> <span class="pill ${tone}">${risk}</span></div><strong>${esc(a.title)}</strong><small>${esc(a.description)}</small></li>`;
  }).join("")}</ul></section>`).join("");
  content().innerHTML = `<section data-part="page-heading" class="page-heading"><div><div class="eyebrow">AGENTS</div><h1>Agent 接入</h1><p>让外部 agent（Claude Code、Codex 等）来管理 Lodestar。API、MCP 和 Skill 用的是同一份能力清单和同一套访问密钥；每把密钥可以限定权限和模块，随时吊销，用它做的每次修改都记在审计日志里。</p></div><div class="heading-actions"><button id="new-key" class="primary">＋ 新建访问密钥</button></div></section>
  <section data-part="panel" class="panel"><div class="panel-head"><h3>接入方式</h3><a class="button" href="/api/v1/admin/agent-kit/skill.zip" download>下载 Skill</a></div><div class="panel-body agent-ways">
    <div><h4>MCP</h4><p class="hint">支持 MCP 的客户端直接接这个地址，请求头带 <code>Authorization: Bearer 访问密钥</code>。</p>${block(kit.mcp_url)}</div>
    <div><h4>API</h4><p class="hint"><code>GET /api/v1/actions</code> 列出能用的能力，<code>POST /api/v1/actions/能力名</code> 执行；高风险能力要加 <code>?confirm=true</code>。<a href="${esc(kit.openapi_url)}" target="_blank" rel="noopener">OpenAPI 文档</a>（同样要带密钥）。</p>${block(kit.base_url + "/api/v1/actions")}</div>
    <div><h4>Skill</h4><p class="hint">Skill 包里有使用说明、全部能力的参数和一个调用脚本。解压到 agent 的 skills 目录（Claude Code 是 <code>~/.claude/skills/</code>），把密钥设成环境变量 <code>LODESTAR_API_KEY</code>。</p></div>
    <div><h4>Codex</h4><p class="hint">写进 <code>~/.codex/config.toml</code>，密钥放在环境变量 <code>LODESTAR_API_KEY</code>。</p>${block(codexConfig(kit))}</div>
  </div></section>
  <section data-part="panel" class="panel"><div class="panel-head"><h3>访问密钥</h3></div><div class="table-scroll"><table class="stack"><thead><tr><th>名字</th><th>权限</th><th>模块</th><th>最近使用</th><th>到期</th><th>状态</th><th>操作</th></tr></thead><tbody>${rows || '<tr><td colspan="7" class="empty-cell">还没有访问密钥。点「新建访问密钥」给 agent 开一把。</td></tr>'}</tbody></table></div></section>
  <section data-part="panel" class="panel"><div class="panel-head"><h3>能力清单 <span class="muted">${kit.actions.length}</span></h3><input type="search" id="action-filter" placeholder="搜索能力" aria-label="搜索能力"></div><div class="panel-body agent-catalog">${catalog}</div></section>`;
  wireCopy(content());
  content().querySelector("#new-key").onclick = () => keyForm(kit);
  content().querySelector("#action-filter").oninput = (e) => {
    const q = e.target.value.trim().toLowerCase();
    content().querySelectorAll(".agent-catalog li").forEach((li) => (li.hidden = q !== "" && !li.dataset.search.includes(q)));
    content().querySelectorAll(".agent-catalog-group").forEach((g) => (g.hidden = !g.querySelector("li:not([hidden])")));
  };
  content().querySelectorAll("[data-revoke-key]").forEach((b) => (b.onclick = () => {
    const k = kit.keys.find((x) => x.id === b.dataset.revokeKey);
    confirmAction(`吊销访问密钥 · ${k.name}`, `用「${k.name}」的 agent 会立即失去访问。吊销后不能恢复，需要的话再新建一把。`, async () => { await send(`/admin/api-keys/${k.id}/revoke`, "POST"); await render(); }, { label: "吊销" });
  }));
  content().querySelectorAll("[data-delete-key]").forEach((b) => (b.onclick = guard(async () => { await send(`/admin/api-keys/${b.dataset.deleteKey}`, "DELETE"); await render(); })));
}

function keyForm(kit) {
  const risks = Object.entries(RISK).map(([id, [label, , hint]], i) => `<label class="radio-card"><input type="radio" name="max_risk" value="${id}" ${i === 0 ? "checked" : ""}><span><strong>${label}</strong><small>${hint}</small></span></label>`).join("");
  const modules = kit.modules.map((m) => `<label class="checkbox"><input type="checkbox" name="module" value="${esc(m.id)}">${esc(m.name)}</label>`).join("");
  const d = modal("新建访问密钥", `<form id="key-form">
    <section class="form-section"><h3>基本</h3><div class="form-grid"><label>名字<input name="name" required maxlength="40" placeholder="例如：笔记本上的 Claude Code"></label><label>有效期<select name="ttl"><option value="0">不过期</option><option value="30">30 天</option><option value="90">90 天</option><option value="365">一年</option></select></label></div></section>
    <section class="form-section"><h3>权限</h3><div class="radio-cards">${risks}</div></section>
    <section class="form-section"><h3>模块</h3><label class="checkbox"><input type="checkbox" name="all" checked>全部模块（以后新增的也包括）</label><div class="agent-modules" hidden>${modules}</div></section>
    <div class="form-footer"><button type="submit" class="primary">创建</button></div></form>`);
  const form = d.querySelector("#key-form");
  form.elements.all.onchange = (e) => (form.querySelector(".agent-modules").hidden = e.target.checked);
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const days = Number(form.elements.ttl.value);
    const picked = form.elements.all.checked ? [] : [...form.querySelectorAll('[name="module"]:checked')].map((x) => x.value);
    if (!form.elements.all.checked && !picked.length) return toast("至少选一个模块，或者勾上「全部模块」");
    const out = await send("/admin/api-keys", "POST", {
      name: form.elements.name.value.trim(),
      max_risk: form.querySelector('[name="max_risk"]:checked').value,
      modules: picked,
      expires_at: days ? Math.floor(Date.now() / 1000) + days * DAY : 0,
    });
    showSecret(kit, out);
    await render();
  });
}

function showSecret(kit, out) {
  const d = modal(`访问密钥 · ${out.key.name}`, `<p class="hint"><strong>密钥只显示这一次</strong>，关掉之后就看不到了，请现在存好（比如放进密码管理器或 agent 的环境变量 <code>LODESTAR_API_KEY</code>）。</p>${block(out.secret)}
    <h4>Claude Code</h4>${block(claudeCommand(kit, out.secret))}
    <div class="form-footer"><button type="button" class="primary" id="secret-done">我已存好</button></div>`);
  wireCopy(d);
  d.querySelector("#secret-done").onclick = () => d.close();
}
