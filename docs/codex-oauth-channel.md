# Codex OAuth 渠道使用说明

Octopus 的 `openai/codex` 是专用渠道适配器，不是把整份 JSON 当作普通 API Key
发送给上游。适配器只取出 OAuth token，按 Codex Responses 协议构造请求，并处理
Codex 要求的认证头、账户 ID、流式参数和 token 刷新。

## 新建渠道

1. 在渠道页面选择 **Codex OAuth**。
2. Base URL 会固定为 `https://chatgpt.com/backend-api/codex`，不能改成中转域名，
   以免 OAuth bearer token 被发送到非官方主机。
3. 点击 **导入 JSON** 选择本机 `.json` 文件，或将完整内容粘贴到
   **Codex OAuth JSON**。导入控件只在浏览器本地读取文件并填入表单，不会单独上传
   文件或保存本地路径。单份凭据上限为 8 KiB。不要只复制 `access_token`，也不要
   把这份 JSON 配成客户端调用 Octopus 时使用的 API Key。
4. 打开该条渠道密钥右侧的启用开关，刷新模型并选择需要暴露的 Codex 模型。
5. 保存渠道，再把它加入相应分组。客户端仍使用 Octopus 自己签发的 API Key 调用
   `/v1/responses` 或 `/v1/chat/completions`。

支持两类导入格式：CLIProxyAPI/MeowCLI 风格的顶层 `access_token`、
`refresh_token`、`id_token`，以及 Codex `auth.json` 风格的 `tokens` 对象。JSON 中
未知的元数据会保留。外部文件里的 `disabled` 也会原样保留，但 Octopus 实际是否
选用该凭据，以渠道密钥右侧的 **启用** 开关为准。

## Responses 附件 JSON

下面的 `OCTOPUS_API_KEY` 是 Octopus 客户端密钥，不是 Codex OAuth JSON：

```bash
curl http://127.0.0.1:8080/v1/responses \
  -H 'Authorization: Bearer OCTOPUS_API_KEY' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-5.3-codex",
    "input": [{
      "type": "message",
      "role": "user",
      "content": [
        {"type": "input_text", "text": "请分析附件"},
        {
          "type": "input_file",
          "filename": "report.pdf",
          "file_data": "data:application/pdf;base64,JVBERi0..."
        }
      ]
    }]
  }'
```

如果文件已经由兼容上游保存，也可在 `input_file` 中传 `file_id`。通过
OpenAI Chat 入口调用时，Octopus 也会把 `messages[].content[]` 中的 `file`
部分恢复为 Codex Responses 的 `input_file`：

```json
{
  "model": "gpt-5.3-codex",
  "messages": [{
    "role": "user",
    "content": [
      {"type": "text", "text": "请分析附件"},
      {
        "type": "file",
        "file": {
          "filename": "report.pdf",
          "file_data": "data:application/pdf;base64,JVBERi0..."
        }
      }
    ]
  }]
}
```

附件会计入 Octopus 的 JSON 请求体上限。请按需要调整
`relay.max_json_request_bytes`，但不要无界放大。

## Token 刷新与安全边界

- 后台 worker 按凭据中的 `expired`、`expires_at`、access-token JWT `exp` 或
  `last_refresh` 计算到期时间，并在到期前 5 分钟自动刷新。应用启动时立即扫描，
  新增、启用或替换的凭据最多 1 分钟被发现；失败后退避 5 分钟再试。
- 请求路径仍会在到期前 3 分钟同步检查，作为后台 worker 停止、时钟偏差或新导入
  凭据尚未扫描时的兜底。同一凭据的并发刷新只会实际请求一次。
- 刷新通过渠道自己的代理访问 `https://auth.openai.com/oauth/token`。这是 access
  token/refresh token 的实际 OAuth 轮换，不是 ChatGPT 套餐或账号有效期的自动续费；
  refresh token 被撤销或失效后仍需重新登录并导入新凭据。
- 新 token 会先以 compare-and-swap 方式持久化到数据库，再切换内存凭据；管理员
  同时替换渠道 JSON 时，后台刷新不会覆盖管理员的新值。
- 刷新器不把含 refresh token 的请求交给通用调试日志层，也不会把 OAuth JSON
  放进请求诊断 artifact。
- 模型列表来自 Codex 适配器的静态目录，不会把整份 OAuth JSON 发送到 `/models`。
- 数据库及其备份会包含这份 OAuth JSON，应按高敏感凭据保存；一旦泄露，请在
  OpenAI 侧撤销或轮换后再更新渠道。
