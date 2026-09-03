# 多供应商凭据兼容实施计划

> 状态：待讨论、待实施。本文件只记录后续路线，不表示当前 Octopus 已支持这些凭据。

## 目标

让 Octopus 能够安全导入、刷新、持久化并调用 CPA/CLIProxyAPI、AxonHub、
MeowCLI 等系统使用的 xAI/Grok 与 Antigravity 凭据，同时保持现有普通 API Key
渠道行为不变。

兼容性必须分别通过四层验收，不能以其中一层代替端到端能力：

1. 凭据格式解析与严格校验；
2. OAuth 认证、刷新与原子持久化；
3. 账号真实可用模型或项目发现；
4. 使用该账号实际调用所发现的模型。

## 计划能力

- 新增 xAI 专用凭据解析、刷新和持久化，支持明确版本的导入格式。
- 新增 Antigravity 专用凭据结构，不再把 JSON 或组合凭据当作普通 API Key。
- 实现 Google OAuth refresh，并使用并发合并与 compare-and-swap 防止刷新覆盖。
- 实现项目发现、onboard 兜底及 `projectID` 的校验和保存。
- 实现 Antigravity `v1internal` 请求与响应封装。
- 实现 Antigravity 端点 fallback、健康追踪和有界 cooldown。
- 实现 provider-specific 模型发现，区分静态目录与账号真实可用模型。
- 为 CPA、AxonHub、MeowCLI 提供显式格式转换器和导入预览。

## 导入边界

- 完整 JSON、OAuth token、refresh token 和项目标识不得写入日志、RelayLog 或 Git。
- 禁止把整个 JSON 文档直接作为 `Authorization: Bearer ...` 的值。
- 每种来源格式必须先识别 provider、schema 版本和必需字段，再转换为内部结构。
- 未识别、字段冲突、provider 不匹配或超出大小限制的凭据必须拒绝导入。
- 导入预览只显示来源类型、账号备注、项目状态和脱敏字段，不返回 token 原文。
- 刷新后的 token 必须原子写回，并保留来源格式所需的可逆元数据；失败时保留旧凭据。

## 分阶段实施

### 阶段 1：内部模型与导入器

- 定义版本化的 xAI OAuth 与 Antigravity 凭据模型。
- 建立 CPA、AxonHub、MeowCLI 输入格式夹具和脱敏转换测试。
- 增加 dry-run/import preview，明确新增、替换、拒绝和冲突结果。

### 阶段 2：认证生命周期

- 实现 xAI OAuth refresh 和 Google OAuth refresh。
- 增加单凭据并发合并、超时、退避、撤销和 compare-and-swap 持久化。
- 验证数据库、备份、导出和管理 API 不泄露凭据。

### 阶段 3：项目、模型与协议

- 实现 Antigravity 项目发现、onboard 兜底和项目绑定。
- 实现 xAI/Grok 与 Antigravity 的账号真实模型发现。
- 实现 `v1internal` envelope、流式转换、错误分类和端点 fallback。

### 阶段 4：端到端验收

- 在隔离账号和明确计费边界内验证 OAuth 刷新。
- 使用账号真实发现的模型执行最小非流式、流式和工具调用。
- 验证限流、token 撤销、项目缺失、端点故障和刷新竞争下的故障转移。
- 分别记录源码测试、隔离运行时、真实供应商接受和部署版本证据。

## 完成标准

- 每种来源格式都有成功、缺字段、错误 provider、超限和敏感信息回归测试。
- OAuth 刷新不会重复并发请求，不会覆盖更新后的凭据，也不会记录 token。
- 模型列表来自账号真实能力时有明确来源；静态 fallback 必须显式标注。
- 实际调用使用导入后的内部凭据结构，而不是未解析的原始字符串。
- 故障转移保持请求总预算、候选隔离和端点 cooldown 有界，不采用第三方项目的
  `600s` 默认等待值。
