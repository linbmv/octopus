# gpt-5.5 Compact 请求问题：排查记录、0681530 修复与 Codex 方案 C

生成时间：2026-06-07 00:47:24 +0800

## 1. 文档范围

本文记录本次围绕 `gpt-5.5` / `gpt-5.5-openai-compact` 的 Compact 请求问题所做的沟通、排查、当前项目曾经采用的修复方式、当前 `0681530` 提交内容，以及 Codex 提出的长期架构方案 C。

本文重点回答以下问题：

1. 为什么 `gpt-5.5` 正常可用，但 Compact 请求仍然触发超时、502 和熔断。
2. 为什么 `axonhub` 会把 Compact 视为 Responses API 专属。
3. 为什么把渠道设置为 Responses 后，第三方中转站仍然无法处理。
4. `0681530` 到底改了什么，解决了什么，没有解决什么。
5. Codex 方案 C 的详细设计、收益、风险和实施边界。
6. 在“稳定第一、性能第二、扩展维护第三”的准则下，当前应保留哪个方案。

## 2. 证据来源

- 当前仓库 HEAD：`0681530831c08cea7a20d6bf891ebea2bc514053`。
- 当前分支状态：`dev`，且 `origin/dev` 指向同一提交。
- 关键提交：`0681530 fix: :bug: 修复 Compact 请求在 OpenAI Chat 渠道上的兼容性问题`。
- 相关代码：
  - `internal/server/server.go:58-62`：Octopus 已注册 `/v1/responses/compact` 入口。
  - `internal/relay/transformers.go:78-91`：当前 `0681530` 的 Compact 出站选择逻辑。
  - `internal/relay/transformers_test.go:126-131`：Compact 请求兼容性测试覆盖。
- 历史排查文档：`.claude/troubleshooting/compact-request-routing.md`，本文已对其进行更新和扩展。
- 会话记录来源：`C:\Users\LinHome\.claude\projects\c--Users-LinHome-Documents-Github-octopus\699c92f7-2dd5-4ae0-b03c-fd37cac158e6.jsonl`。

## 3. 用户沟通记录摘要

> 说明：本节按问题推进顺序整理，不逐字还原全部对话，只保留影响技术决策的关键交流点。

### 3.1 起始问题：gpt-5.5 渠道可用，但 Compact 失败

用户提供的日志显示普通 `gpt-5.5` 渠道可以请求成功，但上下文自动压缩时失败：

```text
Context automatically compacted
Error running remote compact task: unexpected status 502 Bad Gateway: {"code":502,"message":"all channels failed"}, url: http://sgw:801/v1/responses/compact
```

同时 relay 日志出现熔断相关信息，例如：

```text
circuit breaker [23:40:gpt-5.5] HalfOpen -> Open (probe failed, tripCount=3, cooldown=4m0s)
relay complete: model=gpt-5.5 ... final_status=failed ... attempts=2
```

结论：问题不是 `gpt-5.5` 模型本身不可用，而是 Compact 请求链路失败导致连续失败，并进一步触发熔断。

### 3.2 用户确认：是否只需把渠道类型改成 OpenAI Chat

用户问：

```text
所以现在先简单的将渠道类型改为openai chat就可以解决问题？
```

回答结论：对当前第三方中转站场景，是的。因为这些中转站通常支持标准 OpenAI Chat Completions 端点：

```text
/v1/chat/completions
```

但不支持 Responses Compact 端点：

```text
/v1/responses/compact
```

所以对第三方中转站，把渠道视为 `OpenAI Chat`，并让 Octopus 在 Compact 请求时降级到 Chat 端点，是当前最稳定的路径。

### 3.3 用户追问：为什么 axonhub 认为 Compact 是 Responses API 专属

用户问：

```text
为什么axonhub 会认为 Compact 是 Responses API 专属？而我将渠道设置成Responses却无法处理？
```

回答结论：

1. 从 OpenAI API 设计看，Compact 是 Responses API 体系中的能力，和传统 Chat Completions API 不是同一个端点体系。
2. `axonhub` 按官方接口边界实现：
   - `openai` transformer 处理 Chat Completions。
   - `responses` transformer 处理 Responses / Compact。
