package relay

import (
	"testing"

	"github.com/bestruirui/octopus/internal/relay/errorclass"
)

// 生产回归：relay_logs id=1785065704891（2026-07-26 Gemini-Flash 分组）。
// 上游 new-api 第 4 次尝试返回 400 "API key not valid"，修复前被分类为
// client 级导致 runner 立即终止，priority 4~12 的健康候选从未被尝试。
// 修复后必须是 key 级：同渠道轮换 key，耗尽后继续 channel failover。
func TestFailoverRegression_400APIKeyNotValidDoesNotTerminate(t *testing.T) {
	body := []byte(`{"error":{"message":"API key not valid. Please pass a valid API key.","code":400,"type":"upstream_error"}}`)
	c := errorclass.Classify(400, body)
	if c.Level != errorclass.ErrorLevelKey {
		t.Fatalf("400 api-key-not-valid level = %v, want key (reason=%q)", c.Level, c.Reason)
	}
	decision := decideRelayError(400, nil, body, nil)
	if decision.Action == ErrorActionReturnClient {
		t.Fatalf("decision must not terminate failover, got %v", decision.Action)
	}
}

// 同一事件的第 2/3 次尝试：503 model permission 保持 key 级（回归保护）。
func TestFailoverRegression_503ModelPermissionStaysKeyLevel(t *testing.T) {
	body := []byte(`{"error":{"message":"No available channel for model gemini-3.5-flash under group Other (distributor)","code":"model_not_found","type":"new_api_error"}}`)
	c := errorclass.Classify(503, body)
	if c.Level != errorclass.ErrorLevelKey {
		t.Fatalf("503 model permission level = %v, want key (reason=%q)", c.Level, c.Reason)
	}
}

// 无特征词的歧义 400：Level 保持 client（不扣渠道健康分、不熔断），
// 但 Action 必须是 retry_channel，让 iterator 继续走完剩余候选。
func TestFailoverRegression_Ambiguous400ContinuesFailover(t *testing.T) {
	body := []byte(`{"error":"Bad Request"}`)
	decision := decideRelayError(400, nil, body, nil)
	if decision.Classification.Level != errorclass.ErrorLevelClient {
		t.Fatalf("ambiguous 400 level = %v, want client", decision.Classification.Level)
	}
	if decision.Action != ErrorActionRetryChannel {
		t.Fatalf("ambiguous 400 action = %v, want retry_channel", decision.Action)
	}
}
