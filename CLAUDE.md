# Octopus 交付校验规则

## 禁止事项

- 禁止在未验证远端 commit 的情况下声称“已推送”。
- 禁止在未运行相关测试和 `go build ./...` 的情况下声称“已修复”。
- 禁止使用 `git add -A` 或 `git add .`，除非用户明确要求“全部提交”。
- 禁止只新增 route 就声称 API 完整支持；必须检查完整链路。

## 推送校验

每次推送后必须执行：

```bash
git rev-parse HEAD
git ls-remote origin refs/heads/dev
```

只有本地 `HEAD` 与远端 `refs/heads/dev` hash 一致时，才能说“已推送成功”。

## API 改动校验

每次新增或修改 API route 后，必须检查完整链路：

```text
route 注册
→ inbound transformer
→ internal request type
→ outbound transformer
→ tests
→ build
```

至少运行相关包测试和全量构建，例如：

```bash
go test ./internal/relay
go test ./internal/server/...
go build ./...
```

如果只完成其中一部分，必须明确说“部分修复”，不能说“已修复完成”。

## 提交范围规则

提交前必须先查看状态：

```bash
git status --short
```

只能暂存本次任务明确相关的文件，例如：

```bash
git add internal/relay/transformers.go internal/relay/transformers_test.go
```

不得把 `.claude/`、临时文件、worktree、日志或无关改动纳入提交，除非用户明确要求。

## 交付输出格式

每次完成代码修改、提交或推送后，必须给出证据块：

```text
本地验证：
- <命令> ✅/❌

Git 校验：
- 本地 HEAD: <hash>
- 远端 dev: <hash>
- 是否一致: 是/否

变更链路：
- route: 已检查/不涉及
- inbound: 已检查/不涉及
- outbound: 已检查/不涉及
- tests: 已覆盖/说明原因
```

没有证据块，不得声称交付完成。
