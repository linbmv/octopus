package health

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// HealthAPI HTTP API 处理器
type HealthAPI struct {
	manager *HealthManager
}

// NewHealthAPI 创建 API 处理器
func NewHealthAPI(manager *HealthManager) *HealthAPI {
	return &HealthAPI{
		manager: manager,
	}
}

// HealthStatusResponse 健康状态响应
type HealthStatusResponse struct {
	ChannelID int                 `json:"channel_id"`
	KeyID     int                 `json:"key_id"`
	Model     string              `json:"model"`
	Score     float64             `json:"score"`
	Stats     HealthStatsResponse `json:"stats"`
	Timeout   int64               `json:"adaptive_timeout_ms"`
	Policy    TimeoutPolicy       `json:"timeout_policy"`
}

// HealthStatsResponse 统计信息响应
type HealthStatsResponse struct {
	TotalCount                 int64   `json:"total_count"`
	SuccessCount               int64   `json:"success_count"`
	SuccessRate                float64 `json:"success_rate"`
	TimeoutCount               int64   `json:"timeout_count"`
	AutoFirstTokenTimeoutCount int64   `json:"auto_first_token_timeout_count"`
	NetworkCount               int64   `json:"network_count"`
	RateLimitCount             int64   `json:"rate_limit_count"`
	ModelErrorCount            int64   `json:"model_error_count"`
	KeyErrorCount              int64   `json:"key_error_count"`

	FirstTokenP50MS int64   `json:"first_token_p50_ms"`
	FirstTokenP95MS int64   `json:"first_token_p95_ms"`
	FirstTokenP99MS int64   `json:"first_token_p99_ms"`
	CV              float64 `json:"cv"`

	ConsecutiveSuccess int `json:"consecutive_success"`
	ConsecutiveFailure int `json:"consecutive_failure"`
	ConsecutiveTimeout int `json:"consecutive_timeout"`

	LastEventAt string `json:"last_event_at"`
}

// HandleGetAll 获取所有健康状态
// GET /health/status
func (api *HealthAPI) HandleGetAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	allStates := api.manager.GetAllStates()

	response := make([]HealthStatusResponse, 0, len(allStates))
	for key, stats := range allStates {
		health, ok := api.manager.Get(key)
		if !ok {
			continue
		}

		response = append(response, HealthStatusResponse{
			ChannelID: key.ChannelID,
			KeyID:     key.KeyID,
			Model:     key.Model,
			Score:     health.GetScore(),
			Stats:     convertStats(stats),
			Timeout:   health.GetTimeout().Milliseconds(),
			Policy:    health.GetTimeoutPolicy(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"count":  len(response),
		"states": response,
	})
}

// HandleGetByChannel 获取指定渠道的健康状态
// GET /health/status/{channel_id}
func (api *HealthAPI) HandleGetByChannel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 从 URL 路径解析 channel_id
	// 注意：这里假设使用简单的路径解析，实际项目中应该使用路由器
	channelIDStr := r.URL.Query().Get("channel_id")
	if channelIDStr == "" {
		http.Error(w, "Missing channel_id parameter", http.StatusBadRequest)
		return
	}

	channelID, err := strconv.Atoi(channelIDStr)
	if err != nil {
		http.Error(w, "Invalid channel_id", http.StatusBadRequest)
		return
	}

	// 查找所有匹配的健康状态
	allStates := api.manager.GetAllStates()

	response := make([]HealthStatusResponse, 0)
	for key, stats := range allStates {
		if key.ChannelID != channelID {
			continue
		}

		health, ok := api.manager.Get(key)
		if !ok {
			continue
		}

		response = append(response, HealthStatusResponse{
			ChannelID: key.ChannelID,
			KeyID:     key.KeyID,
			Model:     key.Model,
			Score:     health.GetScore(),
			Stats:     convertStats(stats),
			Timeout:   health.GetTimeout().Milliseconds(),
			Policy:    health.GetTimeoutPolicy(),
		})
	}

	if len(response) == 0 {
		http.Error(w, "Channel not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"channel_id": channelID,
		"count":      len(response),
		"states":     response,
	})
}

// HandleGetSpecific 获取特定渠道+Key+Model的健康状态
// GET /health/status/specific?channel_id=1&key_id=100&model=gpt-4
func (api *HealthAPI) HandleGetSpecific(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 解析参数
	channelIDStr := r.URL.Query().Get("channel_id")
	keyIDStr := r.URL.Query().Get("key_id")
	model := r.URL.Query().Get("model")

	if channelIDStr == "" || keyIDStr == "" || model == "" {
		http.Error(w, "Missing required parameters: channel_id, key_id, model", http.StatusBadRequest)
		return
	}

	channelID, err := strconv.Atoi(channelIDStr)
	if err != nil {
		http.Error(w, "Invalid channel_id", http.StatusBadRequest)
		return
	}

	keyID, err := strconv.Atoi(keyIDStr)
	if err != nil {
		http.Error(w, "Invalid key_id", http.StatusBadRequest)
		return
	}

	// 查找健康状态
	key := HealthKey{
		ChannelID: channelID,
		KeyID:     keyID,
		Model:     model,
	}

	health, ok := api.manager.Get(key)
	if !ok {
		http.Error(w, "Health state not found", http.StatusNotFound)
		return
	}

	stats := health.GetStats()
	response := HealthStatusResponse{
		ChannelID: key.ChannelID,
		KeyID:     key.KeyID,
		Model:     key.Model,
		Score:     health.GetScore(),
		Stats:     convertStats(stats),
		Timeout:   health.GetTimeout().Milliseconds(),
		Policy:    health.GetTimeoutPolicy(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// HandleReset 重置健康状态
// POST /health/reset
func (api *HealthAPI) HandleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	api.manager.Reset()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Health states reset successfully",
	})
}

// HandleEnable 启用健康系统
// POST /health/enable
func (api *HealthAPI) HandleEnable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	api.manager.Enable()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Health system enabled",
	})
}

// HandleDisable 禁用健康系统
// POST /health/disable
func (api *HealthAPI) HandleDisable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	api.manager.Disable()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Health system disabled",
	})
}

// convertStats 转换统计信息
func convertStats(stats HealthStats) HealthStatsResponse {
	lastEventAt := ""
	if !stats.LastEventAt.IsZero() {
		lastEventAt = stats.LastEventAt.Format(time.RFC3339)
	}

	return HealthStatsResponse{
		TotalCount:                 stats.TotalCount,
		SuccessCount:               stats.SuccessCount,
		SuccessRate:                stats.SuccessRate,
		TimeoutCount:               stats.TimeoutCount,
		AutoFirstTokenTimeoutCount: stats.AutoFirstTokenTimeoutCount,
		NetworkCount:               stats.NetworkCount,
		RateLimitCount:             stats.RateLimitCount,
		ModelErrorCount:            stats.ModelErrorCount,
		KeyErrorCount:              stats.KeyErrorCount,

		FirstTokenP50MS: stats.FirstTokenP50.Milliseconds(),
		FirstTokenP95MS: stats.FirstTokenP95.Milliseconds(),
		FirstTokenP99MS: stats.FirstTokenP99.Milliseconds(),
		CV:              stats.CV,

		ConsecutiveSuccess: stats.ConsecutiveSuccess,
		ConsecutiveFailure: stats.ConsecutiveFailure,
		ConsecutiveTimeout: stats.ConsecutiveTimeout,

		LastEventAt: lastEventAt,
	}
}
