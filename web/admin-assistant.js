import { api, send, esc, guard, modal, toast } from "./api.js";
import { content, state, go, confirmAction } from "./admin-state.js";
import { assistantMarkdown } from "./assistant-markdown.js";

let selected = "";

function toolHTML(call, result = "") {
  return `<details class="assistant-tool" data-call="${esc(call.id)}"><summary>调用 ${esc(call.name)}</summary><pre>${esc(JSON.stringify(call.arguments, null, 2))}</pre>${result ? `<pre>${esc(result)}</pre>` : ""}</details>`;
}

function messagesHTML(messages) {
  const results = new Map(messages.filter((message) => message.role === "tool").map((message) => [message.call_id, message.content]));
  return messages.filter((message) => message.role !== "tool").map((message) => `<article class="assistant-message ${message.role === "user" ? "from-user" : "from-assistant"}"><strong class="assistant-speaker">${message.role === "user" ? "你" : "助理"}</strong><div class="assistant-prose">${assistantMarkdown(message.content)}</div>${(message.calls || []).map((call) => toolHTML(call, results.get(call.id))).join("")}</article>`).join("");
}

export async function assistantPage() {
  const [config, data] = await Promise.all([api("/admin/assistant/settings"), api("/admin/assistant/conversations")]);
  // 一次对话在服务端后台运行：离开页面、切换会话只断开这里的连接，不会停止处理。
  let controller, busy = false, alive = true, attachment = 0;
  state.pageCleanup = () => { alive = false; controller?.abort(); };
  const root = content();
  root.innerHTML = `<section data-part="page-heading" class="page-heading"><div><h1>助理</h1><p>查到期、看状态，或交代一件要处理的事。</p></div><div class="heading-actions"><button id="assistant-settings">助理设置</button></div></section>
  <section class="panel assistant-layout">
    <aside class="assistant-sidebar"><button class="primary" id="assistant-new">新对话</button><nav id="assistant-conversations" aria-label="历史会话"></nav></aside>
    <div class="assistant-chat">
      <div class="assistant-chat-head"><strong id="assistant-title">新对话</strong><div><button class="button" id="assistant-rename" hidden>改名</button><button class="button" id="assistant-delete" hidden>删除</button></div></div>
      <div id="assistant-messages" class="assistant-messages" role="log" aria-label="对话消息"></div>
      <div id="assistant-confirm"></div>
      <p id="assistant-status" class="hint" role="status" aria-live="polite"></p>
      <form id="assistant-form"><label for="assistant-input" class="sr-only">发给助理的消息</label><textarea id="assistant-input" rows="3" maxlength="16000" placeholder="${config.url ? "例如：下个月有哪些东西到期？" : "先在助理设置中填写网关和模型"}" ${config.url ? "" : "disabled"}></textarea><div class="assistant-compose-foot"><small>查询和日常记录直接执行；删除、改动基础设施或通知去向前都会先问你。离开页面也会继续处理。Ctrl / ⌘ + Enter 发送。</small><button class="button" id="assistant-stop" type="button" hidden>停止</button><button class="primary" id="assistant-send" type="submit" ${config.url ? "" : "disabled"}>发送</button></div></form>
    </div>
  </section>`;
  const query = (selector) => root.querySelector(selector);
  const box = query("#assistant-messages");
  const input = query("#assistant-input");
  const status = query("#assistant-status");
  const setBusy = (value) => {
    busy = value;
    query("#assistant-send").disabled = value || !config.url;
    query("#assistant-stop").hidden = !value;
    query("#assistant-form").setAttribute("aria-busy", String(value));
    root.querySelectorAll("#assistant-rename, #assistant-delete").forEach((button) => { button.disabled = value; });
    query("#assistant-confirm").querySelectorAll("button").forEach((button) => { button.disabled = value; });
  };
  const detach = () => { attachment++; controller?.abort(); controller = null; };
  const sidebar = (conversations) => {
    query("#assistant-conversations").innerHTML = conversations.map((conversation) => `<button type="button" data-conversation="${esc(conversation.id)}" class="${selected === conversation.id ? "active" : ""}" ${selected === conversation.id ? 'aria-current="true"' : ""}>${esc(conversation.title)}</button>`).join("") || '<p class="hint">对话会保存在这里。</p>';
    root.querySelectorAll("[data-conversation]").forEach((button) => { button.onclick = guard(async () => {
      if (button.dataset.conversation === selected) return;
      detach();
      selected = button.dataset.conversation;
      await load();
    }); });
  };
  const showConfirmation = (callID, confirmation) => {
    query("#assistant-confirm").innerHTML = `<section class="assistant-confirm"><h3>需要你确认：${esc(confirmation.title || confirmation.action)}</h3><p class="hint">助理要执行 <code>${esc(confirmation.action)}</code>，参数如下。核对后再决定。</p><pre>${esc(JSON.stringify(confirmation.input, null, 2))}</pre>${confirmation.description ? `<details><summary>能力说明</summary><p>${esc(confirmation.description)}</p></details>` : ""}<div class="form-footer"><button class="button" type="button" data-approve="false">不要</button><button type="button" class="danger" data-approve="true">执行</button></div></section>`;
    query("#assistant-send").disabled = true;
    query("#assistant-confirm").querySelectorAll("[data-approve]").forEach((button) => { button.onclick = guard(async () => {
      if (!busy) await request("/confirm", { conversation_id: selected, call_id: callID, approve: button.dataset.approve === "true" });
    }); });
  };
  const load = async () => {
    const id = selected, current = attachment;
    const conversation = id ? await api(`/admin/assistant/conversations/${encodeURIComponent(id)}`) : null;
    if (!alive || current !== attachment || id !== selected) return;
    const messages = conversation?.messages || [];
    const shown = conversation?.running ? messages.filter((message) => message.id <= conversation.run_after) : messages;
    box.innerHTML = conversation ? messagesHTML(shown) : `<div class="assistant-empty"><h2>今天有什么要处理的？</h2><p>${config.url ? "可以先问问即将到期的资产，或看看节点有没有异常。" : "打开助理设置，填写你自己的网关地址和模型后就能开始。"}</p></div>`;
    query("#assistant-title").textContent = conversation?.title || "新对话";
    query("#assistant-rename").hidden = query("#assistant-delete").hidden = !id;
    query("#assistant-confirm").innerHTML = "";
    status.textContent = "";
    setBusy(false);
    if (conversation?.pending) showConfirmation(conversation.call_id, conversation.pending);
    box.scrollTop = box.scrollHeight;
    sidebar((await api("/admin/assistant/conversations")).conversations);
    // 不等这次连接结束：页面加载要立即完成，流式内容在后面继续进来。
    if (conversation?.running && alive && current === attachment) attach(`/conversations/${encodeURIComponent(id)}/live`, "GET").catch(() => {});
  };
  const attach = async (path, method, payload) => {
    controller = new AbortController();
    const current = ++attachment, signal = controller.signal;
    setBusy(true);
    status.textContent = "助理正在处理…";
    query("#assistant-confirm").innerHTML = "";
    let liveMessage, prose, liveText = "", failed = false, stopped = false;
    const onEvent = (event, value) => {
      if (event === "conversation" && selected !== value.id) {
        selected = value.id;
        query("#assistant-rename").hidden = query("#assistant-delete").hidden = false;
        api("/admin/assistant/conversations").then((list) => { if (alive) sidebar(list.conversations); }).catch(() => {});
      }
      if (event === "user") {
        query(".assistant-empty")?.remove();
        if (query("#assistant-title").textContent === "新对话") query("#assistant-title").textContent = value.content.slice(0, 80);
        box.insertAdjacentHTML("beforeend", messagesHTML([{ role: "user", content: value.content }]));
        liveMessage = null; liveText = "";
      }
      if (event === "text") {
        if (!liveMessage) {
          liveMessage = document.createElement("article");
          liveMessage.className = "assistant-message from-assistant";
          liveMessage.innerHTML = '<strong class="assistant-speaker">助理</strong><div class="assistant-prose"></div>';
          box.append(liveMessage);
          prose = liveMessage.querySelector(".assistant-prose");
        }
        liveText += value.text;
        prose.innerHTML = assistantMarkdown(liveText);
      }
      if (event === "tool_call") {
        box.insertAdjacentHTML("beforeend", toolHTML(value));
        liveMessage = null; liveText = "";
      }
      if (event === "tool_result") {
        const details = [...box.querySelectorAll("[data-call]")].find((entry) => entry.dataset.call === value.call_id);
        if (details) { const pre = document.createElement("pre"); pre.textContent = value.content; details.append(pre); }
      }
      if (event === "confirm") showConfirmation(value.call_id, value.confirmation);
      if (event === "error") { status.textContent = value.error; failed = true; }
      box.scrollTop = box.scrollHeight;
    };
    query("#assistant-stop").onclick = guard(async () => {
      stopped = true;
      status.textContent = "正在停止…";
      if (selected) await send(`/admin/assistant/conversations/${encodeURIComponent(selected)}/stop`, "POST");
    });
    try {
      const response = await fetch("/api/v1/admin/assistant" + path, { method, credentials: "same-origin", headers: payload ? { "Content-Type": "application/json" } : {}, body: payload ? JSON.stringify(payload) : undefined, signal });
      if (response.status === 204) return;
      if (!response.ok) throw new Error((await response.json().catch(() => ({}))).error || "请求失败，请重新登录后重试");
      if (!response.body) throw new Error("浏览器不支持流式响应");
      const reader = response.body.getReader(), decoder = new TextDecoder();
      let buffer = "", terminal = false;
      try {
        while (true) {
          const { value, done } = await reader.read();
          buffer += decoder.decode(value, { stream: !done }).replace(/\r\n/g, "\n");
          let position;
          while ((position = buffer.indexOf("\n\n")) >= 0) {
            const block = buffer.slice(0, position);
            buffer = buffer.slice(position + 2);
            const event = block.match(/^event: (.+)$/m)?.[1];
            const json = block.match(/^data: (.+)$/m)?.[1];
            if (event && json && current === attachment) {
              if (["done", "confirm", "error"].includes(event)) terminal = true;
              onEvent(event, JSON.parse(json));
            }
          }
          if (buffer.length > 256000) throw new Error("响应过长，请刷新会话");
          if (done) break;
        }
        if (!terminal) throw new Error("连接中断，助理仍可能在后台处理；稍后打开这个会话查看结果");
      } finally { reader.releaseLock(); }
      if (!failed) status.textContent = "";
      if (stopped && failed) status.textContent = "已停止；已执行的操作不会回滚。";
    } catch (error) {
      if (alive && current === attachment && error.name !== "AbortError") status.textContent = error.message;
    } finally {
      if (alive && current === attachment) {
        const message = status.textContent;
        controller = null;
        setBusy(false);
        if (selected) await load();
        if (message && !status.textContent) status.textContent = message;
      }
    }
  };
  const request = (path, payload) => attach(path, "POST", payload);
  query("#assistant-settings").onclick = () => { state.settingsView = "assistant"; go("settings"); };
  query("#assistant-new").onclick = guard(async () => { detach(); selected = ""; await load(); input.focus(); });
  query("#assistant-rename").onclick = () => {
    const dialog = modal("修改会话名称", `<form><label>名称<input name="title" maxlength="80" required value="${esc(query("#assistant-title").textContent)}"></label><div class="form-footer"><button type="submit" class="primary">保存</button></div></form>`);
    dialog.querySelector("form").onsubmit = guard(async (event) => {
      event.preventDefault();
      await send(`/admin/assistant/conversations/${encodeURIComponent(selected)}`, "PATCH", { title: event.target.elements.title.value });
      dialog.close();
      await load();
    });
  };
  query("#assistant-delete").onclick = () => confirmAction("删除这个会话？", "删除后不能恢复，已经执行的操作不受影响。", async () => { await send(`/admin/assistant/conversations/${encodeURIComponent(selected)}`, "DELETE"); selected = ""; await load(); });
  query("#assistant-form").onsubmit = guard(async (event) => {
    event.preventDefault();
    const message = input.value.trim();
    if (!message || busy) return;
    input.value = "";
    await request("/chat", { conversation_id: selected, message });
  });
  input.onkeydown = (event) => {
    if (event.key === "Enter" && (event.ctrlKey || event.metaKey) && !event.isComposing) {
      event.preventDefault();
      query("#assistant-form").requestSubmit();
    }
  };
  sidebar(data.conversations);
  if (selected && !data.conversations.some((conversation) => conversation.id === selected)) selected = "";
  await load();
  if (state.assistantPrompt) { input.value = state.assistantPrompt; state.assistantPrompt = ""; input.focus(); }
}

