package handlers

import (
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/client"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/balancer"
)

func TestInvalidateChannelRuntimeStateClearsStickyAndProxyClient(t *testing.T) {
	client.InvalidateAllCustomProxyClients()
	defer client.InvalidateAllCustomProxyClients()

	const (
		channelID = 707
		apiKeyID  = 909
		modelName = "runtime-state-test"
	)
	proxyURL := "http://127.0.0.1:19090"
	firstClient, err := client.GetHTTPClientCustomProxy(proxyURL)
	if err != nil {
		t.Fatalf("GetHTTPClientCustomProxy() error = %v", err)
	}
	balancer.SetSticky(apiKeyID, modelName, channelID, 1, modelName)

	invalidateChannelRuntimeState(channelID, &model.Channel{
		ID:           channelID,
		ChannelProxy: &proxyURL,
	})

	if sticky := balancer.GetSticky(apiKeyID, modelName, time.Minute); sticky != nil {
		t.Fatalf("sticky state remained after channel invalidation: %#v", sticky)
	}
	secondClient, err := client.GetHTTPClientCustomProxy(proxyURL)
	if err != nil {
		t.Fatalf("GetHTTPClientCustomProxy() after invalidation error = %v", err)
	}
	if firstClient == secondClient {
		t.Fatal("channel invalidation reused the stale custom proxy client")
	}
}

func TestInvalidateChannelRuntimeStateWithoutSnapshotClearsProxyCache(t *testing.T) {
	client.InvalidateAllCustomProxyClients()
	defer client.InvalidateAllCustomProxyClients()

	proxyURL := "http://127.0.0.1:19091"
	firstClient, err := client.GetHTTPClientCustomProxy(proxyURL)
	if err != nil {
		t.Fatalf("GetHTTPClientCustomProxy() error = %v", err)
	}

	invalidateChannelRuntimeState(708, nil)

	secondClient, err := client.GetHTTPClientCustomProxy(proxyURL)
	if err != nil {
		t.Fatalf("GetHTTPClientCustomProxy() after fallback invalidation error = %v", err)
	}
	if firstClient == secondClient {
		t.Fatal("missing channel snapshot did not clear the bounded proxy cache")
	}
}

// TestResetGroupMemberCircuitsClearsFrozenChannels 锁定场景 B：分组被手动
// 启用/更新后，其直连成员渠道对应模型的熔断必须立即清除，用户无需等待冷却。
func TestResetGroupMemberCircuitsClearsFrozenChannels(t *testing.T) {
	const (
		frozenChannel = 811
		otherChannel  = 812
		modelName     = "group-reset-model"
	)
	// 制造两个渠道的熔断（默认阈值 2 次连续失败）。
	for i := 0; i < 2; i++ {
		balancer.RecordFailure(frozenChannel, 1, modelName)
		balancer.RecordFailure(otherChannel, 1, modelName)
	}
	if tripped, _ := balancer.IsTripped(frozenChannel, 1, modelName); !tripped {
		t.Fatal("前置条件失败：frozenChannel 应处于熔断态")
	}

	// 分组仅包含 frozenChannel；启用该分组应只清除它。
	group := &model.Group{
		Enabled: true,
		Items: []model.GroupItem{
			{Type: model.GroupItemTypeChannel, ChannelID: frozenChannel, ModelName: modelName},
		},
	}
	resetGroupMemberCircuits(group)

	if tripped, _ := balancer.IsTripped(frozenChannel, 1, modelName); tripped {
		t.Fatal("启用分组后成员渠道模型应立即可用（熔断已清除）")
	}
	if tripped, _ := balancer.IsTripped(otherChannel, 1, modelName); !tripped {
		t.Fatal("非成员渠道的熔断不应被误清除")
	}
	// 清理，避免影响其他用例。
	balancer.ResetCircuit(otherChannel, "")
}

// TestResetGroupMemberCircuitsSkipsDisabledGroupAndNestedItems 验证：禁用态
// 分组不清熔断（未启用即不表达"立即可用"），嵌套子分组项被跳过（在其自身
// 被操作时才清）。
func TestResetGroupMemberCircuitsSkipsDisabledGroupAndNestedItems(t *testing.T) {
	const (
		channelID = 813
		modelName = "group-reset-disabled"
	)
	for i := 0; i < 2; i++ {
		balancer.RecordFailure(channelID, 1, modelName)
	}
	defer balancer.ResetCircuit(channelID, "")

	// 禁用态分组：不应清熔断。
	resetGroupMemberCircuits(&model.Group{
		Enabled: false,
		Items: []model.GroupItem{
			{Type: model.GroupItemTypeChannel, ChannelID: channelID, ModelName: modelName},
		},
	})
	if tripped, _ := balancer.IsTripped(channelID, 1, modelName); !tripped {
		t.Fatal("禁用态分组不应清除成员熔断")
	}

	// nil 分组：安全空操作。
	resetGroupMemberCircuits(nil)

	// 仅含嵌套子分组项的启用分组：无直连渠道，不触碰任何熔断。
	resetGroupMemberCircuits(&model.Group{
		Enabled: true,
		Items: []model.GroupItem{
			{Type: model.GroupItemTypeGroup, TargetGroupID: 999},
		},
	})
	if tripped, _ := balancer.IsTripped(channelID, 1, modelName); !tripped {
		t.Fatal("嵌套子分组项不应影响其他渠道的熔断")
	}
}
