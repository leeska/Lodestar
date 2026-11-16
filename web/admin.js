import { themeButton } from "./theme.js";
import { skinButton } from "./skins.js";
import { api, send, guard } from "./api.js";
import { state, content, loadNodes, setRenderer, go, restoreRoute, syncRoute } from "./admin-state.js";
import { brandHTML } from "./brand.js";
import { adminPage, navHTML } from "./admin-pages.js";
let navReturnFocus;
export async function mountAdmin(root) {
  setRenderer(render);
  state.container = root;
  restoreRoute();
  window.addEventListener("popstate", () => {
    restoreRoute();
    if (root.querySelector("#admin-content")) guard(render)();
  });
  window.addEventListener("resize", () => {
    if (window.innerWidth > 760) setNavOpen(false);
  });
  try {
    state.serverVersion = (await api("/session")).version || "";
    shell();
    await render();
  } catch (e) {
    if (e.status === 401) login();
    else throw e;
  }
}
function login() {
  state.container.innerHTML = `<div class="login-layout"><section class="login-story">${brandHTML()}<div><div class="eyebrow">SMALL STACK. CLEAR CONTROL.</div><h1>少一点复杂，<br>多一点掌控。</h1><p>监控、代理、转发、Docker、证书与备份。<br>一套属于自己的服务器管理中心。</p></div><div class="login-story-foot"><small>Lodestar CONTROLCENTER / CONTROL PLANE</small>${skinButton()}${themeButton()}</div></section><main class="login-panel"><form id="login"><div class="eyebrow">WELCOME BACK</div><h2>进入管理控制台</h2><p>使用后台用户名与密码登录。</p><label>用户名<input type="text" name="username" required autocomplete="username" autocapitalize="off" spellcheck="false" autofocus></label><label>密码<input type="password" name="password" required autocomplete="current-password"></label><label class="totp-field" hidden>两步验证码<input type="text" name="code" inputmode="numeric" pattern="[0-9 ]{6,7}" maxlength="7" autocomplete="one-time-code" placeholder="身份验证器里的 6 位数"></label><p class="hint">忘记密码时在服务器执行 lodestar reset-password 重置。</p><button class="primary" type="submit">登录控制台 →</button><a href="/">← 返回公开面板</a></form></main></div>`;
  const form = state.container.querySelector("form");
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const button = e.target.querySelector("button[type=submit]");
    button.disabled = true;
    const data = new FormData(e.target);
    const codeField = e.target.querySelector(".totp-field");
    try {
      try {
        await send(
          "/login",
          "POST",
          { username: data.get("username"), password: data.get("password"), code: data.get("code") || "" },
        );
      } catch (err) {
        // 密码正确但开了两步验证：亮出验证码输入框。
        if (err.data?.totp && codeField.hidden) {
          codeField.hidden = false;
          codeField.querySelector("input").required = true;
          codeField.querySelector("input").focus();
        }
        throw err;
      }
      state.serverVersion = (await api("/session")).version || "";
      shell();
      await render();
    } finally {
      button.disabled = false;
    }
  });
}
function shell() {
  document.body.classList.remove("admin-nav-open");
  state.container.innerHTML = `<div class="admin-layout"><button type="button" class="nav-backdrop" aria-label="关闭导航" tabindex="-1" hidden></button><aside data-part="sidebar" class="sidebar" aria-label="主导航">${brandHTML()}<button type="button" id="nav-toggle" class="nav-toggle" aria-expanded="false" aria-controls="admin-nav"><span aria-hidden="true">☰</span><b id="nav-current"></b><span class="nav-toggle-label">导航</span></button><nav id="admin-nav" aria-label="功能区域">${navHTML()}</nav><div class="sidebar-bottom"><button type="button" id="nav-assistant">与助理对话</button><a href="/">公开面板 ↗</a>${skinButton("sidebar-skin")}${themeButton("sidebar-theme")}<button id="logout">退出登录</button><small>Lodestar</small></div></aside><main class="admin-main"><header data-part="admin-top" class="admin-top"><span><span id="breadcrumb-group"></span> <span class="muted">/</span> <b id="breadcrumb"></b></span><span class="admin-top-actions"><button type="button" id="quick-assistant">问助理</button><span class="pill" id="fleet-status" aria-live="polite"></span></span></header><div id="admin-content"></div></main></div>`;
  state.container.querySelectorAll("[data-tab]").forEach(
    (b) =>
      (b.onclick = guard(async () => {
        setNavOpen(false);
        await go(b.dataset.tab);
      })),
  );
  state.container.querySelector(".admin-layout").insertAdjacentHTML("beforeend", `<nav class="mobile-shortcuts" aria-label="常用功能"><button type="button" data-quick-tab="overview">今天</button><button type="button" data-quick-tab="todo">待办</button><button type="button" data-quick-tab="assistant">助理</button><button type="button" id="mobile-more" aria-controls="admin-nav" aria-expanded="false">更多</button></nav>`);
  state.container.querySelectorAll("[data-quick-tab]").forEach((button) => {
    button.onclick = guard(() => go(button.dataset.quickTab));
  });
  state.container.querySelector("#mobile-more").onclick = () => setNavOpen(true);
  state.container.querySelectorAll(".nav-section").forEach((section) => {
    section.addEventListener("toggle", () => {
      if (!section.open) return;
      state.container.querySelectorAll(".nav-section").forEach((other) => {
        if (other !== section) other.open = false;
      });
    });
  });
  state.container.querySelector("#nav-toggle").onclick = () => setNavOpen(!state.container.querySelector(".sidebar").classList.contains("nav-open"));
  state.container.querySelector(".nav-backdrop").onclick = () => setNavOpen(false);
  for (const id of ["quick-assistant", "nav-assistant"]) {
    state.container.querySelector(`#${id}`).onclick = guard(async () => {
      setNavOpen(false);
      await go("assistant");
    });
  }
  state.container.onkeydown = (event) => {
    const side = state.container.querySelector(".sidebar.nav-open");
    if (!side) return;
    if (event.key === "Escape") {
      event.preventDefault();
      setNavOpen(false);
    } else if (event.key === "Tab") {
      const items = [...side.querySelectorAll("button, a, summary")].filter((el) => el.getClientRects().length);
      const first = items[0], last = items.at(-1);
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault(); last?.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault(); first?.focus();
      }
    }
  };
  state.container.querySelector("#logout").onclick = guard(async () => {
    await send("/logout", "POST");
    login();
  });
}
// 手机上导航收起成一个「菜单」按钮（桌面上这个按钮不显示，导航一直展开）。
function setNavOpen(open) {
  const side = state.container.querySelector(".sidebar");
  if (!side) return;
  open = open && window.innerWidth <= 760;
  const wasOpen = side.classList.contains("nav-open");
  if (open && !wasOpen) navReturnFocus = document.activeElement;
  side.classList.toggle("nav-open", open);
  document.body.classList.toggle("admin-nav-open", open);
  state.container.querySelector(".nav-backdrop").hidden = !open;
  state.container.querySelector(".admin-main").inert = open;
  state.container.querySelector(".mobile-shortcuts").inert = open;
  state.container.querySelector("#mobile-more").setAttribute("aria-expanded", String(open));
  if (open) {
    side.setAttribute("role", "dialog");
    side.setAttribute("aria-modal", "true");
  } else {
    side.removeAttribute("role");
    side.removeAttribute("aria-modal");
  }
  side.querySelector("#nav-toggle")?.setAttribute("aria-expanded", String(open));
  side.querySelector("#nav-toggle")?.setAttribute("aria-label", open ? "关闭导航" : `打开导航，当前${adminPage(state.tab).label}`);
  side.querySelector(".nav-toggle-label").textContent = open ? "关闭" : "导航";
  side.querySelector("#nav-toggle > span:first-child").textContent = open ? "×" : "☰";
  if (open) side.querySelector("#nav-toggle")?.focus();
  else if (wasOpen) (navReturnFocus?.isConnected ? navReturnFocus : side.querySelector("#nav-toggle"))?.focus();
}
async function render() {
  state.pageCleanup?.();
  state.pageCleanup = null;
  // 切页时面包屑与侧栏高亮立刻换，内容要等接口回来：中间这半秒把旧内容调暗并标成忙，
  // 不然看起来像点到了「网络探测」却还停在「节点管理」。
  const area = content();
  // 换页 / 重绘会把行连同「更多」一起换掉；挪到 body 下还开着的菜单一并清掉，不留孤儿。
  document.querySelectorAll("body > .row-menu").forEach((m) => m.remove());
  // 节点页的批量工具栏随页面一起换掉；它给 toast 与内容区让出的位置也一并收回。
  document.body.classList.remove("has-batch-bar");
  area?.classList.add("is-loading");
  area?.setAttribute("aria-busy", "true");
  try {
    await renderTab();
  } finally {
    area?.classList.remove("is-loading");
    area?.removeAttribute("aria-busy");
  }
}
async function renderTab() {
  state.container
    .querySelectorAll("[data-tab], [data-quick-tab]")
    .forEach((b) => {
      const on = (b.dataset.tab || b.dataset.quickTab) === state.tab;
      b.classList.toggle("active", on);
      // 高亮只是颜色，读屏读不出「现在在哪一页」。
      if (on) b.setAttribute("aria-current", "page");
      else b.removeAttribute("aria-current");
    });
  const page = adminPage(state.tab);
  state.tab = page.id;
  syncRoute(true);
  state.container.querySelectorAll(".nav-section").forEach((section) => {
    const active = section.dataset.section === page.section;
    section.classList.toggle("is-current", active);
    section.open = active;
  });
  state.container.querySelector("#breadcrumb-group").textContent = page.group;
  state.container.querySelector("#breadcrumb").textContent = page.label;
  const current = state.container.querySelector("#nav-current");
  if (current) current.textContent = page.label;
  const nodesReady = loadNodes().catch(() => {
    const status = state.container.querySelector("#fleet-status");
    if (status) {
      status.className = "pill warn";
      status.textContent = "节点状态读取失败";
    }
  });
  await page.page(nodesReady);
  if (state.tab !== page.id) return;
  if (page.title) {
    const title = content().querySelector(".page-heading h1");
    const text = [...(title?.childNodes || [])].find((node) => node.nodeType === Node.TEXT_NODE);
    if (text) text.textContent = page.title;
  }
  content().querySelectorAll(".segmented").forEach((strip) => {
    if (!strip.hasAttribute("role")) {
      strip.setAttribute("role", "group");
      strip.setAttribute("aria-label", `${page.label}内容`);
    }
    strip.querySelectorAll("button").forEach((button) => {
      if (button.getAttribute("role") !== "tab") button.setAttribute("aria-pressed", String(button.classList.contains("active")));
    });
  });
}
