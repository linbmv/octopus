package op

import (
	"context"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

// 端到端: 429 写回后, 用与 relay selectChannelEndpoint 相同的可用性判定重新挑选,
// 被限流的凭据必须真正退出轮换。
func TestE2ERateLimitedKeyLeavesRotation(t *testing.T) {
	channel := newHealthTestChannel(t)
	pick := func(attempt int) string {
		cached, _ := channelCache.Get(channel.ID)
		now := time.Now().Unix()
		var avail []model.ChannelKey
		for _, k := range cached.Keys {
			if k.IsAvailable(now) {
				avail = append(avail, k)
			}
		}
		if len(avail) == 0 {
			return "<none>"
		}
		sort.SliceStable(avail, func(i, j int) bool { return avail[i].ID < avail[j].ID })
		return avail[attempt%len(avail)].ChannelKey
	}
	if got := pick(0); got != "key-one" {
		t.Fatalf("before 429 attempt0 = %q, want key-one", got)
	}
	ChannelKeyHealthUpdate(ChannelKeyHealthReport{
		ChannelID: channel.ID, KeyID: channel.Keys[0].ID,
		StatusCode: http.StatusTooManyRequests, RetryAfter: 300,
	})
	if got := pick(0); got != "key-two" {
		t.Fatalf("after 429 attempt0 = %q, want key-two", got)
	}
	// 第二个凭据也被限流后, 渠道应报告无可用凭据(relay 据此走 failover)。
	ChannelKeyHealthUpdate(ChannelKeyHealthReport{
		ChannelID: channel.ID, KeyID: channel.Keys[1].ID,
		StatusCode: http.StatusTooManyRequests, RetryAfter: 300,
	})
	if got := pick(0); got != "<none>" {
		t.Fatalf("both cooled = %q, want <none>", got)
	}
	// 恢复其一, 立即重回轮换。
	ChannelKeyHealthUpdate(ChannelKeyHealthReport{
		ChannelID: channel.ID, KeyID: channel.Keys[0].ID, StatusCode: http.StatusOK,
	})
	if got := pick(0); got != "key-one" {
		t.Fatalf("after recovery = %q, want key-one", got)
	}
	// 重启语义: 冷却已落库, 重新加载后仍然生效。
	// 先让 key-two 恢复, 使"重启后仍被跳过"只能由 key-one 的冷却解释。
	ChannelKeyHealthUpdate(ChannelKeyHealthReport{
		ChannelID: channel.ID, KeyID: channel.Keys[1].ID, StatusCode: http.StatusOK,
	})
	ChannelKeyHealthUpdate(ChannelKeyHealthReport{
		ChannelID: channel.ID, KeyID: channel.Keys[0].ID,
		StatusCode: http.StatusTooManyRequests, RetryAfter: 300,
	})
	if err := ChannelKeyHealthSaveDB(context.Background()); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := channelRefreshCache(context.Background()); err != nil {
		t.Fatalf("reload cache: %v", err)
	}
	if got := pick(0); got != "key-two" {
		t.Fatalf("after restart = %q, want key-two (cooldown must survive)", got)
	}
}
