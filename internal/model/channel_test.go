package model

import (
	"net/http"
	"testing"
	"time"
)

func TestChannelKeyIsAvailable(t *testing.T) {
	nowSec := time.Now().Unix()
	recent429 := nowSec - int64(time.Minute/time.Second)
	expired429 := nowSec - int64(6*time.Minute/time.Second)

	tests := []struct {
		name string
		key  ChannelKey
		want bool
	}{
		{
			name: "可用 key",
			key:  ChannelKey{Enabled: true, ChannelKey: "sk-ok"},
			want: true,
		},
		{
			name: "禁用 key 不可用",
			key:  ChannelKey{Enabled: false, ChannelKey: "sk-disabled"},
			want: false,
		},
		{
			name: "空 key 不可用",
			key:  ChannelKey{Enabled: true, ChannelKey: ""},
			want: false,
		},
		{
			name: "401 不永久禁用 key（靠熔断器处理，避免误排除可用 key）",
			key:  ChannelKey{Enabled: true, ChannelKey: "sk-once-401", StatusCode: http.StatusUnauthorized},
			want: true,
		},
		{
			name: "400 请求错误不影响 key 可用性",
			key:  ChannelKey{Enabled: true, ChannelKey: "sk-bad-request", StatusCode: http.StatusBadRequest},
			want: true,
		},
		{
			name: "503 上游临时故障不影响 key 可用性",
			key:  ChannelKey{Enabled: true, ChannelKey: "sk-upstream-503", StatusCode: http.StatusServiceUnavailable},
			want: true,
		},
		{
			name: "429 冷却中不可用",
			key:  ChannelKey{Enabled: true, ChannelKey: "sk-rate-limited", StatusCode: http.StatusTooManyRequests, LastUseTimeStamp: recent429},
			want: false,
		},
		{
			name: "429 冷却结束可用",
			key:  ChannelKey{Enabled: true, ChannelKey: "sk-rate-limited", StatusCode: http.StatusTooManyRequests, LastUseTimeStamp: expired429},
			want: true,
		},
		{
			name: "429 Retry-After 精确冷却中",
			key:  ChannelKey{Enabled: true, ChannelKey: "sk-rate-limited", StatusCode: http.StatusTooManyRequests, RetryAfterUntil: nowSec + 5},
			want: false,
		},
		{
			name: "429 Retry-After 精确冷却结束",
			key:  ChannelKey{Enabled: true, ChannelKey: "sk-rate-limited", StatusCode: http.StatusTooManyRequests, RetryAfterUntil: nowSec - 1},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.key.IsAvailable(nowSec)
			if got != tt.want {
				t.Errorf("IsAvailable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestChannelGetChannelKeyByID(t *testing.T) {
	nowSec := time.Now().Unix()
	ch := &Channel{Keys: []ChannelKey{
		{ID: 1, Enabled: true, ChannelKey: "sk-low-cost", TotalCost: 1},
		{ID: 2, Enabled: true, ChannelKey: "sk-sticky", TotalCost: 100},
		{ID: 3, Enabled: false, ChannelKey: "sk-disabled"},
		{ID: 4, Enabled: true, ChannelKey: "sk-rate-limited", StatusCode: http.StatusTooManyRequests, LastUseTimeStamp: nowSec},
	}}

	tests := []struct {
		name  string
		keyID int
		want  int
	}{
		{name: "按 ID 返回可用 sticky key", keyID: 2, want: 2},
		{name: "找不到返回空 key", keyID: 99, want: 0},
		{name: "禁用 key 返回空 key", keyID: 3, want: 0},
		{name: "429 冷却 key 返回空 key", keyID: 4, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ch.GetChannelKeyByID(tt.keyID)
			if got.ID != tt.want {
				t.Errorf("GetChannelKeyByID(%d).ID = %d, want %d", tt.keyID, got.ID, tt.want)
			}
		})
	}
}

func TestChannelGetChannelKeyKeepsLowestCostFallback(t *testing.T) {
	nowSec := time.Now().Unix()
	ch := &Channel{Keys: []ChannelKey{
		{ID: 1, Enabled: true, ChannelKey: "sk-high-cost", TotalCost: 10},
		{ID: 2, Enabled: true, ChannelKey: "sk-low-cost", TotalCost: 1},
		{ID: 3, Enabled: true, ChannelKey: "sk-rate-limited", TotalCost: 0, StatusCode: http.StatusTooManyRequests, LastUseTimeStamp: nowSec},
	}}

	got := ch.GetChannelKey()
	if got.ID != 2 {
		t.Errorf("GetChannelKey().ID = %d, want 2", got.ID)
	}
}

func TestChannelGetChannelKeyStillUsesKeyAfter401(t *testing.T) {
	// 401 不应让 key 被永久排除：上游可能偶发返回 401，靠熔断器处理连续失败，
	// 这里确认被标记 401 的 key 仍可被选中（用户直连可用的 key 不能因一次 401 被废）。
	ch := &Channel{Keys: []ChannelKey{
		{ID: 1, Enabled: true, ChannelKey: "sk-once-401", TotalCost: 0, StatusCode: http.StatusUnauthorized},
	}}

	got := ch.GetChannelKey()
	if got.ID != 1 {
		t.Errorf("GetChannelKey().ID = %d, want 1（401 不应禁用 key）", got.ID)
	}
}

func TestChannelAvailableKeysForAttemptPrioritizesStickyThenCost(t *testing.T) {
	ch := &Channel{Keys: []ChannelKey{
		{ID: 1, Enabled: true, ChannelKey: "sk-expensive", TotalCost: 100},
		{ID: 2, Enabled: true, ChannelKey: "sk-cheap", TotalCost: 1},
		{ID: 3, Enabled: true, ChannelKey: "sk-sticky", TotalCost: 50},
	}}

	keys := ch.AvailableKeysForAttempt(3)
	want := []int{3, 2, 1}
	if len(keys) != len(want) {
		t.Fatalf("key count = %d, want %d", len(keys), len(want))
	}
	for i, id := range want {
		if keys[i].ID != id {
			t.Fatalf("keys[%d].ID = %d, want %d (all=%+v)", i, keys[i].ID, id, keys)
		}
	}
}

func TestChannelAvailableKeysForAttemptSkipsUnavailableSticky(t *testing.T) {
	nowSec := time.Now().Unix()
	ch := &Channel{Keys: []ChannelKey{
		{ID: 1, Enabled: true, ChannelKey: "sk-rate-limited", TotalCost: 0, StatusCode: http.StatusTooManyRequests, LastUseTimeStamp: nowSec},
		{ID: 2, Enabled: true, ChannelKey: "sk-ok", TotalCost: 1},
	}}

	keys := ch.AvailableKeysForAttempt(1)
	if len(keys) != 1 || keys[0].ID != 2 {
		t.Fatalf("keys = %+v, want only key 2", keys)
	}
}
