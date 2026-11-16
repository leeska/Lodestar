const icon = (paths) => `<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${paths}</svg>`;

export const adminSections = [
  {
    id: "workspace", label: "工作台",
    icon: icon('<rect x="3" y="3" width="14" height="14" rx="3"/><path d="M3 8h14M8 8v9"/>'),
    pages: [
      { id: "overview", label: "今天" },
      { id: "todo", label: "日程与待办" },
      { id: "alerts", label: "告警与事件" },
      { id: "brief", label: "每日简报" },
      { id: "assistant", label: "助理" },
      { id: "timeline", label: "时间线" },
    ],
  },
  {
    id: "intel", label: "情报",
    icon: icon('<path d="M4 3h12v14H4zM7 7h6M7 10h6M7 13h3"/>'),
    pages: [{ id: "intel", label: "信息流与订阅" }],
  },
  {
    id: "assets", label: "资产与财务",
    icon: icon('<path d="m3 6 7-3 7 3v9l-7 3-7-3zM3 6l7 3 7-3M10 9v9"/>'),
    pages: [
      { id: "assets", label: "资产清单" },
      { id: "finance", label: "财务账本" },
      { id: "security", label: "账户安全", title: "账户安全" },
    ],
  },
  {
    id: "infrastructure", label: "基础设施",
    icon: icon('<rect x="3" y="3" width="14" height="6" rx="2"/><rect x="3" y="11" width="14" height="6" rx="2"/><path d="M6 6h.01M6 14h.01M10 6h4M10 14h4"/>'),
    pages: [
      { id: "nodes", label: "节点管理", category: "节点与监控" },
      { id: "tasks", label: "网络探测", category: "节点与监控" },
      { id: "remote", label: "远程管理", category: "节点与监控" },
      { id: "proxy", label: "代理", category: "网络与共享" },
      { id: "forward", label: "转发", category: "网络与共享" },
      { id: "users", label: "共享使用者", title: "共享使用者", category: "网络与共享" },
      { id: "sites", label: "网站", category: "应用与服务" },
      { id: "certs", label: "证书", category: "应用与服务" },
      { id: "docker", label: "容器", title: "容器", category: "应用与服务" },
    ],
  },
  {
    id: "automation", label: "自动化",
    icon: icon('<path d="m11 2-7 9h5l-1 7 8-10h-5z"/>'),
    pages: [{ id: "automation", label: "规则与执行" }],
  },
  {
    id: "system", label: "系统设置",
    icon: icon('<path d="M3 6h14M3 14h14"/><circle cx="7" cy="6" r="2"/><circle cx="13" cy="14" r="2"/>'),
    pages: [
      { id: "settings", label: "偏好与账号" },
      { id: "notifications", label: "通知渠道" },
      { id: "agents", label: "访问密钥与接入", title: "访问密钥与接入" },
      { id: "themes", label: "外观主题" },
      { id: "plugins", label: "插件" },
      { id: "backup", label: "系统备份", title: "系统备份" },
      { id: "audit", label: "审计日志" },
    ],
  },
];

export const navigationPages = adminSections.flatMap((section) =>
  section.pages.map((page) => ({ ...page, section: section.id, group: section.label })),
);

export function routeFromHash(hash) {
  const [id, rawView = ""] = hash.replace(/^#/, "").split("/");
  const page = navigationPages.find((item) => item.id === id);
  let view = "";
  try { view = decodeURIComponent(rawView); } catch {}
  const views = {
    settings: ["look", "account", "assistant", "brief", "data"],
    alerts: ["active", "events", "rules"],
    todo: ["open", "done", "calendar", "sources"],
    intel: ["items", "sources", "rules"],
    finance: ["overview", "accounts", "transactions", "budgets", "recurring", "import"],
    automation: ["rules", "pending", "runs"],
    timeline: ["", "event", "audit", "modules", "brief"],
  };
  return { tab: page?.id || "overview", view: views[id]?.includes(view) ? view : "" };
}