3. 因此 `axonhub` 的 `openai` transformer 会拒绝 `RequestTypeCompact`。
4. 但将 Octopus 渠道设置为 Responses 后，出站会走 `/v1/responses/compact`，第三方中转站仍然不支持该端点，所以仍会失败。

### 3.4 用户提出核心架构疑问

用户提出两个关键问题：

```text
1、如果上游中转支持，而我们这样修改不是不能享受这专属？？
2、如果上游中转不支持，为什么在octopus中不能转换？
```

这两个问题成立，并引出了长期方案 C：

- 如果上游真的支持 Responses API，强制走 Chat 降级会浪费 Responses Compact 的专属能力。
- 如果上游不支持 Responses API，Octopus 应该承担协议转换或降级，而不是让用户手动猜测。

### 3.5 用户同意先修，但随后指出过度工程

用户同意先处理问题：

```text
同意
```

随后因实现过程中开始扩大修改范围，用户指出：

```text
你浪费太多时间了？？！！！
```

最终决策：停止扩展式改造，回滚不必要改动，保留 `0681530` 的简单修复路径。

### 3.6 用户询问 Compact 模型和专门分组是否有用

用户问：

```text
如果上游中转站不支持，为什么还有compact模型？？？如果新建一个分组，专门放置这些compact模型的话，能解决问题吗？
```

回答结论：

- `gpt-5.5-openai-compact` 不是一个真实上游模型，而是客户端或路由体系中带有 Compact 语义的模型名变体。
- 根因不是“模型名是否存在”，而是“出站端点是否被上游支持”。
- 新建专门的 Compact 分组不能解决根因，因为只要最终仍发往 `/v1/responses/compact`，不支持该端点的中转站仍然会失败。
- 专门分组只能用于隔离、观测或策略调度，不能替代端点转换。

### 3.7 用户追问：Octopus 是否不支持 `/responses/compact`

用户问：

```text
如果不是模型，而是端口的问题，我想之前有个commit已经解决了？这不是中转的问题，而是octopus的问题，octopus不支持/responses/compact？？？为什么不让octopus支持/responses/compact？？？？？？
```

回答结论：

- Octopus 已支持入口路由：`internal/server/server.go:62` 注册了 `/v1/responses/compact`。
- 真正问题在出站：Octopus 收到 `/v1/responses/compact` 后，需要根据目标渠道能力选择继续走 Responses，还是转换/降级到 Chat。
- `0681530` 做的就是：当目标渠道是 `OpenAI Chat` 时，把 Compact 请求转给 `openai` transformer，并将请求类型改为 Chat，使出站改走 `/v1/chat/completions`。

### 3.8 用户问：`0681530` 与方案 C 哪个最优

用户问：

```text
0681530与方案c哪个最优解？
```

当时给出的决策：

- 纯架构长期最优：方案 C。
- 以当前用户真实场景和准则“稳定第一、性能第二、扩展维护第三”衡量：保留 `0681530`，暂不做方案 C。

原因：

1. `0681530` 改动极小，风险最低。
2. 当前 `gpt-5.5` 渠道是第三方中转站，目标就是绕开不支持的 `/responses/compact`，不损失实际可用能力。
3. 方案 C 需要探测、缓存、错误分类、前端字段、迁移和测试，复杂度高，容易引入新故障。
4. 方案 C 的主要收益是“用户不用理解上游能力差异”，但当前项目主要使用者已经理解该差异，为此付出复杂度成本不划算。

### 3.9 用户问：`dadddb8` 是否包含 `0681530` 功能

历史上用户问：

```text
我现在使用的是dadddb8。这包含0681530的功能吗？
```

本次重新检查的结论：

- `dadddb8` 是一个历史提交：`fix: :bug: compact 请求转发给 OpenAI Chat 渠道时改用标准端点`。
- `dadddb8` 与 `0681530` 不是祖先包含关系；`git merge-base --is-ancestor 0681530 dadddb8` 结果为否。
- 因此不能说 `dadddb8` “包含 commit 0681530”。
- 但从提交说明看，`dadddb8` 包含同类核心行为：Compact 请求转发给 OpenAI Chat 渠道时改走标准 Chat 端点。
- 当前仓库的规范修复点以 `0681530` 为准。

## 4. 问题现象与错误链路

### 4.1 外部表现

1. 普通 `gpt-5.5` 对话请求可用。
2. Claude Code 触发上下文压缩时请求失败。
3. 客户端看到 502：

