package health

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/relay/errorclass"
)

// TestHealthManager_BasicFlow 测试基本流程
func TestHealthManager_BasicFlow(t *testing.T) {
	config := DefaultHealthConfig()
	manager := NewHealthManager(config)

	// 初始状态
	if !manager.IsEnabled() {
		t.Error("Manager should be enabled by default")
	}

	// 记录成功事件
	manager.RecordSuccess(1, 100, "gpt-4", 3*time.Second)

	// 获取健康状态
	health, ok := manager.Get(HealthKey{ChannelID: 1, KeyID: 100, Model: "gpt-4"})
	if !ok {
		t.Fatal("Health state should exist")
	}

	stats := health.GetStats()
	if stats.SuccessCount != 1 {
		t.Errorf("Expected 1 success, got %d", stats.SuccessCount)
	}

	// 获取评分
	score := manager.GetScore(1, 100, "gpt-4")
	if score != 1.0 {
		t.Errorf("Expected score 1.0, got %.2f", score)
	}
}

// TestHealthManager_RecordError 测试错误记录
func TestHealthManager_RecordError(t *testing.T) {
	config := DefaultHealthConfig()
	manager := NewHealthManager(config)

	// 记录成功
	for i := 0; i < 5; i++ {
		manager.RecordSuccess(1, 100, "gpt-4", 3*time.Second)
	}

	// 记录渠道级错误
	classification := &errorclass.Classification{
		Level:  errorclass.ErrorLevelChannel,
		Reason: "503 service unavailable",
	}
	manager.RecordError(1, 100, "gpt-4", classification, 503, nil, 0)

	// 检查统计
	health, _ := manager.Get(HealthKey{ChannelID: 1, KeyID: 100, Model: "gpt-4"})
	stats := health.GetStats()

	if stats.TotalCount != 6 {
		t.Errorf("Expected 6 total events, got %d", stats.TotalCount)
	}

	if stats.SuccessRate == 1.0 {
		t.Error("Success rate should decrease after error")
	}
}

func TestHealthManager_RecordErrorNilClassification(t *testing.T) {
	manager := NewHealthManager(DefaultHealthConfig())

	manager.RecordError(1, 100, "gpt-4", nil, 0, nil, 0)

	health, ok := manager.Get(HealthKey{ChannelID: 1, KeyID: 100, Model: "gpt-4"})
	if !ok {
		t.Fatal("expected health state")
	}
	stats := health.GetStats()
	if stats.TotalCount != 1 || stats.SuccessRate != 0 {
		t.Fatalf("stats = %+v, want one channel failure", stats)
	}
}

// TestHealthManager_ClientCancelIgnored 测试客户端取消不惩罚渠道
func TestHealthManager_ClientCancelIgnored(t *testing.T) {
	config := DefaultHealthConfig()
	manager := NewHealthManager(config)

	// 记录成功
	for i := 0; i < 5; i++ {
		manager.RecordSuccess(1, 100, "gpt-4", 3*time.Second)
	}

	initialScore := manager.GetScore(1, 100, "gpt-4")

	// 记录客户端取消
	manager.RecordError(1, 100, "gpt-4", nil, 0, context.Canceled, 0)

	finalScore := manager.GetScore(1, 100, "gpt-4")

	if finalScore != initialScore {
		t.Errorf("Client cancel should not affect score: initial=%.2f, final=%.2f",
			initialScore, finalScore)
	}
}

