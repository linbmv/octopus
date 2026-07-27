package relay

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/relay/errorclass"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

const geminiSimulationSuccess = `{"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2},"modelVersion":"gemini-2.5-flash"}`

type geminiSimulationChannel struct {
	channel *dbmodel.Channel
	keys    []dbmodel.ChannelKey
}

func TestGeminiFailoverSimulationExhaustsKeysThenSwitchesChannel(t *testing.T) {
	setGeminiSimulationTimeouts(t, 2)

	var mu sync.Mutex
	seenKeys := make([]string, 0, 3)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("x-goog-api-key")
		mu.Lock()
		seenKeys = append(seenKeys, key)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if key != "good-b" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT"}}`))
			return
		}
		_, _ = w.Write([]byte(geminiSimulationSuccess))
	}))
	t.Cleanup(upstream.Close)

	run := newGeminiSimulationRun(t, []dbmodel.GroupItem{
		{ID: 1, Type: dbmodel.GroupItemTypeChannel, ChannelID: 93001, ModelName: "gemini-2.5-flash", Priority: 1},
		{ID: 2, Type: dbmodel.GroupItemTypeChannel, ChannelID: 93002, ModelName: "gemini-2.5-flash", Priority: 2},
	})
	channels := map[int]geminiSimulationChannel{
		93001: newGeminiSimulationChannel(93001, upstream.URL, "bad-a1", "bad-a2"),
		93002: newGeminiSimulationChannel(93002, upstream.URL, "good-b"),
	}
	installGeminiSimulationResolver(t, run, channels)

	run.run()
	if run.c.Writer.Status() != http.StatusOK {
		t.Fatalf("relay status = %d, want 200; headers=%v", run.c.Writer.Status(), run.c.Writer.Header())
	}

	mu.Lock()
	gotKeys := append([]string(nil), seenKeys...)
	mu.Unlock()
	if want := []string{"bad-a1", "bad-a2", "good-b"}; !reflect.DeepEqual(gotKeys, want) {
		t.Fatalf("upstream key order = %v, want %v", gotKeys, want)
	}

	attempts := run.attempts()
	if len(attempts) != 3 {
		t.Fatalf("attempt count = %d, want 3: %+v", len(attempts), attempts)
	}
	if attempts[0].ErrorLevel != dbmodel.AttemptErrorLevel(errorclass.ErrorLevelKey.String()) ||
		attempts[1].ErrorLevel != dbmodel.AttemptErrorLevel(errorclass.ErrorLevelKey.String()) ||
		attempts[2].Status != dbmodel.AttemptSuccess {
		t.Fatalf("attempt outcomes do not show two key failures then success: %+v", attempts)
	}
}

func TestGeminiFailoverSimulationLeavesFullBudgetForSlowLastChannel(t *testing.T) {
	setGeminiSimulationTimeouts(t, 1)
	hung := newHungUpstream(t)

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(geminiSimulationSuccess))
	}))
	t.Cleanup(slow.Close)

	run := newGeminiSimulationRun(t, []dbmodel.GroupItem{
		{ID: 1, Type: dbmodel.GroupItemTypeChannel, ChannelID: 93011, ModelName: "gemini-2.5-flash", Priority: 1},
		{ID: 2, Type: dbmodel.GroupItemTypeChannel, ChannelID: 93012, ModelName: "gemini-2.5-flash", Priority: 2},
	})
	channels := map[int]geminiSimulationChannel{
		93011: newGeminiSimulationChannel(93011, hung.URL, "hung"),
		93012: newGeminiSimulationChannel(93012, slow.URL, "slow-stable"),
	}
	installGeminiSimulationResolver(t, run, channels)

	started := time.Now()
	run.run()
	elapsed := time.Since(started)
	if run.c.Writer.Status() != http.StatusOK {
		t.Fatalf("relay status = %d, want slow fallback success", run.c.Writer.Status())
	}
	if elapsed < 2*time.Second || elapsed > 5*time.Second {
		t.Fatalf("failover elapsed = %v, want about 2.2s (1s hung candidate + 1.2s slow success)", elapsed)
	}

	attempts := run.attempts()
	if len(attempts) != 2 || attempts[0].ErrorReason != "non-stream attempt timeout" || attempts[1].Status != dbmodel.AttemptSuccess {
		t.Fatalf("attempt outcomes = %+v, want timeout then slow success", attempts)
	}
}