```text
unexpected status 502 Bad Gateway: {"code":502,"message":"all channels failed"}
url: http://sgw:801/v1/responses/compact
```

4. Octopus 内部连续失败后，熔断器打开，导致后续请求被跳过或等待冷却。

### 4.2 初始误判

早期怀疑过：

- 模型名改写没有生效。
- `gpt-5.5-openai-compact` 被原样发给中转站。
- 分组后缀回退失败。
- Raw passthrough 干扰了模型名或请求体。

后续排查否定了这些假设。

### 4.3 实际根因

实际根因是出站端点错误：

```text
客户端 Compact 请求
  ↓
Octopus 入站 /v1/responses/compact
  ↓
内部识别为 RequestTypeCompact
  ↓
旧逻辑对 OpenAI Chat 渠道仍选择 responses transformer
  ↓
出站发往第三方中转站 /v1/responses/compact
  ↓
第三方中转站不支持该端点
  ↓
502 / 400 / 503
  ↓
连续失败触发熔断
```

### 4.4 为什么普通请求可用但 Compact 不可用

普通请求走：

```text
/v1/chat/completions
```

Compact 请求在旧逻辑下走：

```text
/v1/responses/compact
```

第三方中转站通常只兼容标准 OpenAI Chat Completions API，而不支持 OpenAI Responses Compact 端点。因此同一个 `gpt-5.5` 模型会出现“普通请求可用、Compact 请求失败”的现象。

## 5. `axonhub` 的角色与限制

### 5.1 两套 API 的边界

OpenAI 相关接口在这里涉及两套体系：

| 体系 | 常见端点 | 用途 | 中转站兼容性 |
|---|---|---|---|
| Chat Completions API | `/v1/chat/completions` | 普通聊天、文本生成 | 高 |
| Responses API | `/v1/responses`、`/v1/responses/compact` | 新式响应、会话管理、Compact | 低，很多第三方中转不支持 |

`axonhub` 按这两套 API 的官方边界实现 transformer，因此：

- `openai.NewOutboundTransformer` 主要面向 Chat Completions。
- `responses.NewOutboundTransformer` 面向 Responses API。
- `RequestTypeCompact` 被放入 Responses API 处理路径。

### 5.2 为什么 OpenAI Chat transformer 会拒绝 Compact

`axonhub` 的设计意图是避免把 Responses Compact 请求误发给 Chat Completions transformer。这个设计对官方 API 边界是合理的，但对第三方中转兼容场景不够灵活。

因此，Octopus 如果要兼容第三方中转，就必须在自己的 relay 层做适配：

- 对支持 Responses API 的渠道，保留 `/v1/responses/compact`。
- 对只支持 Chat Completions 的渠道，降级到 `/v1/chat/completions`。

## 6. 当前项目曾经如何解决

### 6.1 历史文档中的 `dadddb8`

历史文档记录过一个早期修复提交：

```text
dadddb8 fix: :bug: compact 请求转发给 OpenAI Chat 渠道时改用标准端点
```

它的目标是让 OpenAI Chat 类型渠道在处理 Compact 请求时，不再走 `/v1/responses/compact`，而是改走 `/v1/chat/completions`。

### 6.2 当前 HEAD 的 `0681530`

当前仓库 HEAD 为：

```text
0681530831c08cea7a20d6bf891ebea2bc514053
```

提交信息：

```text
fix: :bug: 修复 Compact 请求在 OpenAI Chat 渠道上的兼容性问题
```

提交摘要：

```text
internal/relay/transformers.go | 5 +++++
```

### 6.3 `0681530` 的核心补丁

`0681530` 在 `internal/relay/transformers.go` 的 `RequestTypeCompact` 分支中加入了 OpenAI Chat 降级处理：

```go
case llm.RequestTypeCompact:
    switch channelType {
    case llm.APIFormatOpenAIChatCompletion:
        // compact 请求转发给 OpenAI Chat 渠道时，用 /v1/chat/completions 端点（中转站不支持 /v1/responses/compact）
        // axonhub 的 openai transformer 会检查 RequestType，拒绝 Compact 类型
        // 临时解决：将请求类型改为 Chat，让 transformer 认为这是普通对话请求
        if request != nil {
            request.RequestType = llm.RequestTypeChat
        }
        return openai.NewOutboundTransformer(baseURL, key)
    case llm.APIFormatOpenAIResponse,
        llm.APIFormatOpenAIResponseCompact:
        // 只有渠道本身支持 responses API 时，才用 /v1/responses/compact 端点
        return responses.NewOutboundTransformer(baseURL, key)
    default:
        return nil, fmt.Errorf("channel type %s is not compatible with %s request", channelType, requestType)
    }
```

