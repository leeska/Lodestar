# 情报中心

后台「情报」收集 RSS / Atom、GitHub Release 和网页变化。每条内容保留来源链接，支持搜索、按源筛选、已读和星标。默认没有来源，也没有立即推送规则。

## 来源

| 连接器 | 配置 | 行为 |
| --- | --- | --- |
| `rss` | `url` | 解析 RSS 2.0 / Atom；按 GUID、ID 或链接生成稳定键，重复抓取不产生重复条目 |
| `github_release` | `repository`，例如 `owner/repo`；可选独立 token | 从固定的 GitHub API 域名读取最多 50 条 Release，跳过 draft；token 加密保存 |
| `webpage` | `url`；可选 `pattern` | 提取正则第一捕获组（无捕获组则取完整匹配），或读取整页文本；首次保存基线内容，后续只在提取结果改变时产生新条目 |

网页不执行 JavaScript；没有 CSS 选择器或登录浏览器。可以用提取正则关注价格、补货提示等页面内容，规则匹配的是文字，不是数值阈值。

支持 ETag 和 Last-Modified；304 保留先前的内容指纹。修改名称和间隔保留缓存，修改来源地址或连接器重新建立基线。每个源保留上次抓取时间和错误，正在抓取时不能修改或删除它。

## 网络边界

只允许 HTTP / HTTPS，不接受 URL 中的账号密码。请求前解析域名，校验全部结果都是公网地址，再直接连接校验过的 IP；拒绝本机、内网、链路本地、共享地址、文档和测试网段，以及常见 NAT64 私网绕过路径。重定向同样检查，携带凭据时不跨域，不从 HTTPS 降级，不使用环境代理。

单次请求最多 15 秒、读取最多 1 MB，不把上游响应正文作为错误回传。GitHub token 只发给固定的 GitHub API 地址；源列表只显示是否保存，修改时不传保留、空串删除。

`intel.fetch` 每分钟检查一次，每轮最多处理 20 个到期来源，最多 4 个并发。按上次抓取时间排序，积压来源在后续轮次继续处理。一个源出错会记录到它自身，不阻止其他工作线程完成抓取。

## 规则、推送与简报

规则按创建顺序匹配，第一条命中生效：

- `notify`：通过已有告警通知出口排队推送，使用「情报更新」消息，不显示为告警恢复；同时记时间线。
- `brief`：写时间线，在每日简报中汇总过去 24 小时采集、没有立即推送的条目。
- `ignore`：保留条目，但不推送、不进简报或时间线。

关键词是任意一个命中，空关键词匹配全部；源范围为空表示包括未来新增的全部来源。首次抓取的条目也应用规则。规则修改不追溯处理已保存的条目。没有命中时默认放进简报。

条目按采集时间保留 90 天，星标不自动清理。删除来源会删除凭据和全部条目（包括星标），因此是 Danger 能力；时间线中的既有历史记录保留。

## 接口与能力

后台接口位于 `/api/v1/admin/intel`：

- `GET /sources`、`POST /sources`、`PUT /sources/{id}`、`DELETE /sources/{id}`。
- `POST /sources/{id}/fetch` 立即抓取。
- `GET /items?source=&q=&after=&before=&unread=1&starred=1&limit=`；`after` 是采集时间下界（Unix 秒），`before` 是条目 ID 上界，最多 200 条。
- `POST /items/{id}/mark` 接受 `read` / `starred` 布尔值。
- `GET /rules`、`POST /rules`、`PUT /rules/{id}`、`DELETE /rules/{id}`。

统一能力包括 `intel.sources`、`intel.items`、`intel.add_source`、`intel.update_source`、`intel.delete_source`、`intel.fetch`、`intel.mark` 和规则的增删改查，自动出现在 MCP、OpenAPI 和 Skill。

## 验证

`internal/safefetch/http_test.go` 检查内网、保留地址、混合 DNS 答案和固定连接地址。`internal/intel/sources_test.go` 检查 RSS / Atom、外部实体拒绝、缓存、页面指纹、GitHub 请求及凭据错误遮盖；`intel_test.go` 检查去重、规则、简报、星标保留、单源故障隔离和抓取中禁止修改。

`scripts/intel-test.mjs` 验证条目转义、危险链接拒绝及已读 / 星标；ui-smoke 对一次性 Server 验证来源和规则管理。