func TestGeminiFailoverSimulationTraversesAllFastFailingCandidates(t *testing.T) {
	setGeminiSimulationTimeouts(t, 1)

	var mu sync.Mutex
	seenKeys := make([]string, 0, 12)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("x-goog-api-key")
		mu.Lock()
		seenKeys = append(seenKeys, key)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if key != "candidate-12" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"service temporarily unavailable"}}`))
			return
		}
		_, _ = w.Write([]byte(geminiSimulationSuccess))
	}))
	t.Cleanup(upstream.Close)

	items := make([]dbmodel.GroupItem, 0, 12)
	channels := make(map[int]geminiSimulationChannel, 12)
	for i := 1; i <= 12; i++ {
		channelID := 93100 + i
		items = append(items, dbmodel.GroupItem{
			ID: i, Type: dbmodel.GroupItemTypeChannel, ChannelID: channelID,
			ModelName: "gemini-2.5-flash", Priority: i,
		})
		channels[channelID] = newGeminiSimulationChannel(channelID, upstream.URL, fmt.Sprintf("candidate-%d", i))
	}
	run := newGeminiSimulationRun(t, items)
	installGeminiSimulationResolver(t, run, channels)

	run.run()
	if run.c.Writer.Status() != http.StatusOK {
		t.Fatalf("relay status = %d, want candidate 12 success", run.c.Writer.Status())
	}
	mu.Lock()
	gotKeys := append([]string(nil), seenKeys...)
	mu.Unlock()
	wantKeys := make([]string, 12)
	for i := range wantKeys {
		wantKeys[i] = fmt.Sprintf("candidate-%d", i+1)
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("candidate traversal = %v, want %v", gotKeys, wantKeys)
	}
	if attempts := run.attempts(); len(attempts) != 12 || attempts[11].Status != dbmodel.AttemptSuccess {
		t.Fatalf("attempt outcomes = %+v, want 11 failures then success", attempts)
	}
}

func TestGeminiFailoverSimulationErrorStateMatrix(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		headers    http.Header
		body       string
		wantLevel  errorclass.ErrorLevel
		wantAction ErrorAction
	}{
		{
			name:       "generic 503 skips remaining keys",
			status:     http.StatusServiceUnavailable,
			body:       `{"error":{"message":"service temporarily unavailable"}}`,
			wantLevel:  errorclass.ErrorLevelChannel,
			wantAction: ErrorActionRetryChannel,
		},
		{
			name:       "model permission 503 rotates key first",
			status:     http.StatusServiceUnavailable,
			body:       `{"error":{"message":"No available channel for model gemini-2.5-flash"}}`,
			wantLevel:  errorclass.ErrorLevelKey,
			wantAction: ErrorActionRetryKey,
		},
		{
			name:       "ambiguous resource exhausted rotates account key",
			status:     http.StatusTooManyRequests,
			body:       `{"error":{"code":429,"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED"}}`,
			wantLevel:  errorclass.ErrorLevelKey,
			wantAction: ErrorActionRetryKey,
		},
		{
			name:       "global rate limit skips channel",
			status:     http.StatusTooManyRequests,
			headers:    http.Header{"X-Ratelimit-Scope": {"global"}},
			body:       `{"error":{"message":"rate limited"}}`,
			wantLevel:  errorclass.ErrorLevelChannel,
			wantAction: ErrorActionRetryChannel,
		},
		{
			name:       "upstream 408 probes another channel without health penalty",
			status:     http.StatusRequestTimeout,
			body:       `{"error":{"message":"Request Timeout"}}`,
			wantLevel:  errorclass.ErrorLevelClient,
			wantAction: ErrorActionRetryChannel,
		},
		{
			name:       "deterministic payload error stops traversal",
			status:     http.StatusBadRequest,
			body:       `{"error":{"type":"invalid_request_error","message":"maximum context length exceeded"}}`,
			wantLevel:  errorclass.ErrorLevelClient,
			wantAction: ErrorActionReturnClient,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := decideRelayError(tt.status, tt.headers, []byte(tt.body), fmt.Errorf("upstream status %d", tt.status))
			if decision.Classification.Level != tt.wantLevel || decision.Action != tt.wantAction {
				t.Fatalf("decision = level:%s action:%s reason:%q, want level:%s action:%s",
					decision.Classification.Level, decision.Action, decision.Classification.Reason, tt.wantLevel, tt.wantAction)
			}
		})
	}
}

func newGeminiSimulationRun(t *testing.T, items []dbmodel.GroupItem) *relayRun {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", nil)

	stream := false
	request := &llm.Request{
		Model:       "gemini-2.5-flash",
		APIFormat:   llm.APIFormatGeminiContents,
		RequestType: llm.RequestTypeChat,
		Stream:      &stream,
		Messages:    []llm.Message{compactInputMessage("user", "Reply with OK.")},
		RawRequest: &httpclient.Request{
			Method:  http.MethodPost,
			Path:    "/v1beta/models/gemini-2.5-flash:generateContent",
			Headers: http.Header{"Content-Type": {"application/json"}},
			Body:    []byte(`{"contents":[{"parts":[{"text":"Reply with OK."}]}]}`),
		},
	}
	group := dbmodel.Group{Mode: dbmodel.GroupModeFailover, Items: items}
	iter := balancer.NewIterator(group, 1, request.Model)
	run := &relayRun{
		c:                   ginCtx,
		inAdapter:           newInbound(llm.APIFormatGeminiContents),
		internalRequest:     request,
		metrics:             &RelayMetrics{StartTime: time.Now(), RequestModel: request.Model, ActualModel: request.Model},
		iter:                iter,
		iterStack:           []*relayIteratorFrame{{group: group, iter: iter}},
		iterHistory:         []*balancer.Iterator{iter},
		group:               group,
		maxUpstreamAttempts: conf.Current().Relay.MaxUpstreamAttempts,
	}
	run.attachIteratorTimeline(iter)
	return run
}

func newGeminiSimulationChannel(channelID int, baseURL string, keys ...string) geminiSimulationChannel {
	channelKeys := make([]dbmodel.ChannelKey, len(keys))
	for i, key := range keys {
		channelKeys[i] = dbmodel.ChannelKey{
			ID:         channelID*10 + i + 1,
			ChannelID:  channelID,
			Enabled:    true,
			ChannelKey: key,
		}
	}
	return geminiSimulationChannel{
		channel: &dbmodel.Channel{
			ID:       channelID,
			Name:     fmt.Sprintf("gemini-sim-%d", channelID),
			Type:     llm.APIFormatGeminiContents,
			Enabled:  true,
			BaseUrls: []dbmodel.BaseUrl{{URL: baseURL}},
			Keys:     channelKeys,
		},
		keys: channelKeys,
	}
}

func installGeminiSimulationResolver(t *testing.T, run *relayRun, channels map[int]geminiSimulationChannel) {
	t.Helper()
	run.resolveGroupItemFunc = func(item dbmodel.GroupItem, _ bool, _ int) (*relayAttempt, error) {
		spec, ok := channels[item.ChannelID]
		if !ok {
			return nil, fmt.Errorf("missing simulation channel %d", item.ChannelID)
		}
		outbound, err := newOutbound(spec.channel.Type, run.internalRequest, spec.channel.GetBaseUrl(), spec.keys[0].ChannelKey)
		if err != nil {
			return nil, err
		}
		return &relayAttempt{
			relayRun:       run,
			outAdapter:     outbound,
			channel:        spec.channel,
			groupItem:      item,
			usedKey:        spec.keys[0],
			keyOptions:     spec.keys,
			baseURL:        spec.channel.GetBaseUrl(),
			baseURLOptions: []string{spec.channel.GetBaseUrl()},
		}, nil
	}
}

func setGeminiSimulationTimeouts(t *testing.T, attemptSeconds int) {
	t.Helper()
	old := conf.Current()
	config := old
	config.Relay.NonStreamAttemptTimeoutSeconds = attemptSeconds
	config.Relay.ResponseHeaderTimeoutSeconds = 0
	config.Relay.NonStreamTimeoutSeconds = 10
	config.Relay.MaxUpstreamAttempts = 0
	if err := conf.Set(config); err != nil {
		t.Fatalf("conf.Set simulation config: %v", err)
	}
	t.Cleanup(func() {
		if err := conf.Set(old); err != nil {
			t.Errorf("restore config: %v", err)
		}
	})
}
