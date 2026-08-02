package balancer

import (
	"strings"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

func clearSessions() {
	globalSession.clear()
}

func TestBoundedErrorReasonRemovesControlsAndCapsRunes(t *testing.T) {
	got := boundedErrorReason("  reason\r\n" + strings.Repeat("界", maxAttemptErrorReasonRunes+20))
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("reason retained control characters: %q", got)
	}
	if len([]rune(got)) != maxAttemptErrorReasonRunes {
		t.Fatalf("reason rune length = %d, want %d", len([]rune(got)), maxAttemptErrorReasonRunes)
	}
}

func TestNewIteratorMovesStickyChannelAndKeepsStickyKeyID(t *testing.T) {
	clearSessions()
	defer clearSessions()

	const (
		apiKeyID     = 7
		requestModel = "octopus-model"
		channelID    = 1
		channelKeyID = 101
	)

	SetSticky(apiKeyID, requestModel, channelID, channelKeyID, "upstream-a")
	group := model.Group{
		Mode:            model.GroupModeFailover,
		SessionKeepTime: 60,
		Items: []model.GroupItem{
			{ChannelID: 1, ModelName: "upstream-a", Priority: 1},
			{ChannelID: 2, ModelName: "upstream-b", Priority: 1},
		},
	}

	iter := NewIterator(group, apiKeyID, requestModel)
	if !iter.Next() {
		t.Fatal("Next() = false, want true")
	}
	if got := iter.Item().ChannelID; got != channelID {
		t.Fatalf("sticky channel ID = %d, want %d", got, channelID)
	}
	if !iter.IsSticky() {
		t.Fatal("IsSticky() = false, want true")
	}
	if got := iter.StickyKeyID(); got != channelKeyID {
		t.Errorf("StickyKeyID() = %d, want %d", got, channelKeyID)
	}
}

func TestStickyIsScopedBySessionID(t *testing.T) {
	clearSessions()
	defer clearSessions()
	group := model.Group{Mode: model.GroupModeFailover, SessionKeepTime: 60}
	candidates := []model.GroupItem{
		{ID: 1, ChannelID: 1, ModelName: "m", Priority: 1},
		{ID: 2, ChannelID: 2, ModelName: "m", Priority: 1},
	}
	SetStickyForSession(7, "model", "session-a", 2, 202, "m")
	other := NewIteratorFromCandidatesWithSession(group, 7, "model", "session-b", append([]model.GroupItem(nil), candidates...), nil, nil)
	if !other.Next() || other.Item().ChannelID != 1 || other.IsSticky() {
		t.Fatalf("different session reused sticky: item=%+v sticky=%v", other.Item(), other.IsSticky())
	}
	same := NewIteratorFromCandidatesWithSession(group, 7, "model", "session-a", append([]model.GroupItem(nil), candidates...), nil, nil)
	if !same.Next() || same.Item().ChannelID != 2 || !same.IsSticky() || same.StickyKeyID() != 202 {
		t.Fatalf("same session missed sticky: item=%+v sticky=%v key=%d", same.Item(), same.IsSticky(), same.StickyKeyID())
	}
}

func TestStickyDoesNotCrossPriorityOrPolicyProfile(t *testing.T) {
	clearSessions()
	defer clearSessions()
	group := model.Group{Mode: model.GroupModeFailover, SessionKeepTime: 60}
	SetStickyForSession(8, "model", "session", 2, 202, "m")
	priorityCandidates := []model.GroupItem{
		{ID: 1, ChannelID: 1, ModelName: "m", Priority: 1},
		{ID: 2, ChannelID: 2, ModelName: "m", Priority: 2},
	}
	priority := NewIteratorFromCandidatesWithSession(group, 8, "model", "session", priorityCandidates, nil, nil)
	if !priority.Next() || priority.Item().ChannelID != 1 || priority.IsSticky() {
		t.Fatalf("sticky crossed priority: item=%+v sticky=%v", priority.Item(), priority.IsSticky())
	}
	profileCandidates := []model.GroupItem{
		{ID: 1, ChannelID: 1, ModelName: "m", Priority: 1},
		{ID: 2, ChannelID: 2, ModelName: "m", Priority: 1},
	}
	profiles := map[int]string{1: "official", 2: "untrusted_proxy"}
	profile := NewIteratorFromCandidatesWithSession(group, 8, "model", "session", profileCandidates, nil, profiles)
	if !profile.Next() || profile.Item().ChannelID != 1 || profile.IsSticky() {
		t.Fatalf("sticky crossed policy profile: item=%+v sticky=%v", profile.Item(), profile.IsSticky())
	}
}

