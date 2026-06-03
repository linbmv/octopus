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
			name: "429 冷却中不可用",
			key:  ChannelKey{Enabled: true, ChannelKey: "sk-rate-limited", StatusCode: http.StatusTooManyRequests, LastUseTimeStamp: recent429},
			want: false,
		},
		{
			name: "429 冷却结束可用",
			key:  ChannelKey{Enabled: true, ChannelKey: "sk-rate-limited", StatusCode: http.StatusTooManyRequests, LastUseTimeStamp: expired429},
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