// TestHealthManager_KeyErrorIgnored 测试 Key 级错误不降低渠道健康度
func TestHealthManager_KeyErrorIgnored(t *testing.T) {
	config := DefaultHealthConfig()
	manager := NewHealthManager(config)

	// 记录成功
	for i := 0; i < 5; i++ {
		manager.RecordSuccess(1, 100, "gpt-4", 3*time.Second)
	}

	initialScore := manager.GetScore(1, 100, "gpt-4")

	// 记录 Key 级错误
	classification := &errorclass.Classification{
		Level:  errorclass.ErrorLevelKey,
		Reason: "429 rate limit",
	}
	manager.RecordError(1, 100, "gpt-4", classification, 429, nil, 0)

	finalScore := manager.GetScore(1, 100, "gpt-4")

	// 健康度不应该下降
	if finalScore != initialScore {
		t.Errorf("Key error should not affect score: initial=%.2f, final=%.2f",
			initialScore, finalScore)
	}

	// 但 KeyErrorCount 应该增加
	health, _ := manager.Get(HealthKey{ChannelID: 1, KeyID: 100, Model: "gpt-4"})
	stats := health.GetStats()
	if stats.KeyErrorCount != 1 {
		t.Errorf("Expected 1 key error, got %d", stats.KeyErrorCount)
	}
}

// TestHealthManager_Timeout 测试超时记录
func TestHealthManager_Timeout(t *testing.T) {
	config := DefaultHealthConfig()
	config.MinSamplesForAdaptiveTimeout = 5
	manager := NewHealthManager(config)

	// 记录成功
	for i := 0; i < 10; i++ {
		manager.RecordSuccess(1, 100, "gpt-4", 3*time.Second)
	}

	// 记录超时
	manager.RecordTimeout(1, 100, "gpt-4", 10*time.Second)

	// 检查统计
	health, _ := manager.Get(HealthKey{ChannelID: 1, KeyID: 100, Model: "gpt-4"})
	stats := health.GetStats()

	if stats.TimeoutCount != 1 {
		t.Errorf("Expected 1 timeout, got %d", stats.TimeoutCount)
	}

	// P95 应该被拉高
	if stats.FirstTokenP95 <= 3*time.Second {
		t.Errorf("P95 should be pulled up by timeout, got %v", stats.FirstTokenP95)
	}
}

// TestHealthManager_GetTimeout 测试自适应超时获取
func TestHealthManager_GetTimeout(t *testing.T) {
	config := DefaultHealthConfig()
	config.MinSamplesForAdaptiveTimeout = 10
	manager := NewHealthManager(config)

	// 新渠道，使用冷启动超时
	timeout := manager.GetTimeout(1, 100, "gpt-4")
	if timeout != config.ColdStartTimeout {
		t.Errorf("Expected cold start timeout %v, got %v", config.ColdStartTimeout, timeout)
	}

	// 记录足够样本
	for i := 0; i < 20; i++ {
		manager.RecordSuccess(1, 100, "gpt-4", 3*time.Second)
	}

	// 使用自适应超时
	timeout = manager.GetTimeout(1, 100, "gpt-4")
	if timeout == config.ColdStartTimeout {
		t.Error("Should use adaptive timeout after sufficient samples")
	}

	t.Logf("Adaptive timeout: %v", timeout)
}

func TestHealthManager_HasAdaptiveTimeout(t *testing.T) {
	config := DefaultHealthConfig()
	config.MinSamplesForAdaptiveTimeout = 3
	manager := NewHealthManager(config)

	if manager.HasAdaptiveTimeout(1, 100, "gpt-4") {
		t.Fatal("new channel should not have adaptive timeout")
	}

	manager.RecordSuccess(1, 100, "gpt-4", time.Second)
	manager.RecordSuccess(1, 100, "gpt-4", time.Second)
	if manager.HasAdaptiveTimeout(1, 100, "gpt-4") {
		t.Fatal("insufficient samples should not have adaptive timeout")
	}

	manager.RecordSuccess(1, 100, "gpt-4", time.Second)
	if !manager.HasAdaptiveTimeout(1, 100, "gpt-4") {
		t.Fatal("sufficient samples should have adaptive timeout")
	}
}

