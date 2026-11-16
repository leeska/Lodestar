# v3.3—v3.9 实施记录

## v3.7 财务

- 已部署 v3.7.0；core/3、finance/1，`finance.snapshot` 已登记。HTTPS 200，三个服务 active，13/13 节点在线，Agent 保持 v3.1.0，无新增 warning。
- 全量流水线通过 41 个 Go 包、竞态、前端行为和一次性 Server 集成；桌面和手机验证账户、流水、CSV、净资产曲线及资产提醒按钮。
- 金额使用十进制字符串及整数分记账；CSV 原子校验与去重，静态汇率总和后四舍五入，手动/记账余额、月度预算、每日快照和经常性支出已实现。
- 发布包 `/tmp/lodestar-release-v3.7.0`；生产 `/root/lodestar-v3.7.0`；备份 `/root/lodestar-before-v3.7.0.tar.zst`。未写入虚构财务记录，未提交或推送。

## v3.8 自动化

- 全量流水线通过 42 个 Go 包、竞态、前端行为与一次性 Server 集成；另补测超大指数拒绝，并通过自动化包竞态测试。
- 浏览器桌面 1440×900、手机 375×812 验证保存规则、冻结参数展示、危险确认及成功结果；文档宽度 375，无横向溢出，无浏览器错误。
- 已部署 v3.8.0；三个服务 active，HTTPS 200，13/13 节点在线，core/4、timeline/2、automation/1 已执行，`automation.tick` 已登记，无新增 warning。
- 发布包 `/tmp/lodestar-release-v3.8.0`；生产 `/root/lodestar-v3.8.0`；升级前备份 `/root/lodestar-before-v3.8.0.tar.zst`。没有创建生产规则。

## v3.9 数字身份与安全

- 账号资产复用、2FA / 恢复位置 / 人工复核、HIBP 固定端点前缀查询、每周去重、密码版本绑定、DNS 与证书风险、现有签名设备 Cookie 的登录异常观察已实现。
- 最后完整流水线 `/tmp/lodestar-v390-check-final.log` 通过 43 个 Go 包、vet、竞态、全部前端行为和一次性 Server 集成。另验证 HIBP 官方公开示例 HTTP 200 / 有效后缀计数格式，未使用用户密码。
- Chromium 桌面 1440×900 和手机 375×812 验证安全页、复核表单和设置实际保存；正文宽 375、弹窗宽 343，无页面横向溢出，无浏览器错误。发现并修正总览缺少管理员身份、表单重复取消按钮。
- HIBP / DNS 全局开关和逐账户密码检查均默认关闭；所有示例仅在本机隔离预览，未配置生产外部查询。
- 已部署 v3.9.0，安装 `EXIT=0`；lodestar、lodestar-browser、nginx active，公网 HTTPS 200 / v3.9.0，13/13 节点在线，Agent 保持 v3.1.0。security/1 已迁移，`security.passwords` 与 `security.dns` 已登记，无新增 warning。
- 生产验收显示 HIBP=false、DNS=false、登录提醒=true、自动化规则 0、安全账户 0；助理模型仍未配置，简报仍关闭，未写入示例资产、账户、规则或凭据。
- 发布包 `/tmp/lodestar-release-v3.9.0`；生产 `/root/lodestar-v3.9.0`；停服完整备份 `/root/lodestar-before-v3.9.0.tar.zst`，283297712 字节、0600。磁盘仍有约 92 GB 可用，保留此前各版本发布包及升级前备份。
- Git 仍为 main / 3aa1174，新增与修改内容保留在工作区，没有创建分支、提交或推送。

用户已授权持续推进至 v3.9 并部署。按 ROADMAP 的顺序分别实现、验证和部署；数据库迁移前停服备份整个数据目录。当时没有收到提交或推送授权，工作区保留原有文档改动。2026-11-16 用户已授权提交、推送和部署；后续发布见 [v3.10.0 UI 重构记录](UI-REFACTOR-2026-11-16.md)。

## v3.3 网页端助理

