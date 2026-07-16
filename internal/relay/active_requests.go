package relay

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// ActiveRequestState 活跃请求状态
type ActiveRequestState string

const (
	StateForwarding      ActiveRequestState = "forwarding"       // 正在转发
	StateWaitingUpstream ActiveRequestState = "waiting_upstream" // 等待上游响应
	StateStreaming       ActiveRequestState = "streaming"        // 流式传输中
)

// ActiveRequest 活跃请求信息
type ActiveRequest struct {
	ID            string             `json:"id"`
	Model         string             `json:"model"`
	ChannelID     int                `json:"channel_id"`
	ChannelName   string             `json:"channel_name"`
	ChannelKeyID  int                `json:"channel_key_id"`
	StartTime     time.Time          `json:"start_time"`
	State         ActiveRequestState `json:"state"`
	ElapsedMs     int64              `json:"elapsed_ms"` // 已耗时（毫秒）
	APIKeyID      int                `json:"api_key_id"`
	IsStreaming   bool               `json:"is_streaming"`
	AttemptNumber int                `json:"attempt_number"` // 当前尝试次数
}

// activeRequestManager 活跃请求管理器
type activeRequestManager struct {
	mu       sync.RWMutex
	requests map[string]*ActiveRequest
}

var globalActiveRequests = &activeRequestManager{
	requests: make(map[string]*ActiveRequest),
}

// StartTracking 开始跟踪请求
func StartTracking(model string, channelID int, channelName string, channelKeyID int, apiKeyID int, attemptNumber int) string {
	id := uuid.New().String()
	req := &ActiveRequest{
		ID:            id,
		Model:         model,
		ChannelID:     channelID,
		ChannelName:   channelName,
		ChannelKeyID:  channelKeyID,
		StartTime:     time.Now(),
		State:         StateForwarding,
		APIKeyID:      apiKeyID,
		IsStreaming:   false,
		AttemptNumber: attemptNumber,
	}

	globalActiveRequests.mu.Lock()
	globalActiveRequests.requests[id] = req
	globalActiveRequests.mu.Unlock()

	return id
}

// UpdateState 更新请求状态
func UpdateState(id string, state ActiveRequestState) {
	globalActiveRequests.mu.Lock()
	defer globalActiveRequests.mu.Unlock()

	if req, ok := globalActiveRequests.requests[id]; ok {
		req.State = state
		if state == StateStreaming {
			req.IsStreaming = true
		}
	}
}

// StopTracking 停止跟踪请求
func StopTracking(id string) {
	globalActiveRequests.mu.Lock()
	delete(globalActiveRequests.requests, id)
	globalActiveRequests.mu.Unlock()
}

// GetActiveRequests 获取所有活跃请求
func GetActiveRequests() []*ActiveRequest {
	globalActiveRequests.mu.RLock()
	defer globalActiveRequests.mu.RUnlock()

	now := time.Now()
	result := make([]*ActiveRequest, 0, len(globalActiveRequests.requests))
	for _, req := range globalActiveRequests.requests {
		// 创建副本并计算已耗时
		reqCopy := *req
		reqCopy.ElapsedMs = now.Sub(req.StartTime).Milliseconds()
		result = append(result, &reqCopy)
	}

	return result
}

// GetActiveRequestCount 获取活跃请求数量
func GetActiveRequestCount() int {
	globalActiveRequests.mu.RLock()
	defer globalActiveRequests.mu.RUnlock()
	return len(globalActiveRequests.requests)
}