// TestHealthManager_Disable 测试禁用功能
func TestHealthManager_Disable(t *testing.T) {
	config := DefaultHealthConfig()
	manager := NewHealthManager(config)

	// 记录成功
	manager.RecordSuccess(1, 100, "gpt-4", 3*time.Second)

	// 禁用
	manager.Disable()
	if manager.IsEnabled() {
		t.Error("Manager should be disabled")
	}

	// 禁用后记录不生效
	manager.RecordSuccess(1, 100, "gpt-4", 3*time.Second)

	health, _ := manager.Get(HealthKey{ChannelID: 1, KeyID: 100, Model: "gpt-4"})
	stats := health.GetStats()

	// 仍然只有 1 次成功
	if stats.SuccessCount != 1 {
		t.Errorf("Expected 1 success (disabled), got %d", stats.SuccessCount)
	}

	// 启用
	manager.Enable()
	if !manager.IsEnabled() {
		t.Error("Manager should be enabled")
	}

	// 启用后记录生效
	manager.RecordSuccess(1, 100, "gpt-4", 3*time.Second)
	stats = health.GetStats()

	if stats.SuccessCount != 2 {
		t.Errorf("Expected 2 successes (enabled), got %d", stats.SuccessCount)
	}
}

// TestHealthManager_GetAllStates 测试获取所有状态
func TestHealthManager_GetAllStates(t *testing.T) {
	config := DefaultHealthConfig()
	manager := NewHealthManager(config)

	// 创建多个渠道
	manager.RecordSuccess(1, 100, "gpt-4", 3*time.Second)
	manager.RecordSuccess(2, 200, "gpt-3.5", 2*time.Second)
	manager.RecordSuccess(3, 300, "claude-3", 4*time.Second)

	// 获取所有状态
	allStates := manager.GetAllStates()

	if len(allStates) != 3 {
		t.Errorf("Expected 3 states, got %d", len(allStates))
	}

	// 验证每个状态
	for key, stats := range allStates {
		if stats.SuccessCount != 1 {
			t.Errorf("Key %+v: expected 1 success, got %d", key, stats.SuccessCount)
		}
	}
}

func TestHealthManagerInvalidateChannelCanTargetOneModel(t *testing.T) {
	manager := NewHealthManager(DefaultHealthConfig())
	manager.RecordSuccess(7, 70, "model-a", time.Second)
	manager.RecordSuccess(7, 71, "model-b", time.Second)
	manager.RecordSuccess(8, 80, "model-a", time.Second)

	manager.InvalidateChannel(7, "model-a")
	states := manager.GetAllStates()
	if _, ok := states[HealthKey{ChannelID: 7, KeyID: 70, Model: "model-a"}]; ok {
		t.Fatal("target channel/model health state remained after invalidation")
	}
	if _, ok := states[HealthKey{ChannelID: 7, KeyID: 71, Model: "model-b"}]; !ok {
		t.Fatal("target channel's unrelated model was removed")
	}
	if _, ok := states[HealthKey{ChannelID: 8, KeyID: 80, Model: "model-a"}]; !ok {
		t.Fatal("unrelated channel health state was removed")
	}

	manager.InvalidateChannel(7, "")
	for key := range manager.GetAllStates() {
		if key.ChannelID == 7 {
			t.Fatalf("channel-wide invalidation retained state: %+v", key)
		}
	}
}

// TestHealthManager_Cleanup 测试清理过期状态
func TestHealthManager_Cleanup(t *testing.T) {
	config := DefaultHealthConfig()
	manager := NewHealthManager(config)

	// 创建旧状态
	manager.RecordSuccess(1, 100, "gpt-4", 3*time.Second)

	// 修改最后事件时间（模拟过期）
	health, _ := manager.Get(HealthKey{ChannelID: 1, KeyID: 100, Model: "gpt-4"})
	health.mu.Lock()
	health.Stats.LastEventAt = time.Now().Add(-2 * time.Hour)
	health.mu.Unlock()

	// 创建新状态
	manager.RecordSuccess(2, 200, "gpt-3.5", 2*time.Second)

	// 清理 1 小时前的状态
	ctx := context.Background()
	manager.Cleanup(ctx, 1*time.Hour)

	// 旧状态应该被清理
	_, ok := manager.Get(HealthKey{ChannelID: 1, KeyID: 100, Model: "gpt-4"})
	if ok {
		t.Error("Old state should be cleaned up")
	}

	// 新状态应该保留
	_, ok = manager.Get(HealthKey{ChannelID: 2, KeyID: 200, Model: "gpt-3.5"})
	if !ok {
		t.Error("New state should be kept")
	}
}