### 6.4 `0681530` 解决了什么

`0681530` 解决的是当前最直接的失败链路：

```text
OpenAI Chat 类型渠道 + Compact 请求
```

由旧路径：

```text
RequestTypeCompact → responses.NewOutboundTransformer → /v1/responses/compact
```

改为新路径：

```text
RequestTypeCompact + OpenAI Chat channel
  → request.RequestType = Chat
  → openai.NewOutboundTransformer
  → /v1/chat/completions
```

效果：

1. 第三方中转站可以处理请求。
2. 不再因为 `/v1/responses/compact` 不支持而 502。
3. 减少连续 Compact 失败导致的熔断。
4. 保留 OpenAI Response 类型渠道使用 Responses Compact 的能力。

### 6.5 `0681530` 没有解决什么

`0681530` 不是完整的长期架构方案，它仍有已知边界：

1. **没有自动探测上游是否支持 Responses API**。
2. **没有缓存上游能力结果**。
3. **没有前端展示 Responses API 能力状态**。
4. **没有把用户策略与探测状态拆开**。

### 6.6 `0681530` 的遗留风险已于 2026-06-07 修复

`0681530` 原实现存在请求对象原地修改隐患：

```go
request.RequestType = llm.RequestTypeChat
```

该修改会污染同一个 relay run 内后续渠道重试共享的 `llm.Request`。

**实际影响范围**：

- 如果同一个模型分组里全部是 OpenAI Chat 类型渠道，风险很低。
- 如果同一个分组里混有 OpenAI Chat 和 OpenAI Response 类型渠道，且 Chat 渠道先失败，则后续 Response 渠道可能看到已被改成 Chat 的请求类型，从而无法走 Responses Compact 路径。

**修复方式**：

1. 在 relay pipeline 入口为每次 attempt 构造请求副本。
2. OpenAI Chat 渠道的 Compact 降级只改副本的 `RequestType`，不污染共享请求。
3. 后续 OpenAI Response 渠道重试仍能看到 `RequestTypeCompact`。

**修复文件**：

- `internal/relay/relay.go`: 新增 `requestForOutboundPipeline` 函数。
- `internal/relay/transformers.go`: 去掉 `request.RequestType = Chat` 原地修改。
- `internal/relay/transformers_test.go`: 补充 4 个回归测试，覆盖：
  - `newOutbound` 不再原地修改请求。
  - Chat 降级不污染 Response 重试。
  - `requestForOutboundPipeline` 对 Chat 渠道返回降级副本。
  - `requestForOutboundPipeline` 对 Response 渠道保持 Compact。

**验证结果**：

```bash
go test ./internal/relay -run 'TestNewOutboundCompact|TestRequestForOutboundPipeline'
go test ./internal/relay ./internal/server/...
go test ./...
go build ./...
```

全部通过 ✅

## 7. 当前配置建议

### 7.1 第三方中转站

对于 `muyuan.do`、`wzw.pp.ua`、`anyrouter`、`linuxdo` 等只稳定支持 Chat Completions 的中转站，建议：

```text
渠道类型：OpenAI Chat
Compact 请求：由 0681530 降级到 /v1/chat/completions
```

不要为了 `compact` 后缀单独把渠道类型改成 Responses，除非已经验证该上游确实支持 `/v1/responses/compact`。

### 7.2 官方 OpenAI 或明确支持 Responses API 的上游

如果上游明确支持 Responses API，可以选择：

```text
渠道类型：OpenAI Response 或 OpenAI Response Compact
Compact 请求：走 /v1/responses/compact
```

这样可以保留 Responses Compact 的专属能力。

### 7.3 是否需要专门的 Compact 分组

专门分组不能解决端点兼容问题。

可用于：

- 隔离 Compact 请求流量。
- 单独观察 Compact 成功率。
- 将 Compact 请求绑定到更稳定或更便宜的渠道。

