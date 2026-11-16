# Lodestar

自托管的个人中枢（目标见 [docs/VISION.md](docs/VISION.md)），从服务器管理起步：**一个仓库、Server / Agent 两个程序、一个 SQLite 数据库**。后台与公开页随 Server 嵌入发布，没有 Node 构建依赖。

v3.0.0 起由 TZA ControlCenter 改名为 Lodestar，旧安装的自动迁移见 [部署文档](docs/DEPLOYMENT.md#从-tza-改名为-lodestarv300)。

当前源码版本见 `internal/model/model.go`，发布版本以对应 tag 和二进制输出为准。

当前使用按模块编号的 SQLite 迁移，支持 v2.0.0（schema 30）及后续数据目录原地升级；
更早的数据需要使用新目录部署。全新安装与兼容边界见
[新部署与数据兼容](docs/FRESH-DEPLOYMENT.md)，开发进度和后续安排见 [路线图](docs/ROADMAP.md)。

## 功能

- **资产账本**：登记域名、订阅服务、未接入服务器、设备、证件、账户、保险等，统一记录费用、周期、到期日、标签和续费提醒；即将到期清单同时汇总已接入节点。
- **保险箱**：资产中的密码、证件号和恢复码等敏感字段用 AES-256-GCM 加密，密钥保存在数据目录的 `vault.key`；列表只显示是否已保存，查看单独鉴权并记录审计。当前使用本机密钥，主口令解锁仍是后续目标。
- **时间线与定时任务**：统一查看告警、审计及模块事件；资产提醒和时间线清理由统一调度器运行，可查询执行结果并立即运行。
- **主机监控**：CPU / 内存 / 根挂载点磁盘 / 网络速率 / 网卡累计 / 在线状态，30 天分钟级历史。
- **网络探测**：按节点显式绑定的 IPv4 / IPv6 TCP 延迟与三网回程；回程标签按实际经过的骨干命名（163 / CN2GIA / CN2GT / CTG / 4837 / 9929 / 10099 / CMIN2 / CMI）。
- **IP 被墙检测**：用自有大陆与境外 Agent 对照检测同一公网 IP / TCP 端口，提供连续两轮证据与「疑似被墙 / 大陆可达 / 部分线路不可达 / 各地均不可达 / 证据不足」结论。
- **地区偏移检测**（可选）：Server 上一个没有自己网络的隔离浏览器，经节点 Agent 的受限出口读取 Google / Meta 页面认定的地区，与目标地区比较，可在偏移时尝试一次校正并独立复测。默认不安装，见 [地区偏移检测](docs/IP-GUARD-INTEGRATION.md)。
- **解锁检测**：节点用自己的出口检测 24 个流媒体 / AI / 交易所 / 常用服务，给出 `19/24` 式分数与明细。
- **计费与提醒**：计费周期、流量额度、价格与到期时间、达量降速、按剩余时间比例估算的剩余价值；离线 / 续费 / 流量 / 降速 / 负载提醒，经 Telegram 或 Webhook 发送。
- **公开面板**：顶部磁贴汇总剩余价值与每月成本，统一单位在后台「面板设置 → 计费汇总」切换（跨币种按内置固定参考汇率折算）；卡片与列表两种视图，浅色 / 深色主题与多套外观风格；搜索认中文、全拼、拼音首字母、英文与代号（香港 = `xianggang` / `xg` / `hk`）。
- **管理后台**：节点管理与拖动排序、密钥轮换、任务目录检索与批量绑定、用量校正、告警事件与投递状态。
- **远程管理与服务**：节点显式开启 `--manage` 后，可执行命令、打开终端、管理 Docker 容器与 Compose，以及部署代理入站、转发、证书和 nginx 网站；支持使用者配额、订阅链接与加密备份到节点，详见 [部署与使用](docs/DEPLOYMENT.md)。
- **Agent 接入**：能力注册表统一生成 API（`/api/v1/actions`）、MCP（`/mcp`）、OpenAPI 与可下载的 Skill；访问密钥分只读 / 可写 / 高风险三档、可限定模块，高风险操作每次都要显式确认。见 [docs/MCP.md](docs/MCP.md)。
- **独立前台**：主题可用自己的 HTML / JavaScript 完全重写公开页，通过隔离环境中的 API v1 读取公开监控数据；支持自己的导航、布局、交互和路由，详见 [开发指南](docs/FRONTENDS.md)。
- **主题**：仅内置现代极简（默认）；液态玻璃、复古印刷、二次元等外观是主题包，在后台「主题」页上传 zip 安装（发行包附带官方主题 zip）；每个主题可以声明自己的设置菜单，附加包挂在它依附的主题的设置里开关；Server 不连接任何外部市场，监控数据不出本机。
- **声明式插件**：通知渠道（飞书、钉钉、企业微信、Bark、ntfy、Gotify……）与探测目标集，在后台「插件」页上传安装。插件只是一份 `plugin.json`（数据与模板，由 Lodestar 解释），不含代码；安装前列出它能做的事、发往哪些域名，由你确认。格式见 [插件说明](docs/PLUGINS.md)。

网页端助理已实现，支持 OpenAI / Anthropic 兼容网关、多轮工具和高风险操作逐次确认，使用前在「面板设置 → 助理」配置网关与模型，见 [助理说明](docs/ASSISTANT.md)。[每日简报](docs/BRIEF.md) 支持按时区自动发送、手动预览及模型失败回退，默认关闭。[情报中心](docs/INTEL.md) 支持 RSS / Atom、GitHub Release、网页变化、关键词规则、已读与星标。[日程与待办](docs/CALENDAR-TODO.md) 支持来源转换、分阶段提醒、只读私有 iCal 和简报“今天”。[财务](docs/FINANCE.md) 支持本地账户、精确金额流水、CSV、预算、固定费用和每日净资产快照。[自动化](docs/AUTOMATION.md) 支持时间线、cron、Webhook、权限复核与危险动作持久化确认。[数字身份与安全](docs/SECURITY.md) 复用账户资产，记录两步验证、恢复位置和人工复核，汇总域名/证书风险并提醒登录异常；HIBP 与 DNS 对外检查默认关闭。

## 一键部署

Linux 主机（amd64 / arm64），一条命令装成 systemd 服务。两种方式二选一：

**从 Release 一键安装（推荐）**：下载 GitHub Release 的预编译发布包，免 Go 与 git，内嵌两种架构的 Agent。

安装最新发布：

```sh
curl -fsSL https://raw.githubusercontent.com/leeska/Lodestar/main/deploy/install.sh | sudo sh
```

安装指定版本（把 `v1.0.0` 换成要装的版本）：

```sh
curl -fsSL https://raw.githubusercontent.com/leeska/Lodestar/main/deploy/install.sh | sudo sh -s -- --release v1.0.0
```

**从源码构建**：在本机编译 Server 与 amd64 / arm64 两个 Agent，需要 Go 1.27 与 git。本机编出的 Agent 没有签名，不能在后台一键升级。

构建 `main` 分支：

```sh
curl -fsSL https://raw.githubusercontent.com/leeska/Lodestar/main/deploy/install.sh | sudo sh -s -- --source
```

构建指定分支或 tag（把 `dev` 换成要构建的分支或 tag）：

```sh
curl -fsSL https://raw.githubusercontent.com/leeska/Lodestar/main/deploy/install.sh | sudo sh -s -- --ref dev
```

两种方式都安装 `/usr/local/bin/lodestar` 与 systemd 单元、建好 `/var/lib/lodestar`。重复执行即升级（可以在两种方式之间切换）：旧二进制保留为 `lodestar.previous`，服务自动重启并核对运行的是新版本。其他参数：`--listen ADDR`、`--data DIR`、`--no-systemd`。

装完脚本会打印后台用户名、密码（密码只在首次安装时显示一次，登录后可在“面板设置 → 后台账号”修改；忘记密码执行 `sudo lodestar reset-password --data /var/lib/lodestar`）。访问 `http://127.0.0.1:25774/admin` 用用户名密码登录。MCP API Key 在“面板设置”中生成。跨主机接入需在前面配置 HTTPS 反代，见 [部署说明](docs/DEPLOYMENT.md)。

Docker 部署（镜像本地构建，数据放命名卷）：

```sh
git clone --depth 1 https://github.com/leeska/Lodestar && cd Lodestar
docker compose -f deploy/docker-compose.yml up -d --build
```

后台初始密码不会写进容器日志（日志会被永久保留）：首次启动把它写进数据卷的 `admin-password.txt`，用 `docker compose -f deploy/docker-compose.yml exec lodestar cat /data/admin-password.txt` 读取，登录改密后删除该文件；也可以在 compose 里配置 `LODESTAR_ADMIN_PASSWORD`（8–128 个字符）自定义初始密码。

被监控主机不需要装 Go：后台新增节点后，“接入”弹窗给出的 `curl ... | sh -s -- --endpoint ... --token ...` 命令会下载并启动 Agent。

## 本地运行

需要 Go 1.27，不需要 CGO、外部数据库或 Node。

```sh
go build -o bin/lodestar ./cmd/server
go build -o bin/lodestar-agent ./cmd/agent
./bin/lodestar --listen 127.0.0.1:25774 --data ./data
```

首次直接启动时会创建后台账号 `admin`，随机初始密码写入数据目录的 `admin-password.txt`，日志只提示文件位置；也可先执行 `./bin/lodestar init --data ./data` 在终端拿到初始凭据。访问 `http://127.0.0.1:25774/admin` 用它登录，改密后删除初始密码文件；忘记密码执行 `./bin/lodestar reset-password --data ./data`。也可设置 `LODESTAR_ADMIN_TOKEN`（至少 32 个字符），作为管理 API 的密钥，不用于浏览器登录或外部 agent 接入，密钥不写入日志。接入配置里的地址必须能从 Agent 主机访问，远程机器不能用 `127.0.0.1` 或 `localhost`。也可以下载 `agent.json` 手动运行：

```sh
chmod 600 agent.json
./bin/lodestar-agent --config agent.json
```

```json
{
  "endpoint": "https://probe.example.com",
  "token": "后台生成的64位节点密钥",
  "interfaces": ["eth0"],
  "mount": "/",
  "nexttrace": "",
  "geo_provider": "",
  "trace_method": "",
  "allow_http": false
}
```

- `interfaces` 为空时汇总所有非 loopback 网卡；有桥接、容器、隧道的机器应填公网网卡，避免重复计数。
- `mount` 指定统计的文件系统。
- `nexttrace` 留空用内置追踪核心，填绝对路径则改调外部 NextTrace（仅作运维兜底）。
- `trace_method` 是回程探测方式：`icmp`（默认）、`tcp` 或 `udp`。
- `geo_provider` 是跳 ASN 数据源，默认 `IPAPI.com`；`disable-geoip` 时只靠本地前缀表。上游默认的 `NextTrace-API` 在常驻进程里查不到结果，不作默认。

正式支持 Linux amd64 / arm64，macOS 可用于开发验证。

## IP 被墙检测

后台「网络探测 → ＋ IP 被墙检测」填写公网 IP、正在监听的 TCP 端口，再分别选择至少一个中国大陆检测节点和一个境外对照节点。两组不能重复，合计最多 20 个；建议大陆节点覆盖不同地区与运营商，均使用所在地的直连出口。检测间隔 60–3600 秒，默认 5 分钟，支持 IPv4 / IPv6；旧 Agent 直接执行现有 TCP 任务，只需升级 Server。

大陆节点全部连续两轮连接失败、且境外同期可达时显示「疑似被墙」。每轮尝试 3 次连接，两轮至少相隔 30 秒；离线、缺失上报、协议不可用和过期结果不算连接失败。首轮失败时等待复测；全部大陆节点可达时显示「大陆可达」，部分大陆节点可达、另有节点连续失败时显示「部分线路不可达」；大陆和境外均连续失败则显示「各地均不可达」。任务可立即执行、暂停、复制和编辑，在结果区刷新并查看各节点最近两轮明细。

结论只覆盖指定 IP 与端口，不能证明封锁原因：目标防火墙、地区封禁、路由故障也可能呈现相同结果。修改检测配置会重新积累证据；调整大陆 / 境外节点请编辑任务。此功能提供检测结果，不会自动创建告警规则。

## 三网探测

后台建任务，从内置目录按地区、运营商、IP 协议检索并多选目标（也可填自定义目标，海外选“其他”运营商），再明确绑定节点；空绑定不执行。一个任务可同时勾选延迟与回程，每组目标可单独选 IPv4 / IPv6。

- **频率**：TCP 最短 30 秒；回程最短 15 分钟、默认 1 小时。单个节点每次最多领 8 个 TCP、1 个回程和 1 个解锁任务，各自并行执行，长任务不会卡住延迟探测。
- **TCP 延迟**是连接耗时与连接失败比例，**不是 ICMP 丢包率**。卡片每个运营商一格，下方 20 个小格各代表一次探测，失败画空心红格，无数据画灰格。色带时间范围在后台“面板设置 → 延迟色带”里调。
- **回程**由 Agent 内置的 NTrace-core（NextTrace 的追踪核心）在进程内执行，固定 30 跳、单次测量、45 秒预算，不生成分享地图。标签 ASN 优先、注册库核对过的运营商网段兜底，表外地址保持“未知”，不编造线路；判定规则见 `internal/model/route.go`。卡片只显示节点“卡片代表地区”的结果，详情展示全部；跳地址公开前脱敏。
- **IP 协议可用性**以内核真有该族路由为准：IPv4 只交给本机代理或隧道、没有 IPv4 默认路由的机器不会上报 IPv4，对应任务也不派发。
- **Fake-IP / 代理**：DNS 返回 `198.18.0.0/15` 时 Agent 拒绝把它当目标。开着代理的机器测出来的是代理路径，**不能用来验收回程**。
- **目标目录**是 186 个公网 IP 快照，不代表可达，部署后应在实际节点验证；维护时运行 `scripts/refresh-catalog.py`，Server / Agent 不自动拉取。

内置追踪核心 NTrace-core 为 GPL-3.0，Agent 二进制的分发条款见 [NOTICE](NOTICE)。

## 解锁检测

建一个类型为 `unlock` 的任务并绑定节点（无目标、无端口，最短 1 小时一轮）。Agent 用这台机器自己的出口逐项发起 HTTPS 检测，全部在 Go 标准库里完成，不起 shell、不需要额外安装。IP 协议留空时走 IPv4，纯 IPv6 机器由 Server 改派 IPv6。

- 结论五种：已解锁 / 仅自制剧 / IP 被封禁 / 未解锁 / 检测失败。分数 = 已解锁 + 仅自制剧，分母固定 24。**检测失败不是结论**，连不上、超时、页面异常都记失败，不会写成未解锁。
- Bing / Apple / Google Play / Steam / OneTrust 只给地区或币种，不参与解锁判断。
- 结果反映的是这台机器的实际出口：开着代理或分流时，答案是那条出口的。
- 改任务、删任务、删节点都会连带清掉旧结论。接口见 [HTTP 协议](docs/API.md)。

## 计费与提醒

- 周期按 UTC 时刻逐月重置、精确到秒，当月不足该日时取月末。额度按十进制字节，统计方式可选双向合计、取较大方向、仅下载、仅上传。
- 流量是 Agent 上报的计数器差值：首次上报建立基线；主机重启或网卡集合变化时重建基线，不重复计费。重启前未上报的部分无法恢复，因此它是运维估算，不能替代服务商账单。可手动校正本周期用量，下周期自动失效。
- 达量降速支持两种口径：近 N 小时平均带宽超过阈值，或周期额度用满后限速。
- 提醒规则逐条设置，每条作用于全部节点、某几个分组或指定节点；同一台节点同一种提醒只生效一条，指定节点 > 分组 > 全部节点，作用于全部节点的规则会自动套到新加的节点上（离线提醒对还没接入过的新节点先等 15 分钟，留出装 Agent 的时间）。
- 负载超过阈值并持续设定时间才触发；离线可额外等待；可选不少于 5 分钟的重复提醒。
- 同一时间使用一个通知渠道（Telegram 或 Webhook）。Webhook 接收 `{ "version": 1, "event": { ... } }`，2xx 为成功；失败指数退避、最多 10 次，接收端按 `event.id` 去重。投递是至少一次语义；未配置渠道时事件照常记录，配置后只补发 24 小时内的。
- Telegram 推送按状态、节点、实际读数、触发条件与时间分段，解除事件区分指标正常、节点离线和规则移除；详见 [通知模板说明](docs/NOTIFICATIONS.md)。
- 保留期固定：主机历史 30 天、网络结果 7 天、流量账本 400 天、告警事件 90 天。

## Agent 接入（API / MCP / Skill）

外部 agent（Claude Code、Codex 等）用后台「Agent 接入」页建的访问密钥（`Authorization: Bearer ls_…`）调用 Lodestar 的能力：
`GET /api/v1/actions` 列出能用的能力，`POST /api/v1/actions/<能力名>` 执行；同样的能力在 `POST /mcp` 是 MCP 工具，在 `/api/v1/openapi.json` 有 OpenAPI 文档，页面上还能下载 Skill 包。
密钥分只读 / 可写 / 高风险三档，可限定模块、设有效期、随时吊销；高风险能力（删除、换密钥、执行命令等）每次调用都要显式确认。详见 [Agent 接入](docs/MCP.md)。

## 升级顺序

以后升级时**先升 Server，再升节点 Agent。** Agent 在上报里声明能力（`capabilities`），Server 按能力派发任务。当前 Server 会保留格式合法的未知能力名；更早的 Server 可能不认识新 Agent 的字段或协议，因此仍须遵守升级顺序。涉及数据库迁移时先备份，回退二进制还须恢复升级前的数据目录。

## 部署与维护

- [部署说明](docs/DEPLOYMENT.md)：手工安装、反代、升级、备份与恢复。
- [目标](docs/VISION.md)、[路线图](docs/ROADMAP.md)：个人中枢的方向、已完成能力与后续开发顺序。
- [MCP 接入指南](docs/MCP.md)：API Key、客户端配置、工具能力与连接排错。
- [插件](docs/PLUGINS.md)：声明式通知渠道与探测目标集的格式、模板变量与安全边界。
- [主题包与附加包](docs/THEMES.md)：自己做一套外观风格，或用附加包给某套主题添按节点 / 地区匹配的装饰（zip 上传，只含样式表与图片），格式、颜色变量与示例。
- [组件边界](docs/ARCHITECTURE.md)、[HTTP 协议](docs/API.md)、[项目范围](docs/SCOPE.md)。
- [地区偏移检测](docs/IP-GUARD-INTEGRATION.md)：安装隔离浏览器（`--with-browser`）、节点开通出口（`--ipguard`）、判定规则与故障排查。
- [验证方法](docs/VERIFICATION.md)：怎样跑检查，以及哪些行为只能在真实环境验证。

检查与构建用自带的流水线（不依赖 GitHub Actions），Linux 与 macOS 都能跑。需要 Go 1.27；`check` 的前端部分另需 Node ≥ 20.19 或 Bun（没有 Node 时自动用 Bun）：

```sh
bash scripts/pipeline.sh check   # gofmt / go vet / go test（有 gcc 时加 -race）/ web/*.js 语法 / 前端集成检查
bash scripts/pipeline.sh build   # dist/lodestar-linux-{amd64,arm64}.tar.gz(+.sha256)
bash scripts/pipeline.sh all     # 默认：check 后 build
GITHUB_TOKEN=... bash scripts/release.sh vX.Y.Z dist   # 打 tag、建 Release、上传发行包
```

`UI_SMOKE=0` 跳过前端集成检查，`OUT=<目录>` 换产物目录，`LODESTAR_NODE` / `LODESTAR_NPM` 指定 Node 与 npm，`LODESTAR_BUN` 指定 Bun。

Lodestar 面向个人使用，不做交易、转账、多人协作平台或自训练模型。远程命令与终端只在节点显式开启远程管理后可用，操作记录审计；通过能力 API 或 MCP 执行命令属于高风险操作，每次都要确认。Agent 一键升级由管理员触发，只接受运维者签过名的新版本，签名私钥不放在 Server 上，见 [部署说明](docs/DEPLOYMENT.md)。

主题与插件都在后台上传 zip 安装，不连接市场。插件仅限声明式的通知渠道和探测目标集，不运行第三方代码，也不提供 Agent 端插件或命令通道；独立前台主题的脚本运行在隔离环境，见 [项目范围](docs/SCOPE.md) 与 [前台开发指南](docs/FRONTENDS.md)。

## License

MIT。内置追踪核心 NTrace-core 为 GPL-3.0，见 [NOTICE](NOTICE)。目标目录的数据来源见 `internal/catalog/targets.json` 中的 `source`。
