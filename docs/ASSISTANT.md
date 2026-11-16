# 网页端助理

后台「助理」可以查询和修改 Lodestar 中的数据。模型使用与外部 agent 相同的能力注册表；调用仍经过风险档、模块范围与审计校验。

## 配置

在「面板设置 → 助理」填写协议、网关地址和模型名：

- OpenAI 兼容：地址可以是网关根地址、`/v1`，或完整的 `/v1/chat/completions`。
- Anthropic 兼容：对应 `/v1/messages`。
- 支持本机或内网的 magpie 网关。地址必须能从运行 Lodestar Server 的机器访问；远程服务器上的 `127.0.0.1` 指向远程服务器自身。
- 网关密钥可选，保存在保险箱的 `assistant:gateway:key`，设置接口只返回是否已经保存。留空保留原值，勾选清除才删除。
- 附加提示词用于补充偏好，不改变权限和确认规则。

对话文本、提供给模型的能力说明和工具结果会发送到配置的网关。会话记录存入本机数据库。停止不会回滚已经执行的操作。

## 使用与确认

可以从总览点「问助理」，或在助理页开启新对话。会话支持改名、删除和重新打开；工具的参数与结果可展开查看。

Read 能力和个人数据模块（待办、日程、财务、资产、情报等）的 Write 能力直接执行。以下调用会先展示名称、说明和实际参数，由用户选择「执行」或「不要」：

- 所有 Danger 能力；
- 标记为 `Confirm` 的 Write 能力：`agents`、`alerts`、`automation`、`certs`、`docker`、`nodes`、`proxy`、`settings`、`sites`、`users` 模块的写操作（`settings.test_notification` 除外）。这些操作会改动基础设施、对外暴露范围或通知与数据的去向。模型读到的情报、网页等工具结果可能含有注入指令，因此不让模型自行执行这类修改。名单在 `internal/httpapi/actions.go` 的 `confirmModules`，自动化规则使用同一判断。

一项确认只对应这一项调用，刷新或 Server 重启后仍能继续处理；重复提交失效的确认不会再次执行。

每轮最多 10 次工具调用，确认前后的调用合并计数。一轮处理在 Server 后台运行，最多持续三分钟：离开页面、切换会话或断网只会断开流，处理继续进行并保存结果。回到这个会话时，页面会显示已保存的消息，并从头重放本轮事件、接着接收后续内容。「停止」会调用 stop 接口，取消后台处理和模型请求。已经完成的工具结果保留；执行中断（例如 Server 重启）且结果未记下的调用标记为结果未知，不能自动推断成功或直接重试。

Markdown 只支持经过转义的文字、标题、列表、代码、粗体和 HTTP / HTTPS 链接，不执行模型返回的 HTML。

## HTTP

以下接口只允许后台身份访问，写操作使用现有的同源检查与审计：

| 接口 | 用途 |
| --- | --- |
| `GET /api/v1/admin/assistant/settings` | 读取配置，不返回密钥 |
| `PUT /api/v1/admin/assistant/settings` | 保存协议、地址、模型、提示词与可选的新密钥 |
| `GET /api/v1/admin/assistant/conversations` | 最近 100 个会话 |
| `GET /api/v1/admin/assistant/conversations/{id}` | 会话与最近 120 条消息、待确认调用 |
| `PATCH /api/v1/admin/assistant/conversations/{id}` | 修改 `title` |
| `DELETE /api/v1/admin/assistant/conversations/{id}` | 删除会话和消息 |
| `POST /api/v1/admin/assistant/chat` | `{conversation_id?, message}` |
| `POST /api/v1/admin/assistant/confirm` | `{conversation_id, call_id, approve}` |
| `GET /api/v1/admin/assistant/conversations/{id}/live` | 接回正在运行的一轮，从头重放事件；没有运行时返回 204 |
| `POST /api/v1/admin/assistant/conversations/{id}/stop` | 停止正在运行的一轮 |

chat / confirm / live 返回 SSE，事件包括 `conversation`、`user`、`text`、`tool_call`、`tool_result`、`confirm`、`done`、`error`。会话详情在运行期间带 `running: true` 和 `run_after`（本轮开始前最后一条已保存消息的 id），页面只渲染到这条，其余由重放补上。网关流最后一个事件后不带空行就关闭连接时，也会被正确处理。`confirm` 表示本次流正常暂停，后续由单独的 confirm 请求继续。SSE 禁止代理缓冲，也不经过 API 的 gzip 压缩。

## 外部 agent

`assistant.ask` 是 Write 能力，接受 `{message}`，返回回答、会话 ID 以及可能的待确认信息。它只向模型提供当前访问密钥允许使用的能力，并在执行时再次校验；不能借助助理扩大密钥权限，也不能递归调用自己。

外部 agent 的会话与后台会话按调用者隔离。`assistant.ask` 不提供高风险确认入口，遇到这类操作只返回待确认信息，不会执行。外部 agent 可以使用对应的原生能力，按原有确认流程完成操作。

## 数据与验证

assistant/1 迁移创建配置、会话和消息表；assistant/2 标记会话加密格式。标题、消息和待确认参数通过保险箱加密，原表只保留密文引用；启动时把旧明文记录转换，转换可重复执行。恢复时必须保留数据目录的 `vault.key`，回滚旧二进制必须同时恢复升级前的数据目录。历史备份及旧数据库页仍应按敏感文件保管。

工具结果最多保存约 32 KB；网关密钥即使被上游回显，也会在流和存储前遮盖。工具操作的审计身份以 `assistant:` 开头。

`internal/httpapi/assistant_test.go` 使用假网关验证连续工具调用、危险操作拒绝与确认、防重放、重启后继续、访问范围、审计、取消传播、离开页面后继续运行、stop 接口，以及基础设施写操作需要确认。`internal/assistant/provider_test.go` 覆盖流格式、跨分片密钥遮盖和半截工具调用；`scripts/assistant-test.mjs` 验证 Markdown 转义。
