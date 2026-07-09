package relay

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

// TestOpenAIInboundToAnthropicOutbound 验证你描述的场景：
// 客户端用 OpenAI Chat Completions 格式发请求 -> 渠道类型是 Anthropic
// 期望：Octopus 内部把 OpenAI 格式转成 Anthropic Messages 格式（带 max_tokens、system 提取等）
func TestOpenAIInboundToAnthropicOutbound(t *testing.T) {
	// 1. 客户端发来的 OpenAI 格式请求
	openaiBody := `{
		"model": "claude-opus-4-8",
		"messages": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "Hello"}
		],
		"max_tokens": 100,
		"stream": true
	}`

	inbound := newInbound(llm.APIFormatOpenAIChatCompletion)
	if inbound == nil {
		t.Fatal("newInbound(OpenAI) returned nil")
	}

	// 2. inbound: OpenAI HTTP -> 内部 llm.Request
	llmReq, err := inbound.TransformRequest(context.Background(), &httpclient.Request{
		Method:      "POST",
		Path:        "/v1/chat/completions",
		ContentType: "application/json",
		Headers:     map[string][]string{"Content-Type": {"application/json"}},
		Body:        []byte(openaiBody),
	})
	if err != nil {
		t.Fatalf("inbound.TransformRequest error: %v", err)
	}
	t.Logf("内部 llm.Request: model=%s, messages=%d, stream=%v",
		llmReq.Model, len(llmReq.Messages), llmReq.Stream)

	// 3. outbound: 内部 llm.Request -> Anthropic HTTP 格式 (渠道类型 = Anthropic)
	outbound, err := newOutbound(llm.APIFormatAnthropicMessage, llmReq, testBaseURL, testAPIKey)
	if err != nil {
		t.Fatalf("newOutbound(Anthropic) error: %v", err)
	}
	if outbound == nil {
		t.Fatal("newOutbound(Anthropic) returned nil")
	}

	httpReq, err := outbound.TransformRequest(context.Background(), llmReq)
	if err != nil {
		t.Fatalf("outbound.TransformRequest error: %v", err)
	}

	t.Logf("出站 URL: %s", httpReq.URL)
	t.Logf("出站 Body: %s", string(httpReq.Body))

	// 4. 验证出站 body 是 Anthropic 格式
	var body map[string]any
	if err := json.Unmarshal(httpReq.Body, &body); err != nil {
		t.Fatalf("出站 body 不是合法 JSON: %v", err)
	}

	// Anthropic 格式关键校验点：
	// (a) max_tokens 必须存在（Anthropic 强制要求）
	if _, ok := body["max_tokens"]; !ok {
		t.Errorf("❌ 出站 body 缺少 max_tokens（Anthropic 必需字段）")
	} else {
		t.Logf("✅ max_tokens = %v", body["max_tokens"])
	}

	// (b) system 应该被提取为顶层字段（Anthropic 格式），不在 messages 里
	if sys, ok := body["system"]; ok {
		t.Logf("✅ system 被提取为顶层字段: %v", sys)
	} else {
		t.Logf("⚠️ system 未提取为顶层字段（检查 messages 里是否还有 system role）")
	}

	// (c) messages 里不应该再有 system role（Anthropic 不接受 messages 里的 system）
	if msgs, ok := body["messages"].([]any); ok {
		for i, m := range msgs {
			if mm, ok := m.(map[string]any); ok {
				if mm["role"] == "system" {
					t.Errorf("❌ messages[%d] 仍是 system role，Anthropic 格式不接受", i)
				}
			}
		}
		t.Logf("✅ messages 数量: %d", len(msgs))
	}

	// (d) URL 应该指向 Anthropic 的 /messages 端点
	t.Logf("URL 检查: %s (期望包含 /messages)", httpReq.URL)
}

