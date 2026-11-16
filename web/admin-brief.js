import { api, send, esc, guard, modal, toast } from "./api.js";
import { content, state, go } from "./admin-state.js";

export async function briefPage() {
  const [body, history] = await Promise.all([
    briefSettingsView(),
    api("/admin/timeline?sources=brief&limit=1").catch(() => null),
  ]);
  if (state.tab !== "brief") return;
  const latest = history?.entries?.[0];
  content().innerHTML = `<section data-part="page-heading" class="page-heading"><div><h1>每日简报</h1><p>汇总今天需要关注的事，按你的时间发送。</p></div><button type="button" id="brief-history">历史简报</button></section>
    ${latest ? `<section class="panel"><div class="panel-head"><h2>${esc(latest.title)}</h2></div><pre class="brief-preview panel-body">${esc(latest.body || "")}</pre></section>` : `<p class="notice">${history ? "还没有已发送的简报。可以先预览，再设置发送时间。" : "简报记录读取失败，可以刷新重试。"}</p>`}
    <details class="brief-settings"${latest ? "" : " open"}><summary>简报设置</summary>${body}</details>`;
  wireBriefSettings(briefPage);
  content().querySelector("#brief-history").onclick = () => { state.pageViews.timeline = "brief"; go("timeline"); };
}

const SECTIONS = [["overview", "需要处理"], ["upcoming", "即将到期"], ["events", "最近事件"], ["nodes", "节点在线情况"], ["jobs", "失败的后台任务"], ["intel", "情报"], ["today", "今天的日程与待办"]];

export async function briefSettingsView() {
  const config = await api("/admin/brief/settings");
  return `<section data-part="panel" class="panel"><div class="panel-head"><h3>每日简报</h3></div><form id="brief-settings-form" class="panel-body">
    <label class="checkbox"><input name="enabled" type="checkbox" ${config.enabled ? "checked" : ""}>每天自动发送</label>
    <div class="form-grid"><label>发送时间<input name="time" type="time" required value="${esc(config.time)}"></label><label>时区<input name="timezone" required value="${esc(config.timezone)}" placeholder="Asia/Shanghai"></label></div>
    <fieldset class="brief-sections"><legend>简报内容</legend>${SECTIONS.map(([id, label]) => `<label class="checkbox"><input name="section" value="${id}" type="checkbox" ${config.sections.includes(id) ? "checked" : ""}>${label}</label>`).join("")}</fieldset>
    <label class="checkbox"><input name="use_model" type="checkbox" ${config.use_model ? "checked" : ""}>配置了助理时，让模型整理成口语化摘要</label>
    <p class="hint">每小时检查一次，到设定时间后发送。模型不可用时使用原始数据版。通过已设置的通知出口投递，完整简报保存在时间线中；情报和日程尚未配置时会略过对应部分。</p>
    <div class="form-footer"><button type="button" id="brief-preview">预览</button><button type="button" id="brief-send">立即发送一份</button><button type="submit" class="primary">保存简报设置</button></div>
  </form></section>`;
}

export function wireBriefSettings(refresh) {
  const form = content().querySelector("#brief-settings-form");
  form.onsubmit = guard(async (event) => {
    event.preventDefault();
    const fields = form.elements;
    await send("/admin/brief/settings", "PUT", { enabled: fields.enabled.checked, time: fields.time.value, timezone: fields.timezone.value.trim(), use_model: fields.use_model.checked, sections: [...form.querySelectorAll('[name="section"]:checked')].map((field) => field.value) });
    toast("简报设置已保存");
    await refresh();
  });
  form.querySelector("#brief-preview").onclick = guard(async () => {
    const document = await api("/admin/brief/preview");
    modal(document.title, `<p class="hint">${document.model_used ? "模型整理版" : "原始数据版"} · 使用已保存的设置</p><pre class="brief-preview">${esc(document.body)}</pre>`);
  });
  form.querySelector("#brief-send").onclick = guard(async (event) => {
    const button = event.currentTarget;
    button.disabled = true;
    try {
      await send("/admin/brief/send", "POST");
      toast("简报已发送，完整内容已保存到时间线");
    } finally { button.disabled = false; }
  });
}
