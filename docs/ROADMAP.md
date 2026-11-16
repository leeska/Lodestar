# Lodestar 后续方案（交接用）

这份文档写给接手的人或 agent：现在做到哪了、项目怎么组织、要守哪些规矩、接下来按什么顺序做什么、每一步做到什么算完。
目标本身见 [VISION.md](VISION.md)，架构细节见 [ARCHITECTURE.md](ARCHITECTURE.md)，外部 agent 接入见 [MCP.md](MCP.md)，部署见 [DEPLOYMENT.md](DEPLOYMENT.md)。

> 写于 2026-11-15，当时线上和 `main` 都是 v3.2.0（提交 `3aa1174`）。

2026-11-16 已发布 v3.10.0，发布代码 `0583542` 已推送 `origin/main`；当前生产验收见下列 UI 重构记录，原始版本分工继续保留作对照。

同日发布 v3.10.1（提交 `b74434d`）：助理改为后台运行、基础设施写操作需确认、全站界面修整，生产验收见 [UI 重构记录](UI-REFACTOR-2026-11-16.md#v3101-发布)。

实施进度见 [v3.3—v3.9 记录](IMPLEMENTATION-2026-11-15.md) 和 [v3.10.0 UI 重构记录](UI-REFACTOR-2026-11-16.md)：下面保留最初的版本分工与验收标准，已实现和已部署的版本逐项记在实施记录中。

---

## 1. 现状

### 1.1 已完成并上线

| 版本 | 内容 |
| --- | --- |
| v3.0.0 | 从 TZA ControlCenter 彻底改名为 Lodestar（程序、服务、用户、目录、Go 模块 `github.com/leeska/Lodestar`），主控和节点都能自动迁移 |
| v3.1.0 | 能力注册表 `internal/action` + 访问密钥 `internal/apikey`；API `/api/v1/actions`、MCP `/mcp`、OpenAPI、Skill 都从注册表生成；后台「Agent 接入」页 |
| v3.2.0 | 核心服务模块：保险箱 `internal/vault`、时间线 `internal/timeline`、资产 `internal/assets`、定时任务 `internal/jobs`、连接器骨架 `internal/connector`；后台「资产」「时间线」页 |

### 1.2 线上环境

- 主控在 bero：termark 资产 `5FY8MV92f5n5dNUJ`，SSH 端口 22222，Debian 13。面板地址 https://probe.leeska.net，前面是 Cloudflare。
- 服务：`lodestar`（监听 127.0.0.1:25774）、`lodestar-browser`（地区偏移检测用的隔离浏览器）。数据目录 `/var/lib/lodestar`，数据库 `lodestar.db`，保险箱密钥 `vault.key`。
- 节点：2026-11-16 验收为 13 台，均在线，实际 Agent 版本为 v3.9.0，与 v3.10.0 升级前备份一致；配置在 `/etc/lodestar/agent.json`。
- bero 上还有别的 nginx 站点（evan.fan、pi.leeska.net、tza.cc），**不要动**。
- `/root` 只保留最近两份升级前完整备份：`lodestar-before-v3.10.1.tar.zst` 与 `lodestar-before-v3.10.0.tar.zst`。更早的版本备份、`lodestar-premigrate-*.tar.zst` 和旧发布目录已于 2026-11-16 按用户要求删除。备份含签名材料和私人数据，权限 0600，不要外传。以后每次发版后删掉最旧的一份，保持两份。
- 目前**没有异地备份**：后台「备份」模块已经做好，但还没设口令和存放节点，要用户自己设。

### 1.3 刻意没做 / 留下的债

1. **菜单清单接口**（计划里的 `GET /api/v1/admin/manifest`）没做。目前由 `web/admin-navigation.js` 统一维护功能区域与页面元数据，`web/admin-pages.js` 绑定页面函数。等需要按用户开关模块时再考虑服务端清单。2026-11-16 的导航与首页调整见 [UI 重构记录](UI-REFACTOR-2026-11-16.md)。
2. **定时任务没有统一**：只有新模块（资产提醒、时间线清理）走 `internal/jobs`。`backup`、`certs`、`sites`、`users`、`proxy`、`regiondrift` 仍是各自的 `Run(ctx)` 循环（见 `cmd/server/main.go`）。迁移时要保留它们的 `Kick()`（保存后立即触发）语义。
3. **MCP 老工具**：`internal/mcp/tools.go`、`admin.go` 里的 29 个手写工具，通过 `LegacyActions()` 包成能力，旧名字作别名。以后可以逐个改成原生 Handler，但不急。
4. **备份文件头**：备份文件里的魔数仍是 `TZABK1`（协议常量，刻意不改）；扩展名已改为 `.lsbk`。
5. **签名前缀**：`tza-agent-sig-v1`、`tzapkg-sig-v1`、`tza.ipguard-egress/1` 是协议常量，旧 Agent 和旧包靠它们校验，**永远不要改**。
6. 资产的每月合计只在前端算（`web/format.js` 的 `FX_CNY` 固定参考汇率）；后端没有汇率表。要在后端用（比如简报里算钱），把汇率表搬到 `internal/model` 并加测试把两边钉在一起。

---

## 2. 项目怎么组织

### 2.1 目录

本机仓库路径为 `/Users/Apple/code/Lodestar`，Go 模块和远端仓库均为 `github.com/leeska/Lodestar`。本地目录名与线上安装路径分别管理；线上目录按 [DEPLOYMENT.md](DEPLOYMENT.md) 的迁移流程处理。

```
cmd/server        主控：main.go 里 shippedModules() 列出全部模块，顺序就是数据库迁移顺序
cmd/agent         节点 Agent
internal/module   模块接口：Module / Migrator / Router / Actioner / Jobber / Overviewer
internal/action   能力注册表：Action（名字、说明、JSON Schema、风险档、Handler 或 Route）
internal/apikey   访问密钥
internal/httpapi  HTTP 层：鉴权、后台 REST、能力 API、MCP 挂载、OpenAPI、Skill 打包
internal/mcp      MCP 协议实现（工具清单与调用来自注册表）
internal/storage  SQLite、核心迁移、节点/任务/事件/审计
internal/{vault,timeline,assets,jobs,connector}   v3.2.0 的核心服务
internal/{proxy,users,docker,certs,sites,backup,remote,regiondrift}   业务模块
internal/agent    Agent 端各能力（代理、Docker、证书、网站、备份、终端、改名迁移）
web/              后台与公开页，原生 JS，随 Server 嵌入；admin-navigation.js 是导航元数据，admin-pages.js 绑定页面函数
deploy/           install.sh（主控安装/升级/迁移）、systemd 单元、nginx 配置
scripts/pipeline.sh   检查与构建流水线
docs/             文档
```

### 2.2 加一个模块的标准做法

以「情报」模块为例，每个新模块都按这个套路：

1. 新建 `internal/<模块>/`，定义 `Module` 结构体，实现：
   - `ID()`：稳定的模块名，也是 `schema_migrations` 里的键
   - `Migrations()`：建表；版本从 1 开始逐个加，**已经上线的迁移永远不改，只追加**
   - `Routes(r module.Routes)`：后台接口挂在 `r.Admin("METHOD /api/v1/admin/<模块>/...")`。写操作会自动进审计；需要更细的审计用 `r.Audit`
   - `Actions()`：给外部 agent 的能力。优先用 `action.R(...)` 指向自己的后台路由，这样校验和审计都和后台一致；风险分 `Read` / `Write` / `Danger`
   - `Jobs()`：周期任务（`module.Job`）
   - `Overview(ctx)`：总览卡片
2. 在 `cmd/server/main.go` 的 `shippedModules()` 里登记，在 `run()` 的类型 switch 里注入依赖（数据库、保险箱、时间线、事件……）。
3. 在 `internal/httpapi/agentkit.go` 的 `moduleNames` 里加中文名（Skill 和 Agent 接入页用）。
4. 前端：新建 `web/admin-<模块>.js`，在 `web/admin-navigation.js` 的对应功能区域登记页面 id 与名称，在 `web/admin-pages.js` 绑定页面函数。
5. 需要发通知的：用 `RecordEvent(model.Event{NodeID: "<模块>:<id>", Kind: model.EventXxx, ...})`，并在 `internal/model/event.go` 的 `IsNodeEvent` 和 `internal/notify/transport.go` 的 `telegramTitles` 里登记新类型。
6. 需要存密码 / token 的：存进保险箱（`vault.Put(ctx, "<模块>:<id>:<字段>", 值)`），接口只返回「有没有」，查看要单独的 Danger 能力并写审计。
7. 想出现在时间线里的事：`timeline.Emit(ctx, timeline.Entry{Source: "<模块>", ...})`。
8. 写测试（参照 `internal/assets/assets_test.go`、`internal/httpapi/actions_test.go`），更新 `docs/ARCHITECTURE.md`。

### 2.3 必须守的规矩

**和用户沟通**
- 用简体中文回答用户。
- 只有用户明确说了才提交、推送、部署。
- 不向用户要 SSH 密码或私钥：termark 里存着，用 `termark exec <资产 id> "<命令>"`、`termark upload`。
- 备份口令、Cloudflare token 之类由用户自己设，不要让用户发给你，也不要替用户编造。
- 面板数据库里的 Cloudflare token 只能在服务器上读，不能打印出来。
- 不要改内核 sysctl。
- 不要把用户邮箱放进对外请求。
- 不要恢复已泄露的旧签名密钥 `tza-agent-2026`。

**代码**
- 后台的 CSP 禁止内联 `style` 属性和内联脚本：样式要在渲染后通过 `el.style.xxx = …` 设置，或写进 `web/styles.css`。
- 前端里所有插值都要经过 `esc()`。
- 界面和文案用中文，代码注释可中可英，和周围一致即可。
- 访问密钥只能访问能力 API、MCP 和 OpenAPI，不能访问 `/api/v1/admin/*`，不能登录后台。不要给密钥开后门。
- 高风险能力每次调用都必须确认（`confirm`），不要做「记住确认」。
- 新的数据库迁移要能在生产库上原地执行；有迁移的版本，部署前先备份数据目录。

---

## 3. 开发、测试、部署

### 3.1 本地

- Go 1.27，Node（或 Bun）跑前端检查。
- 全量检查：`bash scripts/pipeline.sh check`。包括 gofmt、go vet、go test、前端语法与测试、一次性 Server 上的 ui-smoke。必须全部通过。
- 只跑 Go：`go test ./...`。
- 本地预览：用 `go build -o <目录>/lodestar ./cmd/server` 编译，再执行 `lodestar init --data <目录>/data`，然后用 `lodestar --listen 127.0.0.1:25790 --data <目录>/data` 启动。
  - Web 资源是嵌进二进制的，改了前端也要重新编译。
- 改完界面要在浏览器里看桌面（1440×900）和手机（375×812）两个尺寸，手机上不能有横向滚动。

### 3.2 构建发布包

```bash
OUT=<产物目录> VERSION=vX.Y.Z LODESTAR_AGENT_KEY=$HOME/.config/tza-probe/agent-signing/tza-agent-20261109/agent.key bash scripts/pipeline.sh build
```

- 产物是 `lodestar-linux-amd64.tar.gz`、`.sha256` 等。发布包里内嵌签好名的两种架构 Agent，后台可以一键升级节点。
- 签名私钥在本机 `~/.config/tza-probe/agent-signing/`，**不要上传到任何地方**。
- 发版前把 `internal/model/model.go` 的 `Version` 和 `docs/DEPLOYMENT.md` 里「目前 `X-dev`」改成下一个版本。

### 3.3 部署到 bero

照着 v3.2.0 的做法：

1. 先在 bero 上建目录：`termark exec 5FY8MV92f5n5dNUJ "mkdir -p /root/lodestar-vX && chmod 700 /root/lodestar-vX"`。
2. 用 `termark upload` 上传发布包、`.sha256` 和一个 `run.sh`。`run.sh` 依次做这些事：
   1. 用 `sha256sum -c` 校验发布包；
   2. 解压到 `pkg/`；
   3. 用 `./pkg/lodestar check-data --data /var/lib/lodestar` 确认数据库能升级；
   4. 有迁移的版本，先停服务，再执行 `tar -C /var/lib --zstd -cf /root/lodestar-before-vX.tar.zst lodestar`，权限设成 0600；
   5. 用 `sh pkg/deploy/install.sh --package /root/lodestar-vX/lodestar-linux-amd64.tar.gz` 安装；
   6. 把 `EXIT=$?` 和 `DONE` 写进 `install.log`。
3. 用 `nohup sh run.sh &` 在后台运行，然后轮询 `install.log` 里有没有 `DONE`。
4. 验证以下几项：
   - `systemctl is-active lodestar lodestar-browser nginx`
   - `curl 127.0.0.1:25774/healthz` 返回新版本号
   - `https://probe.leeska.net/healthz` 返回 200
   - 用 python 只读打开 `/var/lib/lodestar/lodestar.db`，确认 13 台节点的 `last_seen` 都在 60 秒内，`schema_migrations` 里有新模块
   - `journalctl -u lodestar` 里没有新的 error 或 warn
5. 删掉上一个版本的 `/root/lodestar-v*` 目录和 `pkg/`。
6. 节点 Agent 有改动时：
   - 用数据目录里的 `admin-token` 调 `POST /api/v1/admin/agents/upgrade {"ids":[…]}`；
   - 先升一台试，没问题再升其余的；
   - 节点版本看 `/api/v1/admin/nodes` 返回的 `metrics.version`。

**注意**：
- bero 上没有 sqlite3 命令行，查库用 python。
- 不要用 `sleep 60` 之类的长等待去串远程检查，改成短间隔轮询。

---

## 4. 接下来做什么（按顺序）

每一步都单独发版：各自可以上线、可以回滚，并且带着测试和文档。版本号按顺序往上加。

### 4.1 v3.3.0　网页端助理（走 magpie 网关）

**目标**：在后台直接用中文对话，助理能用的工具就是能力注册表里的全部能力，权限和用户本人一样。

**设置**
- 新模块 `assistant`，在后台「面板设置」里加一个「助理」区块，填这些：
  - 网关地址：magpie 网关，OpenAI 兼容的 `/v1/chat/completions`。也支持 Anthropic 的 `/v1/messages`，用户选其中一种。
  - 网关密钥：存进保险箱，键名 `assistant:gateway:key`。
  - 模型名。
  - 系统提示词的附加部分（可选）。
- 设置能读不能读出密钥；改密钥是 Write，查看密钥不提供。

**后端**
- `POST /api/v1/admin/assistant/chat`：请求体 `{conversation_id?, message}`，用 SSE 流式返回。
- 流程：
  1. 组 messages，包括系统提示、历史和这一条。
  2. 把 `actions.For(owner)` 转成 tools（名字用 `ToolName()`，参数用 `Input` 这份 schema），调用模型。
  3. 模型要调工具时：
     - Read / Write 能力直接执行（`a.runAction`，调用者是 `action.Owner(用户名)`）；
     - Danger 能力**不执行**，向前端推一个 `confirm` 事件（内容来自 `action.Confirmation`），对话暂停。
  4. 用户在确认卡片上点同意后，前端调 `POST /api/v1/admin/assistant/confirm {conversation_id, call_id, approve}`。后端带上 `confirm=true` 执行，把结果作为 tool 消息继续对话；用户拒绝时，就把「用户拒绝」作为 tool 结果交回模型。
  5. 每轮最多 10 次工具调用。超时、出错要回给前端，不能卡住。
- 审计：工具执行照常进审计，操作者记为 `assistant:<用户名>`，可以在 `Actor.Name` 上区分。
- 存储：新表 `assistant_conversations(id, title, created_at, updated_at)` 和 `assistant_messages(id, conversation_id, role, content JSON, at)`。
  - 能列出、改名、删除会话。
  - 工具结果太大时截断后再存，比如每条最多 32 KB。
- 能力：`assistant.ask`（Write，外部 agent 也能用它问一句话）。会话的读和删可以先不做成能力。

**前端**
- 新增 `web/admin-assistant.js` 页面「助理」，放在「概览」分组里：
  - 左边是会话列表，右边是对话；
  - 消息用 Markdown 渲染，要做一个安全的小渲染器，不引入 CDN 和内联 HTML，所有输出都要转义；
  - 工具调用折叠显示，标出名字、参数和结果摘要；
  - Danger 调用显示成确认卡片：标题和说明来自 Confirmation，配「执行」「不要」两个按钮。
- 总览页右上角加一个「问助理」入口，直接带上「现在有什么要处理的」。

**测试**
- 用 `httptest` 起一个假的 OpenAI 兼容服务器，验证：
  - 工具调用能连续进行好几轮；
  - Danger 会暂停，确认后执行，拒绝后不执行；
  - 密钥不会出现在任何响应和日志里；
  - SSE 流在中途断开时，后端不会泄漏 goroutine。

**完成标准**：在后台问「下个月有哪些东西到期？顺便把 leeska.net 的到期日改成 2027-11-26」，助理先调 `assets.upcoming`，再调 `assets.update` 把日期改好；问「删掉那个测试资产」时会弹确认卡片。

### 4.2 v3.4.0　每日简报 + 推送

**目标**：每天早上自动生成一份简报，从已有的通知渠道（Telegram 等）发出去，同时写进时间线。

- 新模块 `brief`，用定时任务 `brief.daily`：每小时检查一次，到了用户设定的时间（本地时区，默认 08:00）且当天还没发过，就生成并发送。
- 简报的数据，先由程序直接拼好，不依赖模型：
  - 总览卡片里 warn / bad 的条目；
  - `assets.upcoming(30)`；
  - 过去 24 小时的时间线里 bad / warn 的条目；
  - 节点在线情况；
  - 定时任务里失败的。
- 配了助理模型时，再让模型把上面的数据改写成一段口语化的摘要，模型失败就退回纯数据版。
- 发送：给 `internal/notify` 加一种「简报」消息类型（不是告警事件，不走告警节流），复用用户选好的渠道；同时写一条时间线 `Source: "brief"`，正文就是完整简报。
- 后台：「面板设置」里加简报区块，可以设开关、时间和包含哪些部分，并有「立即发送一份」按钮。
- 能力：`brief.preview`（Read）、`brief.send_now`（Write）。
- 测试：用假时钟验证每天只发一次、跨时区、模型失败时退回。

### 4.3 v3.5.0　情报中心（第一个连接器）

**目标**：持续收集和用户相关的外部信息。命中规则的立即推送，其余的进简报。

- 在 `internal/connector` 上实现几种连接器：
  1. `rss`：RSS / Atom，用标准库解析 XML，不引第三方库。
  2. `github_release`：调 `https://api.github.com/repos/<owner>/<repo>/releases`，token 可选，存进保险箱。
  3. `webpage`：抓一个网页，用 CSS 选择器或正则取出一段内容，内容变了才产出条目。只允许 http / https，要防 SSRF：拒绝内网地址，或者复用现有代码里对内网地址的检查。
  4. （可选）`price`：页面上的价格，或补货状态（有货 / 无货）。
- 新模块 `intel`：
  - 表：`intel_sources(id, connector, name, config JSON, enabled, every_minutes, last_fetch, last_error)`、`intel_items(id, source_id, key UNIQUE(source_id,key), title, url, at, body, read, starred)`、`intel_rules(id, name, keywords, sources, action: notify|brief|ignore)`。
  - 定时任务 `intel.fetch` 每分钟检查一次，哪些源到点了就抓。用 ETag / Last-Modified 节省流量；单个源出错只记在源上，不影响其他源。
  - 规则命中 `notify` 就发事件（新增事件类型 `intel`），同时写时间线。
  - 条目保留 90 天，加星标的不删。
- 后台「情报」页：源管理、条目流（已读 / 加星 / 按源筛选）、规则管理。
- 能力：`intel.sources`、`intel.items`（按关键词和时间筛选）、`intel.add_source`、`intel.update_source`、`intel.delete_source`（Danger）、`intel.mark`。
- 简报里加「情报」一节：过去 24 小时、没有命中立即推送规则的条目。

### 4.4 v3.6.0　日程与待办

- 新模块 `todo`：
  - 表：`todos(id, title, note, due_at, done_at, priority, ref, source, created_at)`，其中 `ref` 指向来源对象，比如 `asset:<id>`、`event:<id>`、`intel:<id>`。
  - 资产到期、告警、情报条目旁边加「转成待办」按钮，也对应一个能力。
  - 定时任务：到期前提醒。
- 日历：用连接器读 iCal（Apple / Google 日历的私有 ics 地址，地址存进保险箱），只读同步到 `calendar_events` 表。
- 简报加「今天」一节：今天的日程和到期的待办。
- 能力：`todo.list/create/update/complete/delete`、`calendar.today`、`calendar.range`。

### 4.5 v3.7.0　财务（只记账）

- 新模块 `finance`：
  - 表：账户 `accounts(id, name, kind, currency)`、流水 `transactions(id, account_id, at, amount, currency, category, note, ref)`、预算 `budgets(category, period, amount)`。
  - 资产和节点的周期费用可以一键生成「每月固定支出」视图（只读汇总，不自动记账）。
  - 支持导入 CSV，列可以自己映射。
- 净资产曲线：每天快照一次账户余额。余额由用户填，或者由流水推算。
- **永远不接**任何交易、转账、付款的接口。
- 后端需要汇率表时，把 `FX_CNY` 搬到 Go（见 1.3 第 6 条）。

### 4.6 v3.8.0　自动化规则

- 新模块 `automation`。每条规则由三部分组成：
  - **触发**：时间线新条目（按来源、类型、级别过滤）、定时（cron）、Webhook（`POST /api/v1/hooks/<随机 token>`）。
  - **条件**：对条目字段做简单的比较。
  - **动作**：调用一个能力，参数可以写模板，比如引用触发条目里的字段。
- 动作的风险：Read / Write 能力可以自动执行；Danger 能力不能自动执行，只能生成一个待确认项，推送给用户后由用户在后台确认。
- 每次执行都进时间线和审计，操作者记为 `automation:<规则名>`。
- 防循环：自动化自己产生的时间线条目不再触发自动化；每条规则限制执行频率。

### 4.7 v3.9.0　数字身份与安全

- 账号清单直接复用资产的 `account` 类型，再加一个视图：2FA 是否开启、恢复方式放在哪里、最后检查时间。
- 密码泄露检查：用 HIBP 的 k-anonymity 接口（只发 SHA-1 的前 5 位），定时任务每周跑一次。泄露的发事件，原文不出保险箱。
- 域名和证书风险汇总：快到期的、DNS 异常的。
- 登录异常：后台登录失败、新设备登录，在现有审计的基础上做提醒。

### 4.8 入口扩展（可以穿插着做）

- **PWA**：加 manifest 和 service worker，后台可以装到手机桌面。只缓存静态资源，API 不缓存。
- **Telegram 双向对话**：把 Telegram Bot 的消息转给助理（4.1），只认用户自己的 chat id；Danger 能力用 Telegram 的按钮确认。
- **iPhone 快捷指令**：提供一个「问一句」接口 `POST /api/v1/actions/assistant.ask`，用一把只读或可写的访问密钥。在文档里给出快捷指令的配置方法。

### 4.9 技术债（找空做）

1. 把 `backup`、`certs`、`sites`、`users`、`proxy`、`regiondrift` 的 `Run` 循环迁到 `internal/jobs`，保留 `Kick()` 立即触发的能力。可以给 `jobs.Module` 加 `Trigger(name)`，或者让模块自己持有一个 kick 通道。
2. 把 MCP 的老手写工具逐个改成原生能力，最后删掉 `internal/mcp/tools.go` 和 `admin.go` 里的实现。
3. 等模块可以按用户开关的时候，再做菜单清单接口 `GET /api/v1/admin/manifest`。
4. 「Agent 接入」页的能力清单现在一次列出 100 多项，模块再多时可以改成按模块折叠。
5. 给 ui-smoke 补上「资产」「时间线」「Agent 接入」三个新页面的冒烟检查。

---

## 5. 每次发版的检查清单

- [ ] `bash scripts/pipeline.sh check` 通过
- [ ] 新模块有单元测试；新能力在 `internal/httpapi/actions_test.go` 的覆盖范围内（OpenAPI、Skill 会自动包含新能力）
- [ ] 浏览器里看过桌面和手机两个尺寸
- [ ] 更新了 `docs/ARCHITECTURE.md`，必要时更新 `README.md`、`docs/MCP.md`
- [ ] 版本号改好（`model.go`、`DEPLOYMENT.md`）
- [ ] 用户同意后再提交、推送、部署；有迁移的先备份数据目录
- [ ] 部署后验证服务、版本、HTTPS、13 台节点在线、日志
