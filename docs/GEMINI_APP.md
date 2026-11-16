# Gemini App 平台（gemini_app）

`gemini_app` 是一个原生 OAuth 平台：sub2api 直接以 Google 账号登录 Gemini App
（`robinfrontend-pa.googleapis.com` 的 `ProcessQuery` RPC），不经过任何自建 Node
网关。上游只提供文本生成，没有原生函数调用；工具通过提示词协议表达，再由 sub2api
还原为 OpenAI `tool_calls`。

## 支持的下游协议

| 协议 | 端点 |
| --- | --- |
| Chat Completions | `POST /v1/chat/completions`、`POST /gemini-app/v1/chat/completions` |
| Responses | `POST /v1/responses`、`POST /gemini-app/v1/responses` |
| 模型列表 | `GET /v1/models`、`GET /gemini-app/v1/models` |

Anthropic Messages（`/v1/messages`）不支持：上游没有对应的原生形态。分组默认
关闭 `/v1/messages` 调度，请求会得到 403 `permission_error`。

## 计费

三个原生别名没有全局模型倍率表条目，缺省按以下回退价卡计费（运维方仍可通过
模型定价配置覆盖）：

| 别名 | 回退价卡 | 输入 / 输出 / 缓存读取（每百万 token） |
| --- | --- | --- |
| `gemini-argon` | `gemini-4-argon` | $2.00 / $10.00 / $0.10 |
| `gemini-hydrogen` | `gemini-3.8-flash` | $0.75 / $3.75 / $0.075 |
| `gemini-flash-lite` | `gemini-flash-lite` | $0.10 / $0.40 / $0.01 |

Gemini App 是订阅制授权，上游不返回计费金额；以上价格仅用于 sub2api 内部记账。

## 模型别名

| 别名 | 原生 mode | 说明 |
| --- | --- | --- |
| `gemini-argon` | 8 | Pro / Ultra 档，默认模型 |
| `gemini-hydrogen` | 1 | FAST 档 |
| `gemini-flash-lite` | 6 | Flash Lite 档 |

其他模型名返回 `UNSUPPORTED_MODEL`。不要在 `gemini_app` 分组里使用裸
`gemini-*` 名称：composite 分组的模型检测会把它们判给 Gemini 平台（三个原生别名
除外，它们已被显式识别为 `gemini_app`）。

思考等级：`thinking_level` 或 `reasoning_effort` 映射到原生 0/1/2/4；
Deep Think（3）是异步模式，本地直接拒绝。

## 账号配置

1. 运维方提供 Gemini App 的 Google OAuth client profile（`clientId` /
   `clientSecret` / `scopes` / `redirectUri`，与 gemini-app2api 的
   `import-profile` 格式一致），通过以下任一方式注入：

   ```
   GEMINI_APP_OAUTH_PROFILE='{"context":2,"enableFst":true,"clientId":"...","clientSecret":"...","scopes":["openid","email","profile"],"redirectUri":"http://localhost:18765"}'
   # 或
   GEMINI_APP_OAUTH_PROFILE_FILE=/path/to/profile.json
   ```

   授权 scope 固定为 `https://www.googleapis.com/auth/flexible-api`；profile 中的
   scopes 仅用于 refresh 请求。

2. 管理端通过这些接口完成授权（路径省略 `/api/v1` 前缀）：

   | 接口 | 作用 |
   | --- | --- |
   | `GET /admin/gemini-app/oauth/capabilities` | 检查 profile 是否已配置，并返回回调地址 |
   | `POST /admin/gemini-app/oauth/auth-url` | 生成授权链接与 `session_id` / `state`（可带 `proxy_id`） |
   | `POST /admin/gemini-app/oauth/exchange-code` | 用粘贴回来的 `code` + `session_id` + `state` 换取 token |
   | `POST /admin/gemini-app/oauth/refresh-token` | 校验已有的 refresh token（可带 `proxy_id`） |

   浏览器完成 Google 登录后，回调指向本机 loopback 地址，浏览器会显示无法连接——复制地址栏
   完整 URL（或其中的 `code`）粘贴回管理端即可。

3. 也可以在新建账号时直接粘贴已有的 `refresh_token`。

账号类型只支持 `oauth`，且必须带 `refresh_token`；后台刷新任务会自动续期
access token，刷新失败会按凭证失败处理并切换账号。

需要重新授权时，在账号操作菜单选「重新授权」：可以走一遍 OAuth 授权码流程，
也可以直接粘贴新的 Refresh Token（Gemini App 标签页），两种方式都会就地更新
该账号的凭证。

## 已知限制

- 不支持图片输入（`UNSUPPORTED_IMAGE_INPUT`）。
- 上游按快照返回文本；若快照身份变化或文本非单调扩展，请求会以
  `NON_MONOTONIC_NATIVE_RESPONSE` 失败而不是拼接错误内容。
- 模型降级会被拒绝：配额类降级返回 429 并切换账号，其他降级返回 422/502。
- 上游没有配额/用量接口：账号用量页只显示 sub2api 本地记录的最近 24 小时请求数、
  Token 与计费金额（`gemini_app_local_usage`），主动查询不会请求上游。
- 账号用量页的额度进度条不适用于该平台。