- 实现 OpenAI / Anthropic 兼容流、会话、工具循环、逐次危险确认、调用者范围、审计和网关密钥遮盖。
- 补充资产、时间线、Agent 接入三个页面的 ui-smoke。
- 全量流水线通过：35 个 Go 包、vet、竞态、前端语法与行为、一次性 Server 集成；角色生成器因本机缺 Pillow / numpy 跳过。
- 真实 Chromium 检查 1440×900 和 375×812；无横向溢出。测试网关下验证多轮调用、确认暂停与拒绝继续、Markdown 转义。
- 发布包：`/tmp/lodestar-release-v3.3.0`，签名使用既有可信密钥，私钥留在本机。
- 生产包：`/root/lodestar-v3.3.0`；升级前备份 `/root/lodestar-before-v3.3.0.tar.zst`，0600。
- 安装日志 `EXIT=0`；lodestar、lodestar-browser、nginx 为 active，本机和公网 HTTPS healthz 为 v3.3.0。
- 生产只读验收：13 / 13 节点在线，Agent 版本 v3.1.0；assistant/1 迁移已执行。公网检查使用 curl User-Agent 返回 200；默认 Python User-Agent 曾返回 403，未修改 Cloudflare 规则。
- 网关与模型尚未在生产配置；没有把测试网关或虚构密钥写入生产。异地备份口令、存放节点仍须由用户设置。

## v3.4 每日简报与会话加密

- 时区调度、防重复投递、通知出口复用、模型改写回退、设置与预览已实现。增加 assistant/2，把会话标题、消息和待确认参数也通过保险箱加密，启动时转换旧记录。
- 加固后完整流水线通过：36 个 Go 包、vet、竞态、前端行为与一次性 Server 集成。补测夏令时重复时刻、模型回退、发送失败不自动重放、手动重试、旧会话转换及删除密文。
- 真实 Chromium 检查 1440×900 和 375×812；修复设置标签和简报按钮在手机上挤字。预览正常，无浏览器错误。
- 发布包 `/tmp/lodestar-release-v3.4.0`；生产包 `/root/lodestar-v3.4.0`；升级前备份 `/root/lodestar-before-v3.4.0.tar.zst`。
- 生产验收：安装 `EXIT=0`，三个服务 active，HTTPS 200 / v3.4.0，13/13 节点在线，assistant/2 与 brief/1 已迁移，`brief.daily` 已登记，无新增 warning。简报保持默认关闭，助理模型仍未配置。

## v3.5 情报中心

- 实现 RSS / Atom、GitHub Release、网页变化，公网地址固定连接、重定向检查、条件缓存、去重、规则、已读与星标、90 天清理和简报接入。令牌存保险箱，网页首次抓取只记基线。
- 全量流水线通过：38 个 Go 包、vet、竞态、前端行为与一次性 Server 集成；修正 CSS 根元素回归断言误匹配 `.intel-body` 的问题。真实 Chromium 桌面与手机检查通过，无横向溢出。
- 发布包 `/tmp/lodestar-release-v3.5.0`；生产包 `/root/lodestar-v3.5.0`；升级前完整备份 `/root/lodestar-before-v3.5.0.tar.zst`，0600。
- 生产验收：安装 `EXIT=0`，三个服务 active，HTTPS 200 / v3.5.0，13/13 节点在线，intel/1 已迁移，`intel.fetch` 已登记，无新增 warning。没有替用户添加订阅来源或开启简报。

## v3.6 日程与待办

- 实现 todo/1、calendar/1：待办 CRUD、来源转换、提醒阶段、完成与重开；来源转换还检查来源模块读取权限。私有 iCal 地址加密，支持时区、重复、例外、取消、条件请求和每天重新展开窗口；失败保留成功快照。
- 简报“今天”合并日程和到期/逾期待办；页面提供只读日历、订阅管理，以及资产、告警、情报转待办入口。
- 全量流水线通过：40 个 Go 包、vet、竞态、前端行为和一次性 Server 集成。修正弹窗 API 使用，增加专门的真实表单提交回归；原生 dialog 关闭不会删除 DOM，集成断言改为检查 open 状态。
- 真实 Chromium 验证 1440×900 / 375×812、待办保存、名称转义、日历保存与私有地址不回显；无横向溢出。补充重定向去 Referer，避免私有日历地址出现在跳转请求头。
- 发布包 `/tmp/lodestar-release-v3.6.0`；生产 `/root/lodestar-v3.6.0`；升级前完整备份 `/root/lodestar-before-v3.6.0.tar.zst`。
- 生产验收：安装 `EXIT=0`、三个服务 active、HTTPS 200 / v3.6.0、13/13 节点在线，两个迁移和 `todo.remind` / `calendar.sync` 已登记，无新增 warning。没有替用户添加私有日历。

## 后续

v3.3—v3.9 已分别实现、验证与部署。后续 UI 重构及 v3.10.0 发布见 [UI 重构记录](UI-REFACTOR-2026-11-16.md)。