// TestAnthropicInboundToOpenAIOutbound 验证反向转换：
// 客户端用 Anthropic Messages 格式发请求 -> 渠道类型是 OpenAI
// 期望：Octopus 把 Anthropic 格式转成 OpenAI Chat Completions 格式
func TestAnthropicInboundToOpenAIOutbound(t *testing.T) {
	// 1. 客户端发来的 Anthropic 格式请求
	anthropicBody := `{
		"model": "claude-opus-4-8",
		"max_tokens": 1024,
		"system": "You are helpful.",
		"messages": [
			{"role": "user", "content": "Hello"}
		],
		"stream": true
	}`

	inbound := newInbound(llm.APIFormatAnthropicMessage)
	if inbound == nil {
		t.Fatal("newInbound(Anthropic) returned nil")
	}

	// 2. inbound: Anthropic HTTP -> 内部 llm.Request
	llmReq, err := inbound.TransformRequest(context.Background(), &httpclient.Request{
		Method:      "POST",
		Path:        "/v1/messages",
		ContentType: "application/json",
		Headers:     map[string][]string{"Content-Type": {"application/json"}},
		Body:        []byte(anthropicBody),
	})
	if err != nil {
		t.Fatalf("inbound.TransformRequest error: %v", err)
	}
	t.Logf("内部 llm.Request: model=%s, messages=%d", llmReq.Model, len(llmReq.Messages))

	// 3. outbound: 内部 llm.Request -> OpenAI HTTP 格式 (渠道类型 = OpenAI)
	outbound, err := newOutbound(llm.APIFormatOpenAIChatCompletion, llmReq, testBaseURL, testAPIKey)
	if err != nil {
		t.Fatalf("newOutbound(OpenAI) error: %v", err)
	}
	if outbound == nil {
		t.Fatal("newOutbound(OpenAI) returned nil")
	}

	httpReq, err := outbound.TransformRequest(context.Background(), llmReq)
	if err != nil {
		t.Fatalf("outbound.TransformRequest error: %v", err)
	}

	t.Logf("出站 URL: %s", httpReq.URL)
	t.Logf("出站 Body: %s", string(httpReq.Body))

	// 4. 验证出站 body 是 OpenAI 格式
	var body map[string]any
	if err := json.Unmarshal(httpReq.Body, &body); err != nil {
		t.Fatalf("出站 body 不是合法 JSON: %v", err)
	}

	// OpenAI 格式关键校验点：
	// (a) system 应该在 messages 里（OpenAI 接受 system role 在 messages 数组中）
	if msgs, ok := body["messages"].([]any); ok {
		hasSystem := false
		for _, m := range msgs {
			if mm, ok := m.(map[string]any); ok {
				if mm["role"] == "system" {
					hasSystem = true
					t.Logf("✅ system 消息在 messages 数组里: %v", mm["content"])
				}
			}
		}
		if !hasSystem {
			t.Logf("⚠️ messages 里没有 system role（可能被放到顶层 system 字段了）")
		}
		t.Logf("messages 总数: %d", len(msgs))
	}

	// (b) URL 应该指向 OpenAI 的 /chat/completions 端点
	if !contains(httpReq.URL, "/chat/completions") {
		t.Errorf("❌ URL = %s，期望包含 /chat/completions", httpReq.URL)
	} else {
		t.Logf("✅ URL 正确: %s", httpReq.URL)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestOpenAINoMaxTokensToAnthropic 验证最容易出问题的场景：
// 客户端 OpenAI 请求【不带 max_tokens】（OpenAI 选填），渠道是 Anthropic（max_tokens 必填）。
// 如果转换后 max_tokens 缺失或为 0，Anthropic 上游会 400 或返回极短内容 —— 这正是你怀疑的现象。
func TestOpenAINoMaxTokensToAnthropic(t *testing.T) {
	openaiBody := `{
		"model": "claude-opus-4-8",
		"messages": [{"role": "user", "content": "写一篇长文"}],
		"stream": true
	}`

	inbound := newInbound(llm.APIFormatOpenAIChatCompletion)
	llmReq, err := inbound.TransformRequest(context.Background(), &httpclient.Request{
		Method:      "POST",
		Path:        "/v1/chat/completions",
		ContentType: "application/json",
		Headers:     map[string][]string{"Content-Type": {"application/json"}},
		Body:        []byte(openaiBody),
	})
	if err != nil {
		t.Fatalf("inbound error: %v", err)
	}
	t.Logf("内部 llm.Request.MaxTokens 指针: %v", llmReq.MaxTokens)
	if llmReq.MaxTokens != nil {
		t.Logf("内部 llm.Request.MaxTokens 值: %d", *llmReq.MaxTokens)
	}

	outbound, err := newOutbound(llm.APIFormatAnthropicMessage, llmReq, testBaseURL, testAPIKey)
	if err != nil {
		t.Fatalf("newOutbound error: %v", err)
	}
	httpReq, err := outbound.TransformRequest(context.Background(), llmReq)
	if err != nil {
		t.Fatalf("outbound.TransformRequest error: %v", err)
	}
	t.Logf("出站 Body: %s", string(httpReq.Body))

	var body map[string]any
	if err := json.Unmarshal(httpReq.Body, &body); err != nil {
		t.Fatalf("出站 body 不是合法 JSON: %v", err)
	}

	mt, ok := body["max_tokens"]
	if !ok {
		t.Errorf("❌ 无 max_tokens 输入时，出站 body 缺少 max_tokens —— Anthropic 上游会拒绝或返回极短响应")
		return
	}
	if n, isNum := mt.(float64); isNum {
		if n <= 0 {
			t.Errorf("❌ max_tokens = %v（<=0）—— 这会导致上游只返回极短/空内容，正是 output_token=9 现象", n)
		} else {
			t.Logf("✅ 转换器为缺失 max_tokens 补了默认值: %v", n)
		}
	}
}

// TestOpenAIInboundToOpenAIOutbound 对照组：OpenAI -> OpenAI 渠道，应该直接透传
func TestOpenAIInboundToOpenAIOutbound(t *testing.T) {
	openaiBody := `{
		"model": "gpt-4",
		"messages": [{"role": "user", "content": "Hello"}],
		"max_tokens": 100
	}`

	inbound := newInbound(llm.APIFormatOpenAIChatCompletion)
	llmReq, err := inbound.TransformRequest(context.Background(), &httpclient.Request{
		Method:      "POST",
		Path:        "/v1/chat/completions",
		ContentType: "application/json",
		Headers:     map[string][]string{"Content-Type": {"application/json"}},
		Body:        []byte(openaiBody),
	})
	if err != nil {
		t.Fatalf("inbound error: %v", err)
	}

	outbound, err := newOutbound(llm.APIFormatOpenAIChatCompletion, llmReq, testBaseURL, testAPIKey)
	if err != nil {
		t.Fatalf("outbound error: %v", err)
	}
	httpReq, err := outbound.TransformRequest(context.Background(), llmReq)
	if err != nil {
		t.Fatalf("TransformRequest error: %v", err)
	}
	t.Logf("OpenAI->OpenAI 出站 Body: %s", string(httpReq.Body))
}