export async function assistantSettingsView() {
  const config = await api("/admin/assistant/settings");
  return `<section data-part="panel" class="panel"><div class="panel-head"><h3>助理</h3></div><form id="assistant-settings-form" class="panel-body">
    <div class="form-grid"><label>协议<select name="protocol"><option value="openai" ${config.protocol === "openai" ? "selected" : ""}>OpenAI 兼容</option><option value="anthropic" ${config.protocol === "anthropic" ? "selected" : ""}>Anthropic 兼容</option></select></label><label>网关地址<input name="url" type="url" required value="${esc(config.url)}" placeholder="http://127.0.0.1:3425/v1"></label><label>模型<input name="model" required maxlength="200" value="${esc(config.model)}" placeholder="填写网关中的模型名"></label><label>网关密钥<input name="key" type="password" autocomplete="new-password" placeholder="${config.has_key ? "已保存，留空不改" : "可选"}"></label></div>
    <label class="checkbox"><input name="clear_key" type="checkbox">清除已保存的网关密钥</label>
    <label>附加提示词（可选）<textarea name="prompt" rows="4" maxlength="8000">${esc(config.prompt)}</textarea></label>
    <p class="hint">密钥加密保存在保险箱里，保存后不能读回。对话及工具结果会发送给你配置的网关；会话记录保存在本机。</p><div class="form-footer"><button type="submit" class="primary">保存助理设置</button></div>
  </form></section>`;
}

export function wireAssistantSettings(refresh) {
  const form = content().querySelector("#assistant-settings-form");
  form.onsubmit = guard(async (event) => {
    event.preventDefault();
    const fields = form.elements;
    const config = { protocol: fields.protocol.value, url: fields.url.value.trim(), model: fields.model.value.trim(), prompt: fields.prompt.value };
    if (fields.clear_key.checked) config.key = "";
    else if (fields.key.value) config.key = fields.key.value;
    await send("/admin/assistant/settings", "PUT", config);
    toast("助理设置已保存");
    await refresh();
  });
}