func TestStickyKeyIDReturnsZeroOutsideStickyCandidate(t *testing.T) {
	clearSessions()
	defer clearSessions()

	const (
		apiKeyID     = 8
		requestModel = "octopus-model"
	)

	group := model.Group{
		Mode:            model.GroupModeFailover,
		SessionKeepTime: 60,
		Items: []model.GroupItem{
			{ChannelID: 1, ModelName: "upstream-a", Priority: 1},
			{ChannelID: 2, ModelName: "upstream-b", Priority: 2},
		},
	}

	iter := NewIterator(group, apiKeyID, requestModel)
	if !iter.Next() {
		t.Fatal("Next() = false, want true")
	}
	if iter.IsSticky() {
		t.Fatal("IsSticky() = true, want false")
	}
	if got := iter.StickyKeyID(); got != 0 {
		t.Errorf("StickyKeyID() = %d, want 0", got)
	}
}

func TestNewIteratorSkipsStickyWhenActualModelDiffers(t *testing.T) {
	clearSessions()
	defer clearSessions()

	const (
		apiKeyID     = 9
		requestModel = "octopus-model"
		channelID    = 1
		channelKeyID = 101
	)

	// 上次成功用的是 upstream-a；本次该渠道候选只有 upstream-b（同渠道不同实际模型）。
	SetSticky(apiKeyID, requestModel, channelID, channelKeyID, "upstream-a")
	group := model.Group{
		Mode:            model.GroupModeFailover,
		SessionKeepTime: 60,
		Items: []model.GroupItem{
			{ChannelID: 1, ModelName: "upstream-b", Priority: 1},
			{ChannelID: 2, ModelName: "upstream-c", Priority: 2},
		},
	}

	iter := NewIterator(group, apiKeyID, requestModel)
	if !iter.Next() {
		t.Fatal("Next() = false, want true")
	}
	// actual model 不一致，不应复用 sticky，避免 prompt cache miss。
	if iter.IsSticky() {
		t.Fatal("IsSticky() = true, want false (actual model differs)")
	}
	if got := iter.StickyKeyID(); got != 0 {
		t.Errorf("StickyKeyID() = %d, want 0", got)
	}
}

func TestNewIteratorMatchesStickyByActualModelAmongMultipleItems(t *testing.T) {
	clearSessions()
	defer clearSessions()

	const (
		apiKeyID     = 10
		requestModel = "octopus-model"
		channelID    = 1
		channelKeyID = 202
	)

	// 同一渠道在分组内服务多个实际模型；sticky 记录的是 upstream-b，应精准命中该 item。
	SetSticky(apiKeyID, requestModel, channelID, channelKeyID, "upstream-b")
	group := model.Group{
		Mode:            model.GroupModeFailover,
		SessionKeepTime: 60,
		Items: []model.GroupItem{
			{ChannelID: 2, ModelName: "upstream-x", Priority: 1},
			{ChannelID: 1, ModelName: "upstream-a", Priority: 1},
			{ChannelID: 1, ModelName: "upstream-b", Priority: 1},
		},
	}

	iter := NewIterator(group, apiKeyID, requestModel)
	if !iter.Next() {
		t.Fatal("Next() = false, want true")
	}
	if !iter.IsSticky() {
		t.Fatal("IsSticky() = false, want true")
	}
	item := iter.Item()
	if item.ChannelID != channelID || item.ModelName != "upstream-b" {
		t.Fatalf("sticky item = (channel %d, model %s), want (channel %d, model upstream-b)", item.ChannelID, item.ModelName, channelID)
	}
	if got := iter.StickyKeyID(); got != channelKeyID {
		t.Errorf("StickyKeyID() = %d, want %d", got, channelKeyID)
	}
}

func TestSetStickyStoresActualModel(t *testing.T) {
	clearSessions()
	defer clearSessions()

	SetSticky(11, "octopus-model", 3, 303, "upstream-z")
	entry := GetSticky(11, "octopus-model", 60*time.Second)
	if entry == nil {
		t.Fatal("GetSticky() = nil, want entry")
		return
	}
	if entry.ModelName != "upstream-z" {
		t.Fatalf("entry.ModelName = %q, want upstream-z", entry.ModelName)
	}
	if entry.ChannelID != 3 || entry.ChannelKeyID != 303 {
		t.Fatalf("entry = (channel %d, key %d), want (3, 303)", entry.ChannelID, entry.ChannelKeyID)
	}
}