不能用于：

- 让不支持 `/v1/responses/compact` 的中转站突然支持该端点。
- 替代 Octopus 的协议转换。

## 8. Codex 制定的方案 C

### 8.1 Codex 的核心判断

Codex 指出：当前设计的核心问题不是单纯“是否支持 Compact”，而是 `Channel.Type` 同时承担了两个职责：

1. 协议格式选择。
2. 上游能力声明。

这会迫使用户在创建渠道时手动判断上游是否支持 Responses API，而很多用户并不知道第三方中转站实际支持哪些端点。

Codex 同时指出一个实现风险：当前 `0681530` 会原地修改共享的 `llm.Request`，多渠道重试时可能污染后续渠道。

### 8.2 方案 C 的字段设计

方案 C 建议把“用户策略”和“探测状态”拆开：

```text
responses_api_mode:
  auto      默认，自动探测并缓存
  enabled   强制使用 Responses API，不自动降级
  disabled  强制使用 Chat API 降级路径

responses_api_status:
  unknown
  supported
  unsupported
  error/transient
```

推荐模型字段：

```go
type Channel struct {
    // 现有字段省略
    ResponsesAPIMode      string    `json:"responses_api_mode"`
    ResponsesAPIStatus    string    `json:"responses_api_status"`
    ResponsesAPICheckedAt time.Time `json:"responses_api_checked_at"`
    ResponsesAPILastError string    `json:"responses_api_last_error"`
}
```

### 8.3 方案 C 的路由策略

```text
Compact 请求到达
  ↓
读取 ResponsesAPIMode
  ↓
├─ enabled
│    └─ 强制走 /v1/responses/compact，失败就按真实失败处理
│
├─ disabled
│    └─ 强制走 /v1/chat/completions
│
└─ auto
     ↓
     读取 ResponsesAPIStatus 缓存
     ↓
     ├─ supported
     │    └─ 走 /v1/responses/compact
     │
     ├─ unsupported
     │    └─ 走 /v1/chat/completions
     │
     └─ unknown / expired
          ↓
          先尝试 /v1/responses/compact
          ↓
          ├─ 明确端点不支持
          │    ├─ 降级到 /v1/chat/completions
          │    └─ 缓存 unsupported
          │
          ├─ 成功
          │    └─ 缓存 supported
          │
          └─ 鉴权、限流、超时、临时 5xx
               └─ 不缓存为 unsupported，按 transient/error 处理
```

### 8.4 自动降级条件

Codex 建议的 fallback 条件：

可以降级并缓存为 `unsupported`：

```text
404
405
501
明确文本：unknown endpoint / route not found / cannot POST /responses/compact
```

需要谨慎处理：

```text
502
```

原因：很多中转站会用 502 表示路由不支持，但 502 也可能代表临时上游故障。因此只有当响应内容明确指向端点不支持时，才应缓存为 `unsupported`。

不应降级或不应缓存为 `unsupported`：

```text
401 / 403：认证或权限问题
429：限流
超时：网络或上游临时问题
普通 5xx：可能是临时故障
```

### 8.5 方案 C 的前端 UI

建议在渠道表单中加入策略选择：

```tsx
<Select label="Responses API 模式">
    <Option value="auto">自动探测（推荐）</Option>
    <Option value="enabled">强制使用 Responses API</Option>
    <Option value="disabled">强制使用 Chat 降级</Option>
</Select>
```

状态展示：

```tsx
<Badge>
    {status === 'supported' && '支持 Responses API'}
    {status === 'unsupported' && '不支持，使用 Chat 降级'}
    {status === 'unknown' && '未探测'}
    {status === 'error' && '探测异常'}
</Badge>
```

### 8.6 方案 C 的实施步骤

Codex 建议分阶段实施：

#### 第 1 步：修复请求污染

目标：避免在多渠道重试时原地修改共享 `llm.Request`。

方向：为每次渠道尝试构造请求副本，或在 transformer 前形成每个 attempt 独立的 request view。

关键点：仅浅拷贝 `llm.Request` 可能不够，如果内部有切片、map 或 metadata，需确认是否会被 transformer 修改；如会修改，应做必要深拷贝。

#### 第 2 步：添加 Responses API 策略字段

新增：

```text
responses_api_mode
responses_api_status
responses_api_checked_at
responses_api_last_error
```

