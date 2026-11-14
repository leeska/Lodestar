import { esc } from "./api.js";

// 站点品牌：后台「面板设置」里改的站点名称与 logo。首屏值由 /theme-default.js 写在
// window.__lodestarBrand 上（theme-boot.js 已据此换好标签页标题与 favicon），这里只负责拼品牌区。
// 两项都空就是内置的 Lodestar 品牌：四角星标记 + Lodestar 字样。
export const DEFAULT_NAME = "Lodestar";
// 指路星：四角星，颜色跟随 .brand-mark 的字色。
export const MARK = `<span class="brand-mark"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 1.5 14.1 9.9 22.5 12 14.1 14.1 12 22.5 9.9 14.1 1.5 12 9.9 9.9Z"/></svg></span>`;

export function currentBrand() {
  const b = window.__lodestarBrand || {};
  return { name: String(b.name || "").trim(), logo: String(b.logo || "") };
}

export function brandHTML() {
  const { name, logo } = currentBrand();
  const mark = logo ? `<img class="brand-logo" src="${esc(logo)}" alt="">` : MARK;
  const text = name ? `<span class="brand-name">${esc(name)}<small>服务器管理中心</small></span>` : `<span class="brand-default">Lodestar</span>`;
  return `<a class="brand" href="/" aria-label="${esc(name || DEFAULT_NAME)} 首页">${mark}${text}</a>`;
}

// 后台保存之后当场换上：标签页标题、favicon 与页面上已有的品牌区一起换，不用刷新。
// 与 theme-boot.js 的首屏逻辑保持一致。
export function applyBrand(next) {
  window.__lodestarBrand = { ...currentBrand(), ...next };
  const { name, logo } = currentBrand();
  document.title = name || DEFAULT_NAME;
  const icon = document.querySelector('link[rel="icon"]');
  if (icon) {
    icon.href = logo || "/favicon.svg";
    if (logo) icon.removeAttribute("type");
    else icon.type = "image/svg+xml";
  }
  for (const el of document.querySelectorAll("a.brand")) el.outerHTML = brandHTML();
}
