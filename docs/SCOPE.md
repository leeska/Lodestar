# 项目范围

Lodestar 是自托管的个人中枢，从服务器管理起步，逐步统一资产、事件与日常事务。部署仍保持一个仓库、Server / Agent 两个程序、一个 SQLite 数据库，组件通过明确接口协作。

本页记录当前已实现的范围；长期目标见 [VISION.md](VISION.md)，开发顺序和待办见 [ROADMAP.md](ROADMAP.md)。网页端助理、每日简报、情报、日程待办、财务账本、自动化及数字身份安全已实现。使用需先配置对应来源或数据，对外 HIBP 与 DNS 查询默认关闭；验收与部署记录见 [IMPLEMENTATION-2026-11-15.md](IMPLEMENTATION-2026-11-15.md)。

## 功能与代码位置

| 功能 | 界面入口 | 实现位置 |
|---|---|---|
| 情报来源、条目、关键词规则与简报汇总 | 情报 | internal/intel、internal/safefetch、web/admin-intel.js |
| 待办、来源转换、截止提醒、私有 iCal 与今天简报 | 日程与待办 | internal/todo、internal/calendar、web/admin-todo.js |
| 本地账户、流水、CSV、预算、固定费用和日快照 | 财务 | internal/finance、internal/model/fx.go、web/admin-finance.js |
| 时间线 / cron / Webhook、条件模板、逐次危险确认 | 自动化 | internal/automation、web/admin-automation.js |
| 账号保护、HIBP 前缀查询、域名证书风险、登录提醒 | 数字身份与安全 | internal/security、web/admin-security.js |
| 每日简报、预览与通知投递 | 面板设置 → 简报 | internal/brief、web/admin-brief.js |
| 网页端助理、工具调用与危险操作逐次确认 | 助理；面板设置 → 助理 | internal/assistant、web/admin-assistant.js |
| 资产账本、敏感字段与到期提醒 | 资产；总览的即将到期项目 | internal/assets、internal/vault、web/admin-assets.js |
| 合并告警、审计与模块事件的时间线；定时任务状态与立即运行 | 时间线 | internal/timeline、internal/jobs、web/admin-timeline.js |
| 主机状态、资源、网络速率 | 公开卡片与节点详情 | agent/collector.go、web/app.js、web/detail.js |
| 内置目标容易查找 | 网络任务表单的搜索、地区/运营商/协议筛选与多选 | web/task-editor.js、internal/catalog |
| 单任务组合延迟与回程、IPv4 与 IPv6 | 表单可同时选择；Agent 按单项调度 | model.Task.Leaves、storage.Lease |
| 海外目标（“其他”运营商） | 自定义目标运营商选项 | web/task-editor.js、model.Task.Validate |
| 卡片只显示指定地区，详情看全部；延迟与回程的地区可分别指定 | 节点编辑的卡片回程地区（display.route_region）/ 卡片延迟地区 | web/admin-nodes.js、web/app.js、web/detail.js |
| IPv4 / IPv6 分开展示 | 公开页与详情页协议切换 | web/app.js、web/detail.js |
| 周期流量、额度、价格、到期时间 | 节点编辑、公开卡片 | model.Cycle、storage/billing.go、web/admin-nodes.js |
| 续费、流量、降速、负载、离线提醒 | 告警与通知 → 提醒规则 | internal/notify、web/admin-alerts.js |
| Telegram / Webhook / 通知插件 | 告警与通知 → 配置出口 | notify/transport.go、notify/plugin.go |
| 公开页与后台，克制的玻璃效果与多套外观 | 公开页、后台 | web/styles.css、themes/glass/glass.css |
| 数据库在线备份下载 | 面板设置 → 数据 → 数据备份 | storage/backup.go |
| 外部 agent 按权限读写；统一的能力 API、MCP、OpenAPI 与 Skill | Agent 接入；`/api/v1/actions`、`/mcp` | internal/action、internal/apikey、internal/mcp、internal/httpapi、web/admin-agents.js |
| 远程命令与交互终端，节点须显式开启管理能力 | 远程管理 | internal/remote、internal/channel、internal/agent、web/admin-remote.js |
| 代理入站、使用者配额与订阅、直接转发与隧道 | 使用者、代理、转发 | internal/users、internal/proxy、internal/agent |
| Docker 容器、镜像与 Compose 管理 | Docker | internal/docker、internal/agent/docker.go、web/admin-docker.js |
| Let's Encrypt DNS-01 证书、续期与下发；nginx 网站与反向代理 | 证书、网站 | internal/certs、internal/sites、internal/agent |
| 口令加密的定时备份到节点与恢复 | 备份 | internal/backup、internal/agent/backup.go、cmd/server/restore.go |
| 主题包 / 附加包：本地上传安装；仅内置现代极简（默认），液态玻璃 / 复古印刷 / 二次元是 themes/ 下的官方主题包；每个主题可声明设置菜单 | 主题（已安装） | pkg/lspkg、internal/theme、themes/、tools/themepack、web/admin-themes.js、web/admin-packages.js |
| 声明式插件：通知渠道、探测目标集（上传安装，装前确认权限） | 插件（已安装）；配置出口；任务目录 | pkg/lspkg（plugin.go、tpl.go）、internal/plugin、notify/plugin.go、service/plugins.go |
| 组件解耦 | 依赖接口和版本化 HTTP 协议 | docs/ARCHITECTURE.md |