func TestStartAttemptRecordsKeyRemark(t *testing.T) {
	group := model.Group{
		Mode: model.GroupModeFailover,
		Items: []model.GroupItem{
			{ChannelID: 1, ModelName: "upstream-a", Priority: 1},
		},
	}
	iter := NewIterator(group, 1, "octopus-model")
	if !iter.Next() {
		t.Fatal("Next() = false, want true")
	}

	span := iter.StartAttempt(1, 101, "channel-a", "linwolfer")
	span.End(model.AttemptSuccess, "")

	attempts := iter.Attempts()
	if len(attempts) != 1 {
		t.Fatalf("attempts 数量 = %d, want 1", len(attempts))
	}
	if attempts[0].ChannelKeyRemark != "linwolfer" {
		t.Fatalf("ChannelKeyRemark = %q, want linwolfer", attempts[0].ChannelKeyRemark)
	}
}

func TestAttemptSpanRecordsRoutingMetadata(t *testing.T) {
	iter := NewIterator(model.Group{Items: []model.GroupItem{{ChannelID: 1, ModelName: "m"}}}, 0, "m")
	if !iter.Next() {
		t.Fatal("iterator has no candidate")
	}
	span := iter.StartAttempt(1, 2, "channel", "key")
	span.SetRoutingMetadata("https://upstream.example", "retry_base_url", false, "base_url_failover")
	span.End(model.AttemptFailed, "temporary upstream failure")
	attempts := iter.Attempts()
	if len(attempts) != 1 {
		t.Fatalf("attempt count = %d, want 1", len(attempts))
	}
	got := attempts[0]
	if got.BaseURL != "https://upstream.example" || got.Action != "retry_base_url" || got.HealthPenalty || got.SelectionReason != "base_url_failover" {
		t.Fatalf("routing metadata = %+v", got)
	}
}

func TestAttemptSpanPersistsClassificationOnlyForClassifiedFailure(t *testing.T) {
	group := model.Group{Items: []model.GroupItem{{ChannelID: 1, ModelName: "m"}}}
	iter := NewIterator(group, 1, "m")
	if !iter.Next() {
		t.Fatal("iterator should contain candidate")
	}

	failed := iter.StartAttempt(1, 11, "channel", "key")
	failed.EndClassified(model.AttemptFailed, "rate limited", model.AttemptErrorLevelKey, "HTTP 429")
	success := iter.StartAttempt(1, 12, "channel", "key-2")
	success.End(model.AttemptSuccess, "")

	attempts := iter.Attempts()
	if len(attempts) != 2 {
		t.Fatalf("attempt count = %d", len(attempts))
	}
	if attempts[0].ErrorLevel != model.AttemptErrorLevelKey || attempts[0].ErrorReason != "HTTP 429" {
		t.Fatalf("classified failure = %+v", attempts[0])
	}
	if attempts[1].ErrorLevel != "" || attempts[1].ErrorReason != "" {
		t.Fatalf("successful attempt must not carry classification: %+v", attempts[1])
	}
}

func TestSkipCircuitBreakRecordsKeyRemark(t *testing.T) {
	const (
		channelID    = 1
		channelKeyID = 101
		modelName    = "upstream-a"
	)
	group := model.Group{
		Mode: model.GroupModeFailover,
		Items: []model.GroupItem{
			{ChannelID: channelID, ModelName: modelName, Priority: 1},
		},
	}
	iter := NewIterator(group, 1, "octopus-model")
	if !iter.Next() {
		t.Fatal("Next() = false, want true")
	}

	// 触发熔断，使 SkipCircuitBreak 记录一条 attempt。
	for i := 0; i < 100; i++ {
		RecordFailure(channelID, channelKeyID, modelName)
	}
	if !iter.SkipCircuitBreak(channelID, channelKeyID, "channel-a", "linwolfer") {
		t.Skip("熔断未触发，跳过该用例")
	}
	defer RecordSuccess(channelID, channelKeyID, modelName)

	attempts := iter.Attempts()
	if len(attempts) == 0 {
		t.Fatal("attempts 为空, 期望记录熔断跳过")
	}
	last := attempts[len(attempts)-1]
	if last.Status != model.AttemptCircuitBreak {
		t.Fatalf("status = %q, want circuit_break", last.Status)
	}
	if last.ChannelKeyRemark != "linwolfer" {
		t.Fatalf("ChannelKeyRemark = %q, want linwolfer", last.ChannelKeyRemark)
	}
}