并补齐：

- 数据库迁移。
- 后端 API 类型。
- 前端表单字段。
- 默认值：`auto`。

#### 第 3 步：实现自动探测和缓存

要求：

- 探测失败不能直接计入正常渠道失败，避免误触发熔断。
- 只有明确端点不支持时才缓存 `unsupported`。
- 支持手动清除或重新探测。
- 缓存应有失效策略，避免上游能力变化后永久错误。

#### 第 4 步：完善观测和日志

需要记录：

- 当前 mode。
- 当前 status。
- 本次选择的出站端点。
- 是否发生降级。
- 探测失败原因。
- 缓存命中情况。

#### 第 5 步：测试覆盖

至少覆盖：

1. `auto + unknown + responses 成功` → 缓存 supported。
2. `auto + unknown + 404` → 降级 Chat，缓存 unsupported。
3. `auto + unknown + 502 且无明确端点错误` → 不缓存 unsupported。
4. `enabled` → 强制 Responses，不降级。
5. `disabled` → 强制 Chat。
6. 多渠道重试时 request 不被污染。
7. 探测失败不触发熔断或不计入普通失败次数。

### 8.7 方案 C 的优缺点

优点：

1. 用户体验最好，默认自动探测。
2. 官方 OpenAI 或支持 Responses 的上游能享受专属 Compact 能力。
3. 第三方中转站可以自动降级到 Chat。
4. 缓存能力结果后，避免每次请求都先撞失败端点。
5. 将“协议格式”和“上游能力”拆开，长期更清晰。

缺点：

1. 实现复杂度最高。
2. 需要错误分类，尤其 502 语义不稳定。
3. 需要缓存失效和手动重探测机制。
4. 需要后端、前端、迁移、测试一起改。
5. 如果处理不好，可能引入比当前问题更难排查的探测误判。

## 9. `0681530` 与方案 C 对比

| 维度 | `0681530` | 方案 C |
|---|---|---|
| 改动规模 | 极小，仅 transformer 分支增加处理 | 大，涉及模型字段、迁移、relay、前端、测试 |
| 当前 gpt-5.5 中转站可用性 | 可解决 | 可解决 |
| 对官方 Responses Compact 的利用 | 需要用户手动选择 OpenAI Response 类型 | 可自动优先使用 |
| 用户配置复杂度 | 用户需知道渠道类型含义 | 默认自动探测，用户可强制指定 |
| 稳定性 | 高，逻辑简单 | 中等，取决于探测与缓存实现质量 |
| 性能 | 高，无额外探测 | 首次可能多一次探测，缓存后可接受 |
| 可维护性 | 短期好，长期能力表达不足 | 长期好，但维护面更大 |
| 已知风险 | 请求对象原地修改 | 错误分类、缓存失效、探测误判 |
| 当前推荐 | 保留 | 作为后续独立任务规划 |

## 10. 当前决策

在用户明确的优先级下：

```text
稳定第一 > 性能第二 > 扩展维护第三
```

当前推荐：

```text
保留 0681530，不立即实施方案 C。
```

理由：

1. 当前真实问题是第三方中转不支持 `/v1/responses/compact`。
2. `0681530` 已将 OpenAI Chat 渠道的 Compact 请求降级到 `/v1/chat/completions`。
3. 当前用户已理解上游能力差异，不需要马上为“自动判断”引入探测系统。
4. 方案 C 是长期更完整的架构，但当前实施会增加故障面。
5. 如果后续需要面向更多用户或接入官方 Responses 能力，再单独按方案 C 开发。

## 11. 验证与运维建议

### 11.1 代码验证

与该链路直接相关的验证命令：

```bash
go test ./internal/relay -run TestNewOutboundKeepsCurrentCompactCompatibility
go test ./internal/relay
go test ./internal/server/...
go build ./...
```

如果修改 API route 或 relay 完整链路，还应检查：

```text
route 注册
→ inbound transformer
→ internal request type
→ outbound transformer
→ tests
→ build
```

### 11.2 运行时验证

重启 Octopus 后，触发 Claude Code 上下文压缩，观察 relay 日志：

期望看到：

```text
Compact 请求命中 gpt-5.5 分组
OpenAI Chat 类型渠道出站
最终请求成功
不再出现 /v1/responses/compact 上游 502
不再连续触发 circuit breaker
```

