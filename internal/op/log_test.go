package op

import (
	"context"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

func TestRelayLogServiceTokens(t *testing.T) {
	service := NewRelayLogService()

	token, err := service.StreamTokenCreate()
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	if token == "" {
		t.Fatal("expected token")
	}
	if !service.StreamTokenVerify(token) {
		t.Fatal("expected token to verify")
	}

	service.StreamTokenRevoke(token)
	if service.StreamTokenVerify(token) {
		t.Fatal("expected revoked token to fail verification")
	}
}

func TestRelayLogServiceListIncludesSuccessLogsNewestFirst(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()
	if err := settingRefreshCache(ctx); err != nil {
		t.Fatalf("refresh settings: %v", err)
	}

	service := NewRelayLogService()
	success := model.RelayLog{
		ID:               1,
		Time:             100,
		RequestModelName: "gpt-test",
		ChannelName:      "ok-channel",
		ActualModelName:  "gpt-test",
		ResponseContent:  `{"ok":true}`,
	}
	failure := model.RelayLog{
		ID:               2,
		Time:             100,
		RequestModelName: "gpt-test",
		ChannelName:      "bad-channel",
		ActualModelName:  "gpt-test",
		Error:            "upstream failed",
	}

	if err := service.Add(ctx, success); err != nil {
		t.Fatalf("add success log: %v", err)
	}
	if err := service.Add(ctx, failure); err != nil {
		t.Fatalf("add failure log: %v", err)
	}

	logs, err := service.List(ctx, nil, nil, 1, 10)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("log count = %d, want 2: %#v", len(logs), logs)
	}
	if logs[0].Error != failure.Error || logs[1].ResponseContent != success.ResponseContent {
		t.Fatalf("logs not sorted by newest id within same second: %#v", logs)
	}
	if logs[1].Error != "" || logs[1].ResponseContent == "" {
		t.Fatalf("success log missing from list or classified as error: %#v", logs[1])
	}
}

func TestRelayLogServiceSubscription(t *testing.T) {
	service := NewRelayLogService()
	ch := service.Subscribe()

	service.notifySubscribers(model.RelayLog{ID: 1})

	select {
	case got := <-ch:
		if got.ID != 1 {
			t.Fatalf("unexpected log id: %d", got.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relay log notification")
	}

	service.Unsubscribe(ch)
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected unsubscribe to close channel")
		}
	default:
		t.Fatal("expected closed channel")
	}
}
