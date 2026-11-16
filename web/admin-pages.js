import { esc } from "./api.js";
import { adminSections, navigationPages } from "./admin-navigation.js";
import { renderAlerts, notificationsPage } from "./admin-alerts.js";
import { nodesPage } from "./admin-nodes.js";
import { renderPlugins } from "./admin-plugins.js";
import { renderSettings } from "./admin-settings.js";
import { tasksPage } from "./admin-tasks.js";
import { renderThemes } from "./admin-themes.js";
import { remotePage, auditPage } from "./admin-remote.js";
import { overviewPage } from "./admin-overview.js";
import { usersPage } from "./admin-users.js";
import { proxyPage } from "./admin-proxy.js";
import { forwardPage } from "./admin-forward.js";
import { dockerPage } from "./admin-docker.js";
import { certsPage } from "./admin-certs.js";
import { backupPage } from "./admin-backup.js";
import { sitesPage } from "./admin-sites.js";
import { agentsPage } from "./admin-agents.js";
import { assetsPage } from "./admin-assets.js";
import { timelinePage } from "./admin-timeline.js";
import { assistantPage } from "./admin-assistant.js";
import { briefPage } from "./admin-brief.js";
import { intelPage } from "./admin-intel.js";
import { todoPage } from "./admin-todo.js";
import { financePage } from "./admin-finance.js";
import { automationPage } from "./admin-automation.js";
import { securityPage } from "./admin-security.js";

const renderers = {
  overview: overviewPage, assistant: assistantPage, brief: briefPage,
  intel: intelPage, todo: todoPage, timeline: timelinePage,
  assets: assetsPage, finance: financePage, security: securityPage,
  nodes: nodesPage, tasks: tasksPage, alerts: renderAlerts, remote: remotePage,
  users: usersPage, proxy: proxyPage, forward: forwardPage,
  sites: sitesPage, certs: certsPage, docker: dockerPage,
  backup: backupPage, audit: auditPage, agents: agentsPage,
  themes: renderThemes, plugins: renderPlugins, settings: renderSettings,
  notifications: notificationsPage, automation: automationPage,
};

export const adminPages = navigationPages.map((page) => ({ ...page, page: renderers[page.id] }));
export const adminPage = (id) => adminPages.find((page) => page.id === id) || adminPages[0];

export const navHTML = () => adminSections.map((section) => {
  let category = "";
  const children = section.pages.map((page) => {
    const caption = page.category && page.category !== category
      ? `<div class="nav-caption">${esc(page.category)}</div>` : "";
    category = page.category || "";
    return `${caption}<button type="button" data-tab="${esc(page.id)}"><span>${esc(page.label)}</span></button>`;
  }).join("");
  return `<details class="nav-section" data-section="${esc(section.id)}"${section.id === "workspace" ? " open" : ""}><summary>${section.icon}<span>${esc(section.label)}</span><svg class="nav-chevron" viewBox="0 0 16 16" fill="none" stroke="currentColor" aria-hidden="true"><path d="m6 4 4 4-4 4"/></svg></summary><div class="nav-children">${children}</div></details>`;
}).join("");
