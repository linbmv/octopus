package relay

import (
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/transformer"
)

// relayRun 保存一次客户端请求在负载均衡循环中共享的状态。
type relayRun struct {
	c                    *gin.Context
	inAdapter            transformer.Inbound
	internalRequest      *llm.Request
	metrics              *RelayMetrics
	iter                 *balancer.Iterator
	iterStack            []*relayIteratorFrame
	iterHistory          []*balancer.Iterator
	group                dbmodel.Group
	selectedBaseURLs     map[int]string
	resolveGroupItemFunc func(item dbmodel.GroupItem, sticky bool, stickyKeyID int) (*relayAttempt, error)
	runAttemptFunc       func(attempt *relayAttempt) (bool, error)
}

type relayIteratorFrame struct {
	group dbmodel.Group
	iter  *balancer.Iterator
	depth int
}

// relayAttempt 保存一次上游通道尝试的状态。
type relayAttempt struct {
	*relayRun

	outAdapter             transformer.Outbound
	channel                *dbmodel.Channel
	groupItem              dbmodel.GroupItem
	firstTokenPolicyGroup  *dbmodel.Group
	usedKey                dbmodel.ChannelKey
	keyOptions             []dbmodel.ChannelKey
	keyIndex               int
	baseURL                string
	keyRemark              string                    // 清洗后的本次 key 备注，用于 attempt 日志记录
	trackingID             string                    // 活跃请求跟踪 ID
	span                   *balancer.AttemptSpan     // attempt 追踪 span，用于记录首 token 时间
	streamActivity         <-chan struct{}           // successful streaming response raw-byte activity (including decoder-consumed heartbeats)
	compactStrategyUpdater compactStrategyUpdateFunc // optional per-attempt persistence hook used by focused policy tests
}