// TestHealthManager_Concurrency 测试并发安全
func TestHealthManager_Concurrency(t *testing.T) {
	config := DefaultHealthConfig()
	manager := NewHealthManager(config)

	var wg sync.WaitGroup
	numGoroutines := 100
	numOpsPerGoroutine := 100

	// 并发写入
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOpsPerGoroutine; j++ {
				manager.RecordSuccess(id%10, id%5, "gpt-4", 3*time.Second)
			}
		}(i)
	}

	// 并发读取
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOpsPerGoroutine; j++ {
				manager.GetScore(id%10, id%5, "gpt-4")
				manager.GetTimeout(id%10, id%5, "gpt-4")
			}
		}(i)
	}

	wg.Wait()

	// 验证数据一致性
	allStates := manager.GetAllStates()
	t.Logf("Total states created: %d", len(allStates))

	totalEvents := int64(0)
	for _, stats := range allStates {
		totalEvents += stats.TotalCount
	}

	expected := int64(numGoroutines * numOpsPerGoroutine)
	if totalEvents != expected {
		t.Errorf("Expected %d total events, got %d", expected, totalEvents)
	}
}

func TestHealthManagerUpdateConfigPreservesEvidence(t *testing.T) {
	manager := NewHealthManager(DefaultHealthConfig())
	key := HealthKey{ChannelID: 7, KeyID: 11, Model: "reasoning-model"}
	manager.RecordSuccess(key.ChannelID, key.KeyID, key.Model, 3*time.Second)
	before, ok := manager.Get(key)
	if !ok {
		t.Fatal("health state was not created")
	}

	updated := DefaultHealthConfig()
	updated.ShadowMode = true
	updated.MinAdaptiveTimeout = 23 * time.Second
	updated.WindowSize = 10
	manager.UpdateConfig(updated)

	after, ok := manager.Get(key)
	if !ok || after != before {
		t.Fatal("UpdateConfig replaced existing health evidence")
	}
	stats := after.GetStats()
	if stats.SuccessCount != 1 || stats.TotalCount != 1 {
		t.Fatalf("evidence changed after hot reload: %+v", stats)
	}
	if after.Config.MinAdaptiveTimeout != 23*time.Second || !manager.IsShadowMode() {
		t.Fatalf("updated policy was not applied: timeout=%v shadow=%t", after.Config.MinAdaptiveTimeout, manager.IsShadowMode())
	}
}

// BenchmarkHealthManager_RecordSuccess 性能测试
func BenchmarkHealthManager_RecordSuccess(b *testing.B) {
	config := DefaultHealthConfig()
	manager := NewHealthManager(config)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		manager.RecordSuccess(1, 100, "gpt-4", 3*time.Second)
	}
}

// BenchmarkHealthManager_GetScore 性能测试
func BenchmarkHealthManager_GetScore(b *testing.B) {
	config := DefaultHealthConfig()
	manager := NewHealthManager(config)

	// 预热
	for i := 0; i < 100; i++ {
		manager.RecordSuccess(1, 100, "gpt-4", 3*time.Second)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		manager.GetScore(1, 100, "gpt-4")
	}
}

// BenchmarkHealthManager_ConcurrentWrites 并发写入性能测试
func BenchmarkHealthManager_ConcurrentWrites(b *testing.B) {
	config := DefaultHealthConfig()
	manager := NewHealthManager(config)

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			manager.RecordSuccess(i%10, i%5, "gpt-4", 3*time.Second)
			i++
		}
	})
}