配套能力：历史图、Agent 接入配置和一键安装脚本、当前周期用量校正、通知状态和失败原因，以及任务暂停/恢复、复制、批量绑定、立即安排执行和最近执行记录。

## 关键约定

- 空节点绑定不执行，前端不会执行探测命令。
- TCP 连接失败率不等同于 ICMP 丢包率；负载提醒是 CPU 或内存使用率。
- 回程追踪核心内置在 Agent（NTrace-core，GPL-3.0，见 NOTICE），节点不需要安装外部二进制；跳的 ASN/地区默认取 `IPAPI.com`，可用 `--geo-provider` 显式指定或 `disable-geoip` 关闭。
- 未检测、过期或失败的回程不编造线路；标签按实际 ASN 与跳地址前缀命名（163 / CN2GIA / CN2GT / 4837 / 9929 / 10099 / CMIN2 / CMI / CTG），ASN 优先、前缀兜底，表外前缀保持未知，不写“精品回程”这类营销词。同一运营商的多条线路收敛成一个标签（电信按 CTG > CN2 > 163，移动按 CMIN2 > CMI）；联通轨迹里出现过 9929 时不再另记 4837（那是这条国际线的落地侧），没有 9929 时 4837 仍是联通线路名。ASN 数据源没解析出来的跳，Agent 会按注册库核对过的运营商自营网段（真实地址匹配，再脱敏）兜底，避免数据源失灵时线路名退回未知。
- 通知出口一次选择一个渠道（Telegram、Webhook 或一个通知插件），不引入复杂多渠道路由。
- 固定保留主机历史 30 天、网络结果 7 天、账本 400 天、事件 90 天。
- 外部 agent 的访问密钥按风险档、模块与有效期授权，只能访问能力 API、MCP 和 OpenAPI，不能登录后台或直接访问后台管理接口；高风险能力每次调用都要显式确认。
- 数据库按模块追加迁移，支持从 v2.0.0 基线及后续版本升级；涉及迁移的部署须先备份，回滚时同时恢复升级前数据，详见 [数据兼容](FRESH-DEPLOYMENT.md)。
- 资产敏感字段使用本地 `vault.key` 加密；主口令解锁尚未实现。统一定时任务当前只覆盖新模块，旧业务模块仍各自运行调度循环。

## 不做的事

不做交易或转账、多人协作平台、自训练模型。使用者模块只提供代理与转发相关的配额、订阅和用量访问，不代表多人共享后台管理权限；面板仍由一个主人管理。后续新增能力按个人中枢的目标排序。

Agent 的「一键升级」由管理员触发，只接受运维者签过名、比当前版本新的程序；签名私钥不在 Server 上。这条升级通道不能用于任意程序下发，和节点显式开启的远程管理通道是两套能力，见 [DEPLOYMENT.md](DEPLOYMENT.md)。

包的边界：Lodestar 不连接市场，主题与插件只通过上传 zip 安装。插件只有声明式的两类——通知渠道（一个 HTTP 请求模板，只能发往声明的域名）和探测目标集（只往任务目录里加候选目标）——格式本身限定了它们能做的事，见 [PLUGINS.md](PLUGINS.md)。不提供可执行代码插件、Agent 端插件或插件命令通道；独立前台主题的脚本在隔离环境中运行，不能访问后台管理接口，见 [FRONTENDS.md](FRONTENDS.md)。

部署、升级和恢复以 [DEPLOYMENT.md](DEPLOYMENT.md) 为准；[VERIFICATION.md](VERIFICATION.md) 说明验证方法与只能在真实环境验证的部分。
