# 验证方法

这份文档写两件事：怎样验证一次修改，以及哪些行为只能在真实环境里验证。

当前新部署与升级边界见 [新部署与数据兼容](FRESH-DEPLOYMENT.md)。数据库测试覆盖全新初始化、
v2.0.0 原地升级、模块迁移只执行一次、失败回滚与重试、不支持的数据拒绝且保留，以及备份独立重开。

## 每次修改

```sh
gofmt -l .                 # 必须无输出
go vet ./...
go test -race ./...        # 没有 gcc 的机器去掉 -race
```

`bash scripts/pipeline.sh check` 会一次跑完以上三项，外加 `web/*.js` 语法检查、`scripts/presentation-test.mjs` 和一次性 Server 上的前端集成检查（需要 Node ≥ 20.19 或 Bun；没有 Node 时自动用 Bun，jsdom 与 esbuild 也由它安装）。


## 核心服务与 Agent 接入回归

- 助理回归见 `internal/httpapi/assistant_test.go`、`internal/assistant/provider_test.go` 和流水线中的 `scripts/assistant-test.mjs`，覆盖多轮工具、确认防重放、重启继续、密钥遮盖、调用者范围和取消传播。

- `internal/storage/migrate_test.go`：旧基线升级、重复启动、失败重试、更新迁移拒绝与审计。
- `internal/httpapi/actions_test.go`：访问密钥的风险档和模块限制、高风险操作逐次确认、失效或吊销密钥拒绝、MCP 工具来自注册表、OpenAPI 与 Skill 覆盖全部能力，以及旧 MCP 密钥兼容。
- `internal/vault/vault_test.go`：密钥文件与密文读写、附加数据绑定；`internal/assets/assets_test.go`：敏感字段加密、续期后提醒重置及各阶段只提醒一次。
- `internal/timeline/timeline_test.go`：告警、审计与模块条目合并；`internal/jobs/jobs_test.go`：定时任务状态与失败、恢复的时间线事件。

历史组件安装器审批与配套 CC 的测试不属于当前仓库，不作为当前版本的验收项；当时的验证记录保留在日期标注的审阅与整改文档中。

## 前端集成检查（ui-smoke）

只针对独立 loopback 数据目录。脚本会创建并删除自己的测试节点和任务，**不要指向生产数据**。

```sh
npm install --prefix /tmp/lodestar-ui-deps --no-audit --no-fund jsdom@26.1.0 esbuild@0.25.10
go build -o bin/lodestar ./cmd/server
mkdir -p /tmp/lodestar-ui-data
./bin/lodestar init --data /tmp/lodestar-ui-data > /tmp/lodestar-ui-credentials
./bin/lodestar --listen 127.0.0.1:25874 --data /tmp/lodestar-ui-data
# 官方主题包（液态玻璃 / 复古印刷 / 二次元）打成 zip，ui-smoke 会把它们装到这台一次性 Server 上：
go run ./tools/themepack -out /tmp/lodestar-ui-themes themes/*
# 另一个终端：
LODESTAR_UI_DEPS=/tmp/lodestar-ui-deps \
LODESTAR_TEST_URL=http://127.0.0.1:25874 \
LODESTAR_TEST_ADMIN_FILE=/tmp/lodestar-ui-data/admin-token \
LODESTAR_TEST_CREDENTIALS_FILE=/tmp/lodestar-ui-credentials \
LODESTAR_TEST_THEMES=/tmp/lodestar-ui-themes \
node scripts/ui-smoke.mjs
```

用 Bun 时把安装换成在依赖目录里 `bun add jsdom@26.1.0 esbuild@0.25.10`（目录里先放一个只含 `{}` 的 package.json），把 `node` 换成 `bun`。这些依赖只用于验收，生产构建与运行不依赖它们。

## 需要真实浏览器的检查

jsdom 不做布局，排版类问题只有真浏览器量得出来。用法见各脚本开头的注释。

| 脚本 | 检查内容 |
|---|---|
| `scripts/frontend-browser-test.mjs` | 独立前台渲染、公开数据隔离、模块分块、深链接、移动端、后台分离与故障恢复，见 [FRONTENDS.md](FRONTENDS.md) |
| `scripts/visual-check.mjs` | 公开页、详情弹窗、后台的桌面与手机截图 |
| `scripts/card-overflow-check.mjs` | 卡片与列表里的文字不越过卡片右边框 |
| `scripts/route-nowrap-check.mjs` | 回程格居中且一行读完（三套皮肤） |
| `scripts/layout-webkit.mjs` | WebKit 下整页高度不随刷新抖动 |
| `scripts/unlock-acceptance.sh` | 解锁检测按 Agent 路径完整走一遍：上报、落库、计分 |

## 只能在真实环境验证的

- **三网回程标签**：必须在有直连出口的 Linux 节点上测。开着代理或 Fake-IP 的机器测到的是代理路径，开发机的结果不能作为证据。
- **ASN 数据源可用性**：`IPAPI.com` 等第三方数据源的可用率随时间变化，需要在节点上观察。
- **解锁检测结论**：取决于节点出口，与其他检测脚本对照时须用同一协议族。
- **长期资源消耗**：大规模节点下 Server 的 SQLite 体积与 1 核机上的查询耗时。

## Agent 诊断回归

`internal/agent/heartbeat_test.go` 验证采集失败/超时时心跳独立发送、无伪造指标、错误分类不泄露原始路径、上报恢复，以及旧 Server 的 404/405 降级。storage 回归确认心跳不改变采样与计费基线、报告恢复、首次接入和节点清理；HTTP 回归验证节点令牌隔离与状态校验。`ui-smoke.mjs` 通过真实 Server 发送诊断与恢复上报，确认卡片即时显示异常并清除旧诊断；布局需另用真实浏览器验证桌面与手机宽度。

## 告警评估暂停回归

`internal/notify/evaluation_test.go` 覆盖 CPU / 内存在离线、采集失败、采集超时、上报失败时保留当前告警、不产生伪恢复或新重复提醒，以及 Server 重启、有效高/低读数恢复、持续计时重置和规则删除。storage 检查暂停状态只写一次，HTTP 检查当前状态字段与不可变历史隔离。流水线中的 `scripts/alert-evaluation-test.mjs` 使用真实页面模块验证「待确认」提示、历史与恢复状态和转义；仍需在一次性 Server 上用内置浏览器检查实际布局。

## 审阅修复回归

源码版本解析由 `cmd/server` 的版本测试覆盖精确 tag、开发分支、无 Git 的源码目录及安装旧 tag 的兼容路径。`scripts/theme-addons-test.mjs` 验证自定义设置主题仍可管理与删除附加包，且不再发出任何市场请求。

Agent 的状态序号测试覆盖故障心跳在恢复报告之后到达、报告尚未应答时的心跳，以及服务端重启和 Agent 新会话。


## IP 被墙检测回归

`go test ./internal/model ./internal/storage ./internal/httpapi ./internal/mcp` 覆盖公网 IP / 角色校验、两轮 TCP 证据、异步对照、暂停、旧 Agent 调度、删除节点、修改角色隔离旧证据与接口鉴权。`scripts/block-checks-test.mjs` 随流水线执行，覆盖两组节点互斥选择、IPv6 表单提交、结果明细与 HTML 转义。

真实封锁结论须在自有中国大陆和境外直连节点上对同一已开放端口检测。本地测试使用合成连接结果，验证调度与判定流程，不能验证实际线路是否被墙。