失败时重点看：

1. 出站 URL 是否仍是 `/v1/responses/compact`。
2. 渠道类型是否被配置成 OpenAI Response。
3. 上游是否真的支持 Responses API。
4. 是否存在熔断冷却状态未清理。
5. 同一分组是否混用了 OpenAI Chat 与 OpenAI Response 类型渠道。

### 11.3 熔断处理

如果 Compact 失败已经触发熔断，即使修复代码后也可能短时间继续失败。处理方式：

1. 重启 Octopus，清理内存中的熔断状态。
2. 或等待冷却时间结束。
3. 再触发新的 Compact 请求验证。

## 12. 后续可执行任务清单

### 12.1 低风险短期任务（✅ 已于 2026-06-07 完成）

1. ✅ 为 `0681530` 的请求对象原地修改补测试，明确当前风险边界。
2. ✅ 检查是否能在 relay attempt 层为每次渠道尝试复制 `llm.Request`。
3. ✅ 避免 Chat 降级污染后续 Response 渠道。

**实施结果**：

- 新增 `requestForOutboundPipeline` 函数，为每次 attempt 构造请求副本。
- OpenAI Chat 渠道的 Compact 降级只改副本，不污染共享请求。
- 补充 4 个回归测试，覆盖 Chat/Response 混合重试场景。
- 全部测试和构建通过。

### 12.2 中期任务

1. 将“是否支持 Responses API”从 `Channel.Type` 中拆出来。
2. 先做显式配置，不做自动探测：`auto` 可先保留但不启用。
3. 前端展示当前模式和实际路由端点。

### 12.3 长期任务：方案 C

按完整方案 C 实施：

1. `responses_api_mode`。
2. `responses_api_status`。
3. 自动探测。
4. 缓存与失效。
5. 状态展示。
6. 降级观测。
7. 完整测试矩阵。

## 13. 最终结论

1. `gpt-5.5-openai-compact` 不是独立真实模型，核心是 Compact 请求类型和端点选择。
2. Octopus 已支持 `/v1/responses/compact` 入站路由，问题在出站到第三方中转站时端点不兼容。
3. 第三方中转站通常支持 `/v1/chat/completions`，不支持 `/v1/responses/compact`。
4. `0681530` 的当前修复让 OpenAI Chat 渠道上的 Compact 请求改走 Chat 端点，因此能解决当前 gpt-5.5 Compact 失败问题。
5. `0681530` 的原地修改共享 request 遗留风险已于 2026-06-07 修复；当前实现为每次 attempt 构造请求副本，Chat 降级不再污染 Response 重试。
6. Codex 方案 C 是长期架构最完整方案，但复杂度明显更高。
7. 按当前”稳定第一、性能第二、扩展维护第三”的准则，应保留当前修复，把方案 C 作为独立后续计划，而不是立即实施。

---

## 14. 2026-06-07 修复摘要

**修复内容**：Compact 降级不再原地修改共享请求

**修复文件**：

| 文件 | 说明 |
|---|---|
| [relay.go](../../internal/relay/relay.go) | 新增 `requestForOutboundPipeline`，pipeline 入口使用 attempt 请求副本 |
| [transformers.go](../../internal/relay/transformers.go) | 去掉 `request.RequestType = Chat` 原地修改 |
| [transformers_test.go](../../internal/relay/transformers_test.go) | 补充 4 个回归测试，覆盖 Chat/Response 混合重试 |

**验证结果**：

```bash
go test ./internal/relay -run 'TestNewOutboundCompact|TestRequestForOutboundPipeline'  ✅
go test ./internal/relay ./internal/server/...  ✅
go test ./...  ✅
go build ./...  ✅
```

**影响范围**：

- OpenAI Chat 渠道仍正常降级 Compact 到 Chat 端点。
- Chat/Response 混合分组重试时，后续 Response 渠道不再受 Chat 降级污染。
- 第三方中转站 Compact 请求继续可用。
- 官方 OpenAI 或支持 Responses API 的上游可以正常使用 `/v1/responses/compact`。

**配置建议不变**：

- 第三方中转站：渠道类型设置为 OpenAI Chat。
- 官方 OpenAI 或明确支持 Responses API 的上游：渠道类型设置为 OpenAI Response。